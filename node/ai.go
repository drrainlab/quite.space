// The personal assistant, as an ordinary Agent Terminal (AI-0).
//
// It is not a special case in the runtime. It is a terminal with a manifest,
// a member card and a space, and it appears in the Navigator like anything
// else. What makes it different is only where it lives: nowhere but here.
//
// Three decisions are worth stating, because each is easy to get wrong in a
// way nothing would complain about.
//
// It signs with its OWN terminal and device keys, because authorship is
// enforced against the emitting participant's manifest and event chains are
// ordered by the signing device. Sharing either with the person would make
// honest authorship impossible or fork the log.
//
// It does NOT get its own principal. The controller claim is the person's,
// because this is an assistant somebody runs, not an independent subject.
// The consequence is that "did I write this" stops being answerable by
// comparing principals — so the API now also asks whether a human wrote it,
// which was always the more honest question.
//
// It is NOT added to the epoch member set. It seals through the space
// replica's current key and needs no wrap of its own, and adding it would
// make the relay push path see a second recipient device — turning a space
// that never leaves the device into one with a peer.
//
// The promise in aicompose.go, "AI never writes the log", is refined here
// rather than broken: it never writes a SHARED log that others read. In a
// space with one human member and no way out, an answer that is an event is
// simply an answer you can still find tomorrow.
package node

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/agent"
)

// ErrLocalOnly refuses anything that would take a local-only space off this
// device. It is one sentence rather than a code because every caller shows
// it to a person.
var ErrLocalOnly = errors.New("node: this space never leaves this device")

// AgentLabel is what the assistant calls itself.
const AgentLabel = "Quite AI"

// Context bounds. Both, because either alone is the wrong limit: twenty
// one-word messages are not a context, and twenty long ones are too much.
const (
	aiWindowEntries = 20
	aiWindowChars   = 6000
)

// loadAgentLocked rebuilds the assistant participant from the keystore.
// Called from Open, before any space is attached, so ResumeChain can run
// alongside the person's own.
func (r *Runtime) loadAgentLocked() error {
	rec := r.ks.Agent
	if !rec.Exists() {
		return nil
	}
	dev, err := identity.NewDeviceFromKeys(rec.DeviceSeed, rec.DeviceX25519)
	if err != nil {
		return err
	}
	// The PERSON's principal, deliberately — see the file header.
	p, err := terminals.NewParticipantFromManifest(r.PrincipalID, dev,
		rec.TerminalSeed, rec.ManifestFrame)
	if err != nil {
		return err
	}
	r.agent = p
	// MD-0: an owned device is certified as soon as it exists, restore
	// included — a device the person owns must never be the one refused.
	r.certifyOwnedDeviceLocked(dev)
	return nil
}

// EnsureAISpace returns the assistant's space, creating it on first use.
//
// Lazily, not at LLM setup: configuring a provider is not a request for a
// space, and an empty room sitting in the Navigator before anybody wanted
// one is clutter.
func (r *Runtime) EnsureAISpace() (id.TerminalID, error) {
	r.mu.Lock()
	if r.ks.Agent.Space != (id.TerminalID{}) {
		tid := r.ks.Agent.Space
		r.mu.Unlock()
		return tid, nil
	}
	r.mu.Unlock()

	// Mint the agent's own identity if this is the first time.
	if r.agent == nil {
		dev, err := identity.NewDevice(identity.NewRand())
		if err != nil {
			return id.TerminalID{}, err
		}
		p, seed, err := terminals.NewParticipantFrom(r.PrincipalID, dev, nil,
			agent.Template(AgentLabel))
		if err != nil {
			return id.TerminalID{}, err
		}
		r.mu.Lock()
		r.agent = p
		r.certifyOwnedDeviceLocked(dev)
		r.ks.Agent = storage.AgentRecord{
			DeviceSeed: dev.Seed(), DeviceX25519: dev.X25519Priv(),
			TerminalSeed: seed, ManifestFrame: p.ManifestFrame,
		}
		r.mu.Unlock()
	}

	// Memory "everything", not private_history: people will want to pass an
	// answer on to somebody, and sealing the past inside a room with one
	// human member would be policy theatre.
	// LocalOnly at creation, not after it: the space is attached to
	// r.spaces inside this call, and the announce and relay loops read
	// that map — a tick landing between the attach and a later stamp
	// would have seen the AI's room without its flag.
	tid, err := r.CreateSpaceWithOptions(AgentLabel, CreateOptions{
		Character: terminals.DefaultCharacter("orbit"),
		LocalOnly: true,
	})
	if err != nil {
		return id.TerminalID{}, err
	}

	r.mu.Lock()
	r.ks.Agent.Space = tid
	st := r.spaces[tid]
	// The agent publishes its manifest so the room honestly shows what is
	// in it: MemberCards already projects Kind, Agency, AIPresent and
	// Autonomy, so no new member UI is needed for the truth to be visible.
	if st != nil && r.agent != nil {
		if _, _, err := r.agent.PublishManifest(st.space); err != nil {
			r.mu.Unlock()
			return id.TerminalID{}, err
		}
	}
	err = r.saveKeystore()
	r.mu.Unlock()
	return tid, err
}

// AIStatus is what the interface needs to say the true thing about where a
// question goes before somebody asks it.
type AIStatus struct {
	Space      string `json:"space,omitempty"`
	Configured bool   `json:"configured"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	// Local is true when the provider runs on this machine, which is the
	// difference between "nothing leaves the device" and "every question is
	// sent over the internet".
	Local bool `json:"local"`
	// Pending is the question currently out with the provider, if any.
	Pending string `json:"pending,omitempty"`
	// LastError is a DEVICE-LOCAL diagnostic. It is never an event: the log
	// records what happened between participants, and "your provider
	// returned 401" is not one of those things.
	LastError string `json:"last_error,omitempty"`
}

// AI reports the assistant's state.
func (r *Runtime) AI() AIStatus {
	cfg := r.LLMConfig()
	r.mu.Lock()
	defer r.mu.Unlock()
	st := AIStatus{
		Configured: cfg.Provider != "" && cfg.Model != "",
		Provider:   cfg.Provider, Model: cfg.Model,
		Local:   cfg.Provider == "local" || cfg.Provider == "ollama",
		Pending: r.aiPending, LastError: r.aiError,
	}
	if sp := r.ks.Agent.Space; sp != (id.TerminalID{}) {
		st.Space = sp.Hex()
	}
	return st
}

// Ask puts a question to the assistant. The question is YOUR event, emitted
// immediately; the answer arrives later as the agent's own.
func (r *Runtime) Ask(text string) (id.TerminalID, id.EventID, error) {
	if strings.TrimSpace(text) == "" {
		return id.TerminalID{}, id.EventID{}, errors.New("node: nothing to ask")
	}
	cfg := r.LLMConfig()
	// Refuse BEFORE emitting: a question with nowhere to go would sit in
	// the log unanswered forever, and the log would be recording a promise
	// this device never made.
	if cfg.Provider == "" || cfg.Model == "" {
		return id.TerminalID{}, id.EventID{}, errors.New(
			"node: set up an AI provider in Settings first — this space has nowhere to send the question")
	}
	tid, err := r.EnsureAISpace()
	if err != nil {
		return id.TerminalID{}, id.EventID{}, err
	}
	eid, err := r.Say(tid, text, SayOptions{})
	if err != nil {
		return id.TerminalID{}, id.EventID{}, err
	}

	r.mu.Lock()
	r.aiPending, r.aiError = eid.Hex(), ""
	r.mu.Unlock()

	r.wg.Add(1)
	go r.answer(tid, eid, cfg.Model)
	return tid, eid, nil
}

// answer runs the provider call and emits the reply. Everything that can go
// wrong here is reported device-locally: the space must not fill with
// apologies from a machine about its own plumbing.
func (r *Runtime) answer(tid id.TerminalID, question id.EventID, model string) {
	defer r.wg.Done()
	sys, user := r.aiPrompt(tid)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := r.llm().Generate(ctx, r.LLMConfig(), sys, user)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.aiPending = ""
	if err != nil {
		r.aiError = err.Error()
		return
	}
	st := r.spaces[tid]
	if st == nil || r.agent == nil {
		r.aiError = "node: the assistant's space is not open"
		return
	}
	if _, err := agent.Say(r.agent, st.space, strings.TrimSpace(out), model,
		uint64(time.Now().Unix())); err != nil {
		r.aiError = err.Error()
		return
	}
	r.aiError = ""
	r.persistEpochsLocked(tid, st.space)
	_ = question
}

// aiPrompt assembles the window. There is no conversation API behind
// Generate — it is one shot — so the node builds the context itself and the
// interface says exactly how much of it there is.
func (r *Runtime) aiPrompt(tid id.TerminalID) (system, user string) {
	system = "You are answering inside one person's private local space on their own device. " +
		"You have only the exchange quoted below and nothing else — no memory of " +
		"other conversations, no access to any other space, no tools. " +
		"Do not claim otherwise. Answer plainly."

	r.mu.Lock()
	st := r.spaces[tid]
	var lines []string
	if st != nil {
		msgs := st.space.State.Messages()
		total := 0
		for i := len(msgs) - 1; i >= 0 && len(lines) < aiWindowEntries; i-- {
			m := msgs[i]
			who := "You"
			if m.ProducedBy == signal.AuthorshipAIAgent {
				who = AgentLabel
			}
			line := who + ": " + m.Text
			if total+len(line) > aiWindowChars {
				break
			}
			total += len(line)
			lines = append([]string{line}, lines...)
		}
	}
	r.mu.Unlock()
	return system, strings.Join(lines, "\n")
}

// ---- HTTP ----

func (a *APIServer) handleAI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.rt.AI())
}

func (a *APIServer) handleAsk(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Text string `json:"text"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	tid, eid, err := a.rt.Ask(body.Text)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{"space": tid.Hex(), "question": eid.Hex()})
}

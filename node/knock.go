package node

// KNOCKING ON A PERSON — ADR-027.
//
// The verb the product never had: not "make a room and hand somebody a
// link", but "ask this person for a conversation". Four rules shape
// everything here, and each is a decision written down in the ADR:
//
//	the envelope goes to the person's MAILBOX, never into the room they
//	  share — that they were asked is a fact belonging to two people;
//	only somebody already in a shared PRIVATE space may knock, and the
//	  RECIPIENT verifies that from its own log, never from the knock;
//	three answers, of which only "do not ask" is remembered;
//	a remembered refusal answers on the person's behalf, forever, with
//	  the sentence they wrote once — never silence, never a NEW answer.
//
// The last one is the load-bearing kindness: a refused knocker always
// gets a reply and can never learn anything by knocking again, because
// "she declined again" and "she never saw it" are the same fact.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relay"
)

const (
	// knockTTL is how long a knock keeps asking. A day, for the reason the
	// door already gives: an hour is dishonest when somebody may be asleep.
	knockTTL = 24 * time.Hour
	// maxPendingKnocks bounds the queue a person can be made to look at —
	// the door's own bound, for the door's own reason: a queue nobody can
	// drain is a memory-growth vector for anybody who can reach the box.
	maxPendingKnocks = 20
	// maxRefusals bounds the remembered refusals. It is not the defence —
	// the acquaintance floor is — so it may be small and prune the oldest.
	maxRefusals = 512
)

// PendingKnock is a knock waiting for the person to answer.
type PendingKnock struct {
	ID        string `json:"id"`
	Principal string `json:"principal"`
	// Name is what the asker's manifest calls them IN THE SHARED SPACE —
	// resolved locally from the member card, never from the envelope. A
	// stranger does not get to choose the name they arrive under.
	Name string `json:"name,omitempty"`
	// Via names the room that makes this person not-a-stranger, so the
	// answer is given with the reason for it on screen.
	Via      string `json:"via"`
	ViaTitle string `json:"via_title,omitempty"`
	// Line is the asker's own sentence. Rendered as somebody else's words.
	Line       string `json:"line,omitempty"`
	ReceivedAt int64  `json:"received_at"`
	ExpiresAt  int64  `json:"expires_at"`
}

// knockState is the receiving half, held in memory and rebuilt from the
// mailbox: a knock is a request, not a record, and one that is never
// answered simply expires.
type knockState struct {
	mu      sync.Mutex
	pending map[[32]byte]*heldKnock
	// answered remembers what this device already replied to a knock id,
	// so a re-delivered envelope gets the same answer and the person is
	// not asked twice about one question.
	answered map[[32]byte]bool
}

type heldKnock struct {
	knock      *terminals.Knock
	principal  id.PrincipalID
	xpub       [32]byte
	receivedAt int64
}

func (r *Runtime) knocksInit() {
	if r.knocks == nil {
		r.knocks = &knockState{
			pending:  map[[32]byte]*heldKnock{},
			answered: map[[32]byte]bool{},
		}
	}
}

// ---- asking ----

// KnockOn asks one person, met in one space, for a conversation.
//
// It creates the room the two would share and mints a pass to it, and the
// knock carries that pass: authority to REQUEST entry and nothing else
// (ADR-012 invariant 1), so a person who never answers has still given
// nothing away. The room exists before the answer because a pass must
// name a space — and an unanswered knock leaves an empty room this device
// can forget, which is cheaper than a protocol that has to invent one
// after the fact.
func (r *Runtime) KnockOn(via id.TerminalID, who id.PrincipalID, line string) (string, error) {
	if err := terminals.ValidateKnockLine(line); err != nil {
		return "", err
	}
	if who == r.PrincipalID {
		return "", errors.New("node: that is you")
	}

	r.mu.Lock()
	// THE FLOOR, CHECKED ON THE ASKING SIDE TOO — not as a security
	// measure (the recipient checks its own log; that is the one that
	// counts) but as honesty: a knock that will be refused as unacquainted
	// should not be sent, and the person asking deserves to be told why
	// rather than to wait a day for silence.
	st, ok := r.spaces[via]
	if !ok {
		r.mu.Unlock()
		return "", errors.New("node: unknown space")
	}
	if meta := r.ks.Spaces[via]; meta.LocalOnly || meta.Visibility == "public" {
		r.mu.Unlock()
		return "", errors.New("node: a knock needs a private room you already share")
	}
	member := false
	for _, c := range st.space.MemberCards(0) {
		if c.Principal == who {
			member = true
		}
	}
	if !member {
		r.mu.Unlock()
		return "", errors.New("node: that person is not in this space")
	}
	// Their certified devices, from certificates this node already holds —
	// the same source the epoch expansion reads.
	devices := r.ident.store.CertifiedDevices(who)
	certs := r.ownCertFramesLocked()
	selfXpub := r.Device.X25519Pub
	r.mu.Unlock()
	if len(devices) == 0 {
		return "", errors.New("node: no certified device is known for that person yet")
	}

	title := ""
	tid, err := r.CreateSpace(title) // unnamed: it is named by who is in it
	if err != nil {
		return "", err
	}
	addr := r.PersonalRelayAddress()
	if addr == "" {
		return "", errors.New("node: a knock needs a relay to leave the envelope at")
	}
	pass, err := r.MintPass(tid, 1, uint64(knockTTL/time.Hour), addr)
	if err != nil {
		return "", err
	}

	var kid [32]byte
	var reply [16]byte
	if _, err := rand.Read(kid[:]); err != nil {
		return "", err
	}
	if _, err := rand.Read(reply[:]); err != nil {
		return "", err
	}
	now := time.Now()
	k := &terminals.Knock{
		ID: kid, From: r.Device.ID, Principal: r.PrincipalID, Certs: certs,
		Via: via, Line: strings.TrimSpace(line), Pass: pass.Link,
		Reply: reply, ExpiresAt: uint64(now.Add(knockTTL).Unix()),
		XPub: selfXpub,
	}

	// One envelope per certified device: the person answers on whichever
	// one they pick up, and the others stop asking when the answer lands
	// (a decision is addressed to the asker, and the recipient's own
	// devices converge through the identity plane).
	expires := uint64(now.Add(knockTTL).Unix())
	bucket := relay.Bucket(uint64(now.Unix()))
	sent := 0
	for _, d := range devices {
		sealed, err := terminals.SealKnock(k, d.Device, d.X25519Pub, r.Device.SignKey())
		if err != nil {
			continue
		}
		ep := r.routeForDevice(d.Device, addr)
		if ep == "" {
			continue
		}
		hint := relay.HintKnock(d.Device, bucket)
		if err := r.withRelayControl(ep, func(client *relay.Client) error {
			_, err := client.Put(hint, expires, sealed)
			return err
		}); err == nil {
			sent++
		}
	}
	if sent == 0 {
		return "", errors.New("node: could not leave the knock anywhere that person listens")
	}
	r.mu.Lock()
	r.knocksInit()
	r.mu.Unlock()
	return tid.Hex(), nil
}

// routeForDevice is the peer's stated relay when the book has one, this
// node's own as the courtesy otherwise — the same rule the identity plane
// uses, and for the same reason: a guess is an attempt, not a promise.
func (r *Runtime) routeForDevice(dev id.DeviceID, fallback string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rt := range r.ks.PeerRoutes[dev] {
		if rt.Transport == "relay" && rt.Endpoint != "" {
			return rt.Endpoint
		}
	}
	return fallback
}

// ---- receiving ----

// fetchKnocks reads this device's knock mailbox once. Called from the sync
// cycle beside the identity plane.
func (r *Runtime) fetchKnocks(addr string) {
	if addr == "" {
		return
	}
	if _, yes := r.relayThrottled(addr); yes {
		return
	}
	now := uint64(time.Now().Unix())
	b := relay.Bucket(now)
	hints := [][]byte{relay.HintKnock(r.Device.ID, b)}
	if b > 0 {
		hints = append(hints, relay.HintKnock(r.Device.ID, b-1))
	}
	var items [][]byte
	if err := r.withRelayControl(addr, func(client *relay.Client) error {
		got, err := client.Fetch(hints)
		items = got
		return err
	}); err != nil {
		return
	}
	for _, item := range items {
		r.admitKnock(item, addr)
	}
}

// admitKnock is the whole gate: the envelope, the person behind it, the
// acquaintance floor, the standing refusal, and the bounds. Every check
// that matters is answered from THIS node's own state.
func (r *Runtime) admitKnock(sealed []byte, addr string) {
	k, err := terminals.OpenKnock(r.Device.ID, r.Device.X25519Priv(), sealed)
	if err != nil {
		return // not for us, or not what it claims to be
	}
	if k.ExpiresAt != 0 && uint64(time.Now().Unix()) >= k.ExpiresAt {
		return // it stopped asking
	}
	// WHO IS ASKING — from a root signature, not from the envelope's word.
	principal, xpub, err := terminals.KnockPrincipal(k)
	if err != nil || principal == r.PrincipalID {
		return
	}

	r.mu.Lock()
	r.knocksInit()
	// ALREADY ANSWERED: a re-delivered envelope is the same question.
	r.knocks.mu.Lock()
	answered := r.knocks.answered[k.ID]
	_, waiting := r.knocks.pending[k.ID]
	r.knocks.mu.Unlock()
	if answered || waiting {
		r.mu.Unlock()
		return
	}
	// THE STANDING REFUSAL answers on the person's behalf, unchanged. The
	// person is never told, and the asker learns nothing new.
	refusal, refused := r.refusalForLocked(principal)
	// THE ACQUAINTANCE FLOOR, from our own membership and our own log.
	acquainted := r.sharesPrivateSpaceLocked(k.Via, principal)
	viaTitle := r.ks.Spaces[k.Via].Title
	name := r.nameInSpaceLocked(k.Via, principal)
	r.mu.Unlock()

	switch {
	case refused:
		r.answerKnockWire(k, xpub, terminals.DecisionDeclined, refusal.Reason, addr)
		r.rememberAnswered(k.ID)
		return
	case !acquainted:
		// NOT A REFUSAL AND NOT SILENCE: nobody decided anything about
		// this person — the door is simply not open to strangers, and
		// saying so is the honest answer (ADR-023).
		r.answerKnockWire(k, xpub, terminals.DecisionUnavailable,
			"we do not share a private space", addr)
		r.rememberAnswered(k.ID)
		return
	}

	r.knocks.mu.Lock()
	defer r.knocks.mu.Unlock()
	if len(r.knocks.pending) >= maxPendingKnocks {
		// The queue is full. Refusing plainly beats a silent drop: the
		// asker is told the door is busy, not left to time out.
		go r.answerKnockWire(k, xpub, terminals.DecisionUnavailable,
			"too many people are waiting to be answered", addr)
		return
	}
	// ONE LIVE KNOCK PER PERSON: a second envelope while one waits is not
	// a second question.
	for _, held := range r.knocks.pending {
		if held.principal == principal {
			return
		}
	}
	r.knocks.pending[k.ID] = &heldKnock{
		knock: k, principal: principal, xpub: xpub,
		receivedAt: time.Now().Unix(),
	}
	_ = name
	_ = viaTitle
}

func (r *Runtime) rememberAnswered(kid [32]byte) {
	r.mu.Lock()
	r.knocksInit()
	r.mu.Unlock()
	r.knocks.mu.Lock()
	r.knocks.answered[kid] = true
	r.knocks.mu.Unlock()
}

// sharesPrivateSpaceLocked is the floor: is this principal a member of the
// named space, and is that space one of ours and private? Caller holds
// r.mu.
func (r *Runtime) sharesPrivateSpaceLocked(via id.TerminalID, who id.PrincipalID) bool {
	st, ok := r.spaces[via]
	if !ok {
		return false
	}
	if meta := r.ks.Spaces[via]; meta.LocalOnly || meta.Visibility == "public" {
		return false
	}
	for _, c := range st.space.MemberCards(0) {
		if c.Principal == who {
			return true
		}
	}
	return false
}

// nameInSpaceLocked resolves what the shared room calls this person. A
// stranger does not get to choose the name they arrive under. Caller holds
// r.mu.
func (r *Runtime) nameInSpaceLocked(via id.TerminalID, who id.PrincipalID) string {
	st, ok := r.spaces[via]
	if !ok {
		return ""
	}
	for _, c := range st.space.MemberCards(0) {
		if c.Principal == who && c.Name != "" {
			return c.Name
		}
	}
	return ""
}

func (r *Runtime) refusalForLocked(who id.PrincipalID) (storage.RefusalRecord, bool) {
	for _, rf := range r.ks.Refusals {
		if rf.Principal == who {
			return rf, true
		}
	}
	return storage.RefusalRecord{}, false
}

// answerKnockWire seals a decision and leaves it at the asker's rendezvous.
func (r *Runtime) answerKnockWire(k *terminals.Knock, xpub [32]byte,
	state terminals.DecisionState, reason string, addr string) {

	// The decision is bound to the DYAD space the pass names, which both
	// sides know, and to the knock id: the same shape the door uses.
	space, err := passSpaceOf(k.Pass)
	if err != nil {
		return
	}
	sealed, err := terminals.BuildDecision(space, xpub, &terminals.Decision{
		RequestID: k.ID, State: state, Reason: reason,
	})
	if err != nil {
		return
	}
	ep := r.routeForDevice(k.From, addr)
	if ep == "" {
		return
	}
	expires := uint64(time.Now().Add(knockTTL).Unix())
	_ = r.withRelayControl(ep, func(client *relay.Client) error {
		_, err := client.Put(terminals.RespHint(k.Reply, k.ID), expires, sealed)
		return err
	})
}

// passSpaceOf reads which space a share link names. The decision's HPKE
// info binds to it, so both sides derive the same envelope from the same
// pass — and a decision for one conversation can never open at another.
func passSpaceOf(link string) (id.TerminalID, error) {
	_, envelope, err := splitShare(link)
	if err != nil {
		return id.TerminalID{}, err
	}
	pass, _, err := terminals.DecodePass(envelope)
	if err != nil {
		return id.TerminalID{}, err
	}
	return pass.Space, nil
}

// ---- answering ----

// Knocks lists what is waiting for this person, oldest first.
func (r *Runtime) Knocks() []PendingKnock {
	r.mu.Lock()
	r.knocksInit()
	ks := r.knocks
	r.mu.Unlock()

	ks.mu.Lock()
	held := make([]*heldKnock, 0, len(ks.pending))
	for _, h := range ks.pending {
		held = append(held, h)
	}
	ks.mu.Unlock()

	r.mu.Lock()
	out := make([]PendingKnock, 0, len(held))
	for _, h := range held {
		out = append(out, PendingKnock{
			ID: hex.EncodeToString(h.knock.ID[:]), Principal: h.principal.Hex(),
			Name: r.nameInSpaceLocked(h.knock.Via, h.principal),
			Via:  h.knock.Via.Hex(), ViaTitle: r.ks.Spaces[h.knock.Via].Title,
			Line: h.knock.Line, ReceivedAt: h.receivedAt,
			ExpiresAt: int64(h.knock.ExpiresAt),
		})
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ReceivedAt < out[j].ReceivedAt })
	return out
}

// KnockAnswer is one of the three things a person may say.
type KnockAnswer string

const (
	// KnockLetIn opens the conversation: this device uses the pass.
	KnockLetIn KnockAnswer = "let_in"
	// KnockNotNow declines this knock and remembers nothing.
	KnockNotNow KnockAnswer = "not_now"
	// KnockNever declines and records a refusal against the PERSON, which
	// answers for them from now on.
	KnockNever KnockAnswer = "do_not_ask"
)

// AnswerKnock gives one of the three answers. `reason` is the person's own
// words and travels verbatim; on "do not ask" it becomes the sentence the
// refusal will keep repeating.
func (r *Runtime) AnswerKnock(knockHex string, answer KnockAnswer, reason string) error {
	raw, err := hex.DecodeString(strings.TrimSpace(knockHex))
	if err != nil || len(raw) != 32 {
		return errors.New("node: bad knock id")
	}
	var kid [32]byte
	copy(kid[:], raw)

	r.mu.Lock()
	r.knocksInit()
	ks := r.knocks
	r.mu.Unlock()

	ks.mu.Lock()
	held, ok := ks.pending[kid]
	if !ok {
		ks.mu.Unlock()
		return errors.New("node: no such knock is waiting")
	}
	delete(ks.pending, kid)
	ks.answered[kid] = true
	ks.mu.Unlock()

	addr := r.PersonalRelayAddress()
	switch answer {
	case KnockLetIn:
		// The ordinary pass flow, unchanged: the knock never carried a
		// key, so this is where access begins.
		if _, err := r.JoinByPass(held.knock.Pass); err != nil {
			return err
		}
		r.answerKnockWire(held.knock, held.xpub, terminals.DecisionReceived, reason, addr)
		return nil
	case KnockNotNow:
		if reason == "" {
			reason = "not this time"
		}
		r.answerKnockWire(held.knock, held.xpub, terminals.DecisionDeclined, reason, addr)
		return nil
	case KnockNever:
		if reason == "" {
			reason = "not this time"
		}
		if err := r.RefusePerson(held.principal, reason); err != nil {
			return err
		}
		r.answerKnockWire(held.knock, held.xpub, terminals.DecisionDeclined, reason, addr)
		return nil
	}
	return errors.New("node: unknown answer")
}

// RefusePerson records a standing refusal. It is principal-scoped, so it
// holds however that person changes device, and it converges to this
// person's own other devices (ADR-024) — declining on the phone silences
// the laptop.
func (r *Runtime) RefusePerson(who id.PrincipalID, reason string) error {
	if who == r.PrincipalID {
		return errors.New("node: that is you")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.ks.Refusals {
		if r.ks.Refusals[i].Principal == who {
			r.ks.Refusals[i].Reason = reason
			r.ks.Refusals[i].At = time.Now().Unix()
			return r.saveKeystore()
		}
	}
	r.ks.Refusals = append(r.ks.Refusals, storage.RefusalRecord{
		Principal: who, Reason: reason, At: time.Now().Unix(),
	})
	if len(r.ks.Refusals) > maxRefusals {
		r.ks.Refusals = r.ks.Refusals[len(r.ks.Refusals)-maxRefusals:]
	}
	return r.saveKeystore()
}

// UnrefusePerson takes a refusal back: a door somebody closed is a door
// they may open, and a refusal nobody can lift would be a punishment
// rather than an answer.
func (r *Runtime) UnrefusePerson(who id.PrincipalID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.ks.Refusals[:0]
	for _, rf := range r.ks.Refusals {
		if rf.Principal != who {
			out = append(out, rf)
		}
	}
	r.ks.Refusals = out
	return r.saveKeystore()
}

// Refusals lists the people this person has declined to hear from.
func (r *Runtime) Refusals() []storage.RefusalRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]storage.RefusalRecord(nil), r.ks.Refusals...)
}

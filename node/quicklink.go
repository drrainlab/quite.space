// Quick links: handing someone a way in without first agreeing on a space.
//
// "Add each other" has no meaning in this protocol — the unit of membership
// is (space, device), and two principals cannot be connected without a shared
// space to be connected inside. So a quick link does not bypass that. It
// collapses it into one gesture: the first link you hand out creates your
// LINE — a private space that is simply yours — and the words let somebody
// walk into it. What it becomes after that is an ordinary space and an
// ordinary conversation.
//
// The words themselves are a pointer, never the pass (protocol/quicklink
// explains why five words cannot carry 32-byte secrets). This file is the
// part that puts the sealed pass somewhere the guest can reach it, and keeps
// an honest local record of what was handed out.
package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/quicklink"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relay"
)

const quickLinkFile = "quicklinks.json"

// QuickLinkTTL is how long a link stays fetchable. Ten minutes is the number
// the whole 55-bit argument rests on: it is long enough to say five words to
// somebody and short enough that grinding the ciphertext offline finishes
// after the pass inside has already expired. Do not raise it without
// revisiting the word count.
const QuickLinkTTL = 10 * time.Minute

// LineTitle is what a personal line is called when it is created. It is an
// ordinary space in every respect — title included, so it can be renamed.
const LineTitle = "my line"

// QuickLinkRecord is the local proof of what was handed out. It exists so
// that "who did I give a link to, and is it still live" has an answer, and so
// a link can be withdrawn. It deliberately does NOT store the words: they are
// a bearer secret, they were shown once, and a node that keeps them is a node
// that can hand out the same access twice without the issuer knowing.
type QuickLinkRecord struct {
	// Hint is the relay address of the sealed payload, hex. It identifies
	// the link without being able to open it.
	Hint string `json:"hint"`
	// Space is where this link leads.
	Space string `json:"space"`
	// PassID ties the record to the underlying pass, which is what actually
	// enforces expiry and use count.
	PassID    string `json:"pass_id"`
	Note      string `json:"note,omitempty"`
	IssuedAt  int64  `json:"issued_at"`
	ExpiresAt int64  `json:"expires_at"`
	Withdrawn bool   `json:"withdrawn,omitempty"`
}

// quickLinkState is the on-disk shape: the line space plus the issuance log.
type quickLinkState struct {
	Line    string            `json:"line_space,omitempty"`
	Records []QuickLinkRecord `json:"records"`
}

var quickLinkMu sync.Mutex

func (r *Runtime) quickLinkPath() string { return filepath.Join(r.dataDir, quickLinkFile) }

func (r *Runtime) loadQuickLinks() quickLinkState {
	var st quickLinkState
	b, err := os.ReadFile(r.quickLinkPath())
	if err != nil {
		return st
	}
	_ = json.Unmarshal(b, &st)
	return st
}

func (r *Runtime) saveQuickLinks(st quickLinkState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	path := r.quickLinkPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// LineSpace returns this node's personal line, creating it the first time.
//
// Creation is lazy on purpose: a node that has never handed out a link should
// not carry a space nobody asked for. It is also idempotent — if the recorded
// space no longer exists (restored from an older backup, say), a new one is
// made rather than failing, because the alternative is a "quick link" button
// that reports an error about a space the person never knew they had.
func (r *Runtime) LineSpace() (id.TerminalID, error) {
	quickLinkMu.Lock()
	defer quickLinkMu.Unlock()

	st := r.loadQuickLinks()
	if st.Line != "" {
		if tid, err := id.ParseTerminalID(st.Line); err == nil {
			r.mu.Lock()
			_, ok := r.spaces[tid]
			r.mu.Unlock()
			if ok {
				return tid, nil
			}
		}
	}
	tid, err := r.CreateSpaceWithCharacter(LineTitle, terminals.DefaultCharacter("campfire"))
	if err != nil {
		return id.TerminalID{}, err
	}
	st.Line = tid.Hex()
	if err := r.saveQuickLinks(st); err != nil {
		return id.TerminalID{}, err
	}
	return tid, nil
}

// QuickLinkInfo is what the issuer is shown, exactly once.
type QuickLinkInfo struct {
	// Link and Phrase are the same 55 bits, written for reading and for
	// saying. They are returned once and never stored.
	Link   string `json:"link"`
	Phrase string `json:"phrase"`
	Hint   string `json:"hint"`
	Space  string `json:"space"`
	Title  string `json:"title"`
	// ExpiresAt is when the RELAY drops the payload — the moment the words
	// stop working, which is sooner than the pass inside expires.
	ExpiresAt int64 `json:"expires_at"`
	// RelayNote says plainly what this required, because a quick link is the
	// one sharing path in this app that is not air-gapped.
	RelayNote string `json:"relay_note"`
}

// MintQuickLink mints a pass into a space and parks it on the relay under a
// five-word token.
//
// If space is the zero value the link leads to this node's line, creating it
// on first use. That is the "add me" case; passing a real space id is the
// "let somebody into this room" case, and the mechanism is identical.
func (r *Runtime) MintQuickLink(space id.TerminalID, note string) (QuickLinkInfo, error) {
	relayAddr := r.GetSettings().Relay
	if relayAddr == "" {
		return QuickLinkInfo{}, errors.New(
			"node: a quick link needs a relay, because five words can only point at a " +
				"pass, never carry one — set a relay in Settings, or share the pass itself " +
				"by QR or by sound, which need no network at all")
	}
	if err := r.relayGate(); err != nil {
		return QuickLinkInfo{}, err
	}

	var err error
	if space == (id.TerminalID{}) {
		if space, err = r.LineSpace(); err != nil {
			return QuickLinkInfo{}, err
		}
	}

	// The pass outlives the words deliberately: the link is the fragile part
	// (ten minutes), the pass is what the guest actually redeems, and a guest
	// who fetched the payload should not lose it because they took a minute
	// to decide.
	pass, err := r.MintPass(space, 1, 1, relayAddr)
	if err != nil {
		return QuickLinkInfo{}, err
	}

	tok, err := quicklink.New()
	if err != nil {
		return QuickLinkInfo{}, err
	}
	title := r.spaceTitle(space)
	sealed, err := quicklink.Seal(tok, quicklink.Payload{
		PassLink: pass.Link,
		From:     r.DisplayName(),
		Space:    title,
	})
	if err != nil {
		return QuickLinkInfo{}, err
	}

	client, err := relay.DialClient(relayAddr)
	if err != nil {
		return QuickLinkInfo{}, fmt.Errorf("node: could not reach the relay to park the link: %w", err)
	}
	defer client.Close()
	expires := time.Now().Add(QuickLinkTTL)
	if _, err := client.Put(tok.Hint(), uint64(expires.Unix()), sealed); err != nil {
		return QuickLinkInfo{}, fmt.Errorf("node: the relay would not hold the link: %w", err)
	}

	quickLinkMu.Lock()
	st := r.loadQuickLinks()
	st.Records = append(st.Records, QuickLinkRecord{
		Hint:      fmt.Sprintf("%x", tok.Hint()),
		Space:     space.Hex(),
		PassID:    pass.PassID,
		Note:      note,
		IssuedAt:  time.Now().Unix(),
		ExpiresAt: expires.Unix(),
	})
	saveErr := r.saveQuickLinks(st)
	quickLinkMu.Unlock()
	if saveErr != nil {
		// The link is live whether or not we managed to write it down, so
		// say so rather than pretending nothing happened.
		return QuickLinkInfo{}, fmt.Errorf(
			"node: the link is live but could not be recorded locally, so it cannot be "+
				"withdrawn from here: %w", saveErr)
	}

	return QuickLinkInfo{
		Link:      tok.String(),
		Phrase:    tok.Phrase(),
		Hint:      fmt.Sprintf("%x", tok.Hint()),
		Space:     space.Hex(),
		Title:     title,
		ExpiresAt: expires.Unix(),
		RelayNote: "The pass waits on " + relayAddr + ", encrypted under these words. " +
			"The relay cannot read it. It is fetched once and then gone.",
	}, nil
}

// QuickLinkPreview is what a guest sees before deciding.
type QuickLinkPreview struct {
	// From and Space are CLAIMS carried inside the link. They are shown as
	// what the link says, never as verified fact — the pass verifies itself
	// against the space's own key at join time, these two strings do not.
	From  string `json:"from"`
	Space string `json:"space"`
	// PassLink is the ordinary Space Pass the words were pointing at. From
	// here the existing join path takes over unchanged.
	PassLink string `json:"pass_link"`
}

// ResolveQuickLink turns five words back into a Space Pass.
//
// Note what this does NOT do: it does not join. Fetching and joining are kept
// apart so a person can read who is inviting them into what before anything
// is signed on their behalf.
func (r *Runtime) ResolveQuickLink(words string) (QuickLinkPreview, error) {
	tok, err := quicklink.Parse(words)
	if err != nil {
		return QuickLinkPreview{}, err
	}
	relayAddr := r.GetSettings().Relay
	if relayAddr == "" {
		return QuickLinkPreview{}, errors.New(
			"node: a quick link is fetched from a relay, and no relay is configured — " +
				"set the same relay the sender used in Settings")
	}
	if err := r.relayGate(); err != nil {
		return QuickLinkPreview{}, err
	}
	client, err := relay.DialClient(relayAddr)
	if err != nil {
		return QuickLinkPreview{}, fmt.Errorf("node: could not reach the relay: %w", err)
	}
	defer client.Close()

	// Collect, not Fetch: it removes what it reads, which is what makes a
	// quick link single-use without the relay knowing that is what it is
	// enforcing.
	items, err := client.Collect([][]byte{tok.Cap()})
	if err != nil {
		return QuickLinkPreview{}, fmt.Errorf("node: the relay could not be asked: %w", err)
	}
	if len(items) == 0 {
		return QuickLinkPreview{}, errors.New(
			"node: nothing waiting under those words. A quick link lasts ten minutes and " +
				"works once — it may have expired, already been used, or been meant for a " +
				"different relay")
	}
	var firstErr error
	for _, it := range items {
		p, err := quicklink.Open(tok, it)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		return QuickLinkPreview{From: p.From, Space: p.Space, PassLink: p.PassLink}, nil
	}
	return QuickLinkPreview{}, firstErr
}

// QuickLinks lists what this node has handed out, newest first.
func (r *Runtime) QuickLinks() []QuickLinkRecord {
	quickLinkMu.Lock()
	defer quickLinkMu.Unlock()
	st := r.loadQuickLinks()
	out := make([]QuickLinkRecord, 0, len(st.Records))
	for i := len(st.Records) - 1; i >= 0; i-- {
		out = append(out, st.Records[i])
	}
	return out
}

// WithdrawQuickLink revokes the pass a link points at.
//
// It cannot un-publish the sealed payload — the relay holds it until the TTL
// runs out, and anyone with the words can still fetch it. What it does is
// make the pass inside worthless, which is the part that grants access. The
// distinction matters enough to be in the record: the link may still resolve
// and then fail to join, and that is the honest outcome, not a bug.
func (r *Runtime) WithdrawQuickLink(hint string) error {
	quickLinkMu.Lock()
	st := r.loadQuickLinks()
	var rec *QuickLinkRecord
	for i := range st.Records {
		if st.Records[i].Hint == hint {
			rec = &st.Records[i]
			break
		}
	}
	if rec == nil {
		quickLinkMu.Unlock()
		return errors.New("node: no such quick link was issued from this device")
	}
	rec.Withdrawn = true
	err := r.saveQuickLinks(st)
	passID, space := rec.PassID, rec.Space
	quickLinkMu.Unlock()
	if err != nil {
		return err
	}
	_ = space // the pass registry is keyed by pass id alone
	return r.RevokePass(passID)
}

// spaceTitle is a best-effort label for display inside a sealed link.
func (r *Runtime) spaceTitle(tid id.TerminalID) string {
	for _, s := range r.Spaces() {
		if s.ID == tid {
			return s.Title
		}
	}
	return ""
}

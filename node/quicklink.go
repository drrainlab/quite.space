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
	"strings"
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
// QuickLinkOptions is what a person decided about the door they are opening.
type QuickLinkOptions struct {
	// Title names the new space. Empty means unnamed — the space will be
	// called after whoever is in it (QL-3).
	Title string
	Note  string
	// MaxUses counts DEVICES, not people: a verified principal does not
	// exist in this protocol, so promising "one person" would promise a
	// thing no signature backs. 0 → 1.
	MaxUses uint64
	// TTLHours defaults to 1 for an open link and 24 when a host must
	// approve — an hour is dishonest when somebody may be asleep.
	TTLHours uint64
	// Approval is "" (walk straight in) or "host" (a person decides).
	Approval string
}

// CreateQuickLink opens a NEW place and a door into it.
//
// A quicklink never picks a space implicitly. It used to reach for this
// node's one "line", so a link made for Alice and a link made for Misha put
// two strangers in the same room — the accident this wave removes. To let
// somebody into a room that already exists, say so: InviteToSpace.
func (r *Runtime) CreateQuickLink(o QuickLinkOptions) (QuickLinkInfo, error) {
	space, err := r.createSpaceForLink(o.Title)
	if err != nil {
		return QuickLinkInfo{}, err
	}
	return r.mintLink(space, o)
}

// InviteToSpace opens a door into a space that already exists — how a
// conversation between two people becomes a group without anyone
// re-creating it somewhere else.
func (r *Runtime) InviteToSpace(space id.TerminalID, o QuickLinkOptions) (QuickLinkInfo, error) {
	if space == (id.TerminalID{}) {
		return QuickLinkInfo{}, errors.New("node: name the space to invite into")
	}
	r.mu.Lock()
	local := r.ks.Spaces[space].LocalOnly
	r.mu.Unlock()
	if local {
		return QuickLinkInfo{}, ErrLocalOnly
	}
	return r.mintLink(space, o)
}

// createSpaceForLink opens the room a new link leads to. An empty title is
// a CHOICE, recorded as such, not a missing value.
func (r *Runtime) createSpaceForLink(title string) (id.TerminalID, error) {
	if strings.TrimSpace(title) == "" {
		return r.CreateSpaceUnnamed()
	}
	return r.CreateSpace(title)
}

// MintQuickLink is the legacy shape, kept so existing callers compile. A
// zero space still means "this node's line" for them; new code should say
// which door it wants.
func (r *Runtime) MintQuickLink(space id.TerminalID, note string) (QuickLinkInfo, error) {
	if space == (id.TerminalID{}) {
		var err error
		if space, err = r.LineSpace(); err != nil {
			return QuickLinkInfo{}, err
		}
	}
	return r.mintLink(space, QuickLinkOptions{Note: note})
}

func (r *Runtime) mintLink(space id.TerminalID, o QuickLinkOptions) (QuickLinkInfo, error) {
	note := o.Note
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

	maxUses := o.MaxUses
	if maxUses == 0 {
		maxUses = 1
	}
	ttl := o.TTLHours
	if ttl == 0 {
		ttl = 1
		if o.Approval == "host" {
			// A host who has to decide may be asleep. An hour would refuse
			// people for the crime of arriving at night.
			ttl = 24
		}
	}
	// A personal link is not a stored mode — it IS one device. One concept,
	// enforced in one place.
	door := quicklink.DoorShared
	if maxUses == 1 {
		door = quicklink.DoorPersonal
	}

	// The pass outlives the words deliberately: the link is the fragile part
	// (ten minutes), the pass is what the guest actually redeems, and a guest
	// who fetched the payload should not lose it because they took a minute
	// to decide.
	pass, err := r.MintPass(space, maxUses, ttl, relayAddr)
	if err != nil {
		return QuickLinkInfo{}, err
	}
	if o.Approval != "" {
		r.setPassApproval(pass.PassID, o.Approval)
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
		// Claims, so the guest can see what they are walking into. The
		// owner's own record is what admits or refuses.
		MaxUses: maxUses, Approval: o.Approval, ExpiresAt: pass.ExpiresAt,
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
	if _, err := client.Put(tok.HintFor(door), uint64(expires.Unix()), sealed); err != nil {
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
	// MaxUses, Approval and ExpiresAt are the link's CLAIMED intent, shown
	// so a person can decide whether to enter. What actually admits them is
	// the owner's own validated pass state — if the two disagree, the owner
	// wins and the guest is told what really happened.
	MaxUses   uint64
	Approval  string
	ExpiresAt uint64
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

	// Which door is this? The kind is inside the sealed payload, so it
	// cannot be read before choosing how to read it — resolve it in the
	// ADDRESS instead. Try the SHARED mailbox first with a non-destructive
	// Fetch: a wrong guess costs nothing, whereas guessing the other way
	// round would spend a personal link nobody managed to read.
	items, err := client.Fetch([][]byte{tok.HintFor(quicklink.DoorShared)})
	if err != nil {
		return QuickLinkPreview{}, fmt.Errorf("node: the relay could not be asked: %w", err)
	}
	// Fall back ONLY on a definitive empty answer. A timeout, an offline
	// relay or a protocol error must not push resolution into the
	// destructive branch — that would burn a personal link on a network
	// hiccup.
	if len(items) == 0 {
		items, err = client.Collect([][]byte{tok.CapFor(quicklink.DoorPersonal)})
		if err != nil {
			return QuickLinkPreview{}, fmt.Errorf("node: the relay could not be asked: %w", err)
		}
	}
	if len(items) == 0 {
		return QuickLinkPreview{}, errors.New(
			"node: nothing waiting under those words. A personal link lasts ten minutes " +
				"and is spent by the first person who opens it; a shared one lasts until " +
				"it expires — these may have run out, been used, or been meant for a " +
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
		prev := QuickLinkPreview{From: p.From, Space: p.Space, PassLink: p.PassLink,
			MaxUses: p.MaxUses, Approval: p.Approval, ExpiresAt: p.ExpiresAt}
		// Remember the entrance. The words may be spent, but backing out of
		// a preview should not cost somebody their way in.
		r.rememberResolved(tok.Phrase(), prev)
		return prev, nil
	}
	return QuickLinkPreview{}, firstErr
}

// ResolvedQuickLink returns an entrance this device already opened. The
// relay copy of a personal link is gone after the first read; this is how
// pressing Back and returning does not lose it.
func (r *Runtime) ResolvedQuickLink(words string) (QuickLinkPreview, error) {
	tok, err := quicklink.Parse(words)
	if err != nil {
		return QuickLinkPreview{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	prev, ok := r.resolvedLinks[tok.Phrase()]
	if !ok {
		return QuickLinkPreview{}, errors.New(
			"node: this device has not opened those words")
	}
	return prev, nil
}

// rememberResolved caches a resolved entrance in memory only. It is not
// persisted: the pass inside it is short-lived, and a cache that outlived
// the process would keep a bearer secret around for no benefit.
func (r *Runtime) rememberResolved(phrase string, prev QuickLinkPreview) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resolvedLinks == nil {
		r.resolvedLinks = map[string]QuickLinkPreview{}
	}
	r.resolvedLinks[phrase] = prev
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
	// Whoever is waiting at this door is told the door is gone, rather
	// than left to time out against a link that no longer exists.
	r.declinePendingForPass(passID, "the link was withdrawn")
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

// The mirror role (PH-3).
//
// A public space lives at the relay for 48 hours at a time. The owner's
// heartbeat refreshes it; when the owner goes offline for longer than that,
// the projection expires and the space simply stops existing for anyone who
// did not already have it. One device — the owner's — was the only thing
// standing between a public space and disappearance.
//
// A mirror is any node that volunteers to keep a public space reachable. It
// republishes the owner's envelope VERBATIM, greedily fetches the media that
// envelope references, and answers other readers' wants from what it holds.
//
// What a mirror can never do is the important half:
//
//   - It has no space key, so BuildPublicProjection refuses it structurally
//     and it cannot alter a byte of what it serves.
//   - It republishes with Put, never Replace, so a stale mirror cannot
//     overwrite a fresher envelope from the owner.
//   - It writes the OWNER's PublisherDevice into its state, never its own.
//   - It FETCHES ingress (never Collects), so it cannot take a contribution
//     out of the owner's mailbox.
//
// A mirror adds availability and nothing else. That is the entire promise,
// and every line below is written to keep it narrow.
package node

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/projection"
	"github.com/drrainlab/quiet_places/transports/bundle"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// projectionDir holds the last verified envelope per space, VERBATIM. It is
// a file rather than a keystore field for two reasons: an envelope runs to
// hundreds of kilobytes and the keystore is rewritten whole on every save,
// and these bytes are already public — they are what the relay hands to
// strangers. Nothing secret lands here.
const projectionDir = "projections"

func (r *Runtime) projectionPath(tid id.TerminalID) string {
	return filepath.Join(r.dataDir, projectionDir, tid.Hex()+".qpp")
}

// saveProjection persists the envelope a mirror will republish. Best effort:
// a mirror that loses this file re-fetches on its next cycle, and if the
// relay has meanwhile forgotten the space, the mirror is honestly unable to
// help — which is better than serving something it cannot vouch for.
func (r *Runtime) saveProjection(tid id.TerminalID, wire []byte) {
	if len(wire) == 0 {
		return
	}
	dir := filepath.Join(r.dataDir, projectionDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := r.projectionPath(tid)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, wire, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// loadProjection reads a persisted envelope back and RE-VERIFIES it. The
// file sits in a plain directory, so it is treated exactly like bytes off
// the wire: the space signature is the only reason to believe any of it.
func (r *Runtime) loadProjection(tid id.TerminalID) ([]byte, *projection.Envelope, error) {
	wire, err := os.ReadFile(r.projectionPath(tid))
	if err != nil {
		return nil, nil, err
	}
	env, err := projection.Decode(wire)
	if err != nil {
		return nil, nil, err
	}
	if env.SpaceID != tid {
		return nil, nil, errors.New("node: stored projection belongs to another space")
	}
	if err := projection.Verify(env); err != nil {
		return nil, nil, err
	}
	return wire, env, nil
}

// mirrorKeepalive keeps one space reachable: republish if the relay has
// nothing at least as fresh, then greedily take custody of the media the
// projection references so this node can answer for it.
func (r *Runtime) mirrorKeepalive(addr string, tid id.TerminalID) error {
	r.mu.Lock()
	st, ok := r.spaces[tid]
	if !ok {
		r.mu.Unlock()
		return errors.New("node: unknown space")
	}
	wire, seq := st.projWire, st.projSeq
	r.mu.Unlock()

	if len(wire) == 0 {
		// Nothing in memory (a fresh start): fall back to what we persisted.
		w, env, err := r.loadProjection(tid)
		if err != nil {
			return nil // nothing to mirror yet; the next fetch will supply it
		}
		wire, seq = w, env.Seq
		r.mu.Lock()
		if st, ok := r.spaces[tid]; ok {
			st.projWire, st.projSeq, st.ingressHints = w, env.Seq, env.IngressHints
		}
		r.mu.Unlock()
	}

	// Take custody of what the projection references. This runs OUTSIDE
	// r.mu deliberately — RequestAsset locks — and it is what separates a
	// mirror from an ordinary reader: readers fetch media when a person
	// opens it, a mirror fetches it so that someone else can.
	r.saveProjection(tid, wire)
	if env, err := projection.Decode(wire); err == nil {
		r.requestIncompleteAssets(tid, env.Frames)
	}

	if err := r.relayGate(); err != nil {
		return err
	}
	// Bulk lane (RR-2): projection fetches and republish Puts are large.
	return r.withRelayBulk(addr, func(client *relay.Client) error {
		// Is anything at least as fresh already there? Fetch is
		// non-destructive, so asking costs the relay one read and nobody a
		// mailbox.
		now := uint64(time.Now().Unix())
		b := relay.Bucket(now)
		hints := [][]byte{relay.HintPublicOutbox(tid, b)}
		items, err := client.Fetch(hints)
		if err != nil {
			return err
		}
		freshest := uint64(0)
		for _, item := range items {
			env, err := projection.Decode(item)
			if err != nil || env.SpaceID != tid || projection.Verify(env) != nil {
				continue
			}
			if env.Seq > freshest {
				freshest = env.Seq
			}
		}
		if freshest >= seq {
			return nil // the owner (or another mirror) is keeping it alive
		}

		// Put, never Replace. Replace would let a mirror holding an older
		// envelope wipe a newer one; Put is content-idempotent, so repeated
		// keepalives from many mirrors settle into one item.
		expires := now + uint64(DefaultRelayTTL/time.Second)
		_, err = client.Put(relay.HintPublicOutbox(tid, b), expires, wire)
		return err
	})
}

// followEvery paces the card walk. A catalogue's list changes when somebody
// edits it, which is nothing like the two-second cadence of the loop this
// runs inside — and each new card costs a network open.
const followEvery = 5 * time.Minute

// dueToFollow reports whether this directory's cards are worth walking again,
// and records the attempt. First call for a space always says yes: a mirror
// that has just started should not wait five minutes to discover what it is
// mirroring.
func (r *Runtime) dueToFollow(tid id.TerminalID) bool {
	rs := r.relaySync
	if rs == nil {
		return false
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.lastFollow == nil {
		rs.lastFollow = map[id.TerminalID]time.Time{}
	}
	if last, ok := rs.lastFollow[tid]; ok && time.Since(last) < followEvery {
		return false
	}
	rs.lastFollow[tid] = time.Now()
	return true
}

// mirrorFollowCards makes a mirrored DIRECTORY mean what it says.
//
// A directory is a space whose content is cards pointing at other spaces.
// Mirroring one used to keep only the LIST reachable: the cards appeared, the
// places they named did not, and every picture in them waited on the
// catalogue owner's own machine being awake. Measured on the demo catalog —
// the mirror's data directory held 44 KiB and no blobs at all, while the
// screen it fed showed posts full of media nobody could load. A catalogue
// whose entries are unreachable is not a catalogue that has been kept
// reachable.
//
// ONLY SPACES THIS NODE DOES NOT ALREADY KNOW ARE TOUCHED, and that single
// rule is what keeps the operator in charge. `mirror remove` clears the flags
// and leaves the space; a space that is present is therefore never
// re-decided here, so a deliberate removal stays removed without this needing
// any memory of its own.
//
// Seeding is INHERITED rather than assumed: a mirror that answers requests
// for the catalogue answers for what the catalogue lists, and one that
// quietly holds without seeding goes on quietly holding.
func (r *Runtime) mirrorFollowCards(tid id.TerminalID) {
	// The links are read first and the lock let go: everything below dials.
	var links []string
	var seed bool
	if err := r.withSpace(tid, func(st *spaceState) error {
		seed = r.ks.Spaces[tid].Seed
		for _, p := range st.space.State.Publications() {
			if p.Document == nil || p.Document.Kind != "space" {
				continue
			}
			if l := spaceCardTarget(p.Document); l != "" {
				links = append(links, l)
			}
		}
		return nil
	}); err != nil {
		return
	}

	for _, link := range links {
		_, target, _, err := ParsePublicLink(strings.TrimPrefix(strings.TrimSpace(link), "qs:"))
		if err != nil {
			continue // a card this build cannot read is not a card to act on
		}
		r.mu.Lock()
		_, known := r.spaces[target]
		r.mu.Unlock()
		if known {
			continue
		}
		if _, err := r.OpenPublicLink(strings.TrimPrefix(strings.TrimSpace(link), "qs:")); err != nil {
			continue // unreachable right now; the next pass tries again
		}
		if err := r.SetMirror(target, true); err != nil {
			continue
		}
		if seed {
			_ = r.SetSeed(target, true)
		}
	}
}

// mirrorAnswerWants serves media out of what this node already holds. It
// FETCHES the ingress shards rather than collecting them: draining would
// steal contributions the owner has not seen yet. Frames in those bundles
// are ignored entirely — a mirror carries bytes, it does not absorb events
// on anyone's behalf.
func (r *Runtime) mirrorAnswerWants(client *relay.Client, tid id.TerminalID) {
	r.mu.Lock()
	st, ok := r.spaces[tid]
	if !ok {
		r.mu.Unlock()
		return
	}
	hints := append([][]byte(nil), st.ingressHints...)
	r.mu.Unlock()
	if len(hints) == 0 {
		return
	}
	items, err := client.Fetch(hints)
	if err != nil {
		return
	}
	acc := newWantAccumulator() // one deduped answer per reply box (PM-0)
	for _, item := range items {
		parts, err := bundle.DecodeParts(item)
		if err != nil || parts.Terminal != tid || len(parts.Wants) == 0 {
			continue
		}
		acc.add(parts.Wanter, parts.Wants, parts.ReplyBox)
	}
	acc.answer(r, client, tid, true)
}

// seedForSpace answers other readers' media wants out of what this node
// already holds. It is the whole of the opt-in Asset Swarm: no fetching, no
// custody promises, no new state — just "if I have these bytes and someone
// is asking, they can have them".
func (r *Runtime) seedForSpace(addr string, tid id.TerminalID) error {
	if err := r.relayGate(); err != nil {
		return err
	}
	// Bulk lane (RR-2): answers carry media blobs.
	return r.withRelayBulk(addr, func(client *relay.Client) error {
		r.mirrorAnswerWants(client, tid)
		return nil
	})
}

// SetMirror turns this node into a mirror for a public space (or turns it
// off). Refused for spaces this node owns — an owner is already the origin,
// and calling that "mirroring" would blur who is responsible for the space.
func (r *Runtime) SetMirror(tid id.TerminalID, on bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return errors.New("node: unknown space")
	}
	if !st.space.Policy().IsPublic() {
		return errors.New("node: only public spaces can be mirrored")
	}
	meta := r.ks.Spaces[tid]
	if meta.Owned {
		return errors.New("node: this space is yours — you are its origin, not a mirror")
	}
	meta.Mirror = on
	r.ks.Spaces[tid] = meta
	return r.saveKeystore()
}

// SetSeed turns opt-in peer seeding on or off for a public space. Off by
// default: answering with media you already downloaded tells others you
// have it, and therefore that you read this space. That is a consent
// question, so it is never enabled on someone's behalf.
func (r *Runtime) SetSeed(tid id.TerminalID, on bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return errors.New("node: unknown space")
	}
	if !st.space.Policy().IsPublic() {
		return errors.New("node: only public spaces can be seeded")
	}
	meta := r.ks.Spaces[tid]
	meta.Seed = on
	r.ks.Spaces[tid] = meta
	return r.saveKeystore()
}

// handleSetMirror toggles this node's availability roles for a space.
// Both default off and neither grants any say over the space's content.
func (a *APIServer) handleSetMirror(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Mirror *bool `json:"mirror"`
		Seed   *bool `json:"seed"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if body.Mirror != nil {
		if err := a.rt.SetMirror(tid, *body.Mirror); err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
	}
	if body.Seed != nil {
		if err := a.rt.SetSeed(tid, *body.Seed); err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
	}
	a.rt.mu.Lock()
	meta := a.rt.ks.Spaces[tid]
	a.rt.mu.Unlock()
	writeJSON(w, map[string]bool{"mirror": meta.Mirror, "seed": meta.Seed})
}

// MirrorStatus is what a mirror can honestly say about a space: not what it
// intends to hold, but what it actually holds.
type MirrorStatus struct {
	SpaceID     id.TerminalID
	Title       string
	Seed        bool
	AssetsHeld  int
	AssetsTotal int
}

// MirroredSpaces reports every space this node keeps reachable, with real
// custody counts rather than a promise. An operator deserves to see the
// difference between "I volunteered" and "I have the bytes".
func (r *Runtime) MirroredSpaces() []MirrorStatus {
	r.mu.Lock()
	type row struct {
		tid   id.TerminalID
		title string
		seed  bool
		refs  []string
	}
	var rows []row
	for tid, meta := range r.ks.Spaces {
		if !meta.Mirror {
			continue
		}
		rows = append(rows, row{tid, meta.Title, meta.Seed, r.assetRefsLocked(tid)})
	}
	r.mu.Unlock()

	out := make([]MirrorStatus, 0, len(rows))
	for _, rw := range rows {
		st := MirrorStatus{SpaceID: rw.tid, Title: rw.title, Seed: rw.seed,
			AssetsTotal: len(rw.refs)}
		for _, aid := range rw.refs {
			if s, err := r.AssetStatus(rw.tid, aid); err == nil &&
				s.State == assets.StateComplete {
				st.AssetsHeld++
			}
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		return string(out[i].SpaceID[:]) < string(out[j].SpaceID[:])
	})
	return out
}

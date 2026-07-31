// The transient post preview (PS-3).
//
// A card invites a look, and looking must not decide anything: opening a
// shared post makes the reader a member of nothing, persists no replica,
// touches no Navigator and changes no relay. Those invariants are not a
// checklist here — they hold BY CONSTRUCTION, because the preview is a
// throwaway reducers.State with no Space, no path on disk and no keystore
// handle anywhere in the call. Closing discards the session from the UI
// and changes no durable state; verified projection bytes may remain in
// the bounded in-memory cache below until TTL.
//
// The preview is deliberately not a LAXER reader than a real replica: the
// same envelope verification, the same per-frame verification and the same
// Authorized defense in depth, all shared with the install path in
// terminals (MaterializePublicProjection).
package node

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"

	"github.com/drrainlab/quiet_places/kernel/reducers"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// previewTTL bounds how long a session lives; previewCap bounds how many.
// Small on purpose — this is a reading room, not a cache layer.
const (
	previewTTL = 10 * time.Minute
	previewCap = 8
)

// Preview states, an explicit enum — distinguished, never merged, and none
// of them is an error ("the publisher is away" is routine, 72fb396).
const (
	PreviewExistingLocal = "existing_local"       // the space is already held: plain navigation
	PreviewWaiting       = "preview_waiting"      // nothing has arrived from this address yet
	PreviewResolved      = "preview_resolved"     // readable, and nothing was saved
	PreviewMissingDoc    = "publication_missing"  // the space arrived; this post is not in it
	PreviewArchived      = "publication_archived" // the author withdrew it
)

// previewSession is one verified, in-memory reading of a public space.
type previewSession struct {
	id    string
	space id.TerminalID
	state *reducers.State
	title string
	// publish and join are the verified policy's participate axes, so the
	// continuation can offer the right words: Follow for a broadcast
	// space, Follow read-only / Join and participate for a community.
	publish string
	join    string
	assets  map[string]*schemas.AssetRef
	born    time.Time
	// fetcher drives the swarm for this session (PM-2). Every death path
	// of the session closes it — that is what makes "nothing survives"
	// true for in-flight downloads too.
	fetcher *sessionFetcher
}

// previewStore is the bounded session table. Its own mutex, not r.mu: a
// preview never touches runtime state, and taking the runtime lock here
// would couple what is deliberately uncoupled.
type previewStore struct {
	mu       sync.Mutex
	sessions map[string]*previewSession
}

func (ps *previewStore) get(pid string) *previewSession {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	s := ps.sessions[pid]
	if s == nil || time.Since(s.born) > previewTTL {
		delete(ps.sessions, pid)
		return nil
	}
	return s
}

// put stores a session, evicting expired ones and, past the cap, the
// oldest — a bounded LRU by birth, which is enough for a reading room.
func (ps *previewStore) put(s *previewSession) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.sessions == nil {
		ps.sessions = map[string]*previewSession{}
	}
	for k, v := range ps.sessions {
		if time.Since(v.born) > previewTTL {
			delete(ps.sessions, k)
		}
	}
	for len(ps.sessions) >= previewCap {
		oldest, at := "", time.Now()
		for k, v := range ps.sessions {
			if v.born.Before(at) {
				oldest, at = k, v.born
			}
		}
		delete(ps.sessions, oldest)
	}
	ps.sessions[s.id] = s
}

// closeAll kills every session's fetcher — the runtime-shutdown death
// path. The loops also watch r.stop; this releases their budgets.
func (ps *previewStore) closeAll() {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for k, v := range ps.sessions {
		if v.fetcher != nil {
			v.fetcher.close()
		}
		delete(ps.sessions, k)
	}
}

// bySpace finds a fresh session for a space, so re-opening within the TTL
// does not refetch.
func (ps *previewStore) bySpace(tid id.TerminalID) *previewSession {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, v := range ps.sessions {
		if v.space == tid && time.Since(v.born) <= previewTTL {
			return v
		}
	}
	return nil
}

// PostPreview is what PreviewPublicPublication answers with.
type PostPreview struct {
	PreviewID  string
	Space      id.TerminalID
	Document   [16]byte
	State      string
	Reason     string
	SpaceTitle string
	Publish    string
	Join       string
	Pub        *reducers.Publication
}

// PreviewPublicPublication opens a transient read-only view of one post
// behind a reference. The reference's relay is dialed and NEVER adopted:
// this address arrived inside a message anyone could send.
func (r *Runtime) PreviewPublicPublication(reference string) (*PostPreview, error) {
	relayAddr, tid, doc, err := ParsePublicLink(strings.TrimPrefix(strings.TrimSpace(reference), "qs:"))
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, errors.New("node: this reference names no post")
	}

	// The space is already here — no preview at all, plain navigation.
	r.mu.Lock()
	_, held := r.spaces[tid]
	r.mu.Unlock()
	if held {
		return &PostPreview{Space: tid, Document: *doc, State: PreviewExistingLocal}, nil
	}

	// A cached session that HOLDS the post serves it; one that does not is
	// refetched rather than trusted for its TTL — a Retry on "not in it
	// yet" must actually retry, and the post may have been published a
	// heartbeat after our cached read. The cache still shields the common
	// case: re-opening a post the session already resolves.
	sess := r.previews.bySpace(tid)
	if sess != nil {
		if _, ok := sess.state.PublicationByID(*doc); !ok {
			sess = nil
		}
	}
	if sess == nil {
		if err := r.relayGate(); err != nil {
			return nil, err
		}
		client, err := relay.DialClient(relayAddr)
		if err != nil {
			return &PostPreview{Space: tid, Document: *doc, State: PreviewWaiting,
				Reason: "nothing has arrived from this address yet"}, nil
		}
		env, _, err := bestProjectionFor(client, tid)
		client.Close()
		if err != nil {
			if errors.Is(err, ErrNoProjection) {
				return &PostPreview{Space: tid, Document: *doc, State: PreviewWaiting,
					Reason: "nothing has arrived from this address yet"}, nil
			}
			return nil, err
		}
		state, m, err := terminals.MaterializePublicProjection(tid, env)
		if err != nil {
			return nil, err
		}
		pid := make([]byte, 16)
		if _, err := rand.Read(pid); err != nil {
			return nil, err
		}
		title := ""
		if len(m.DeclaredLabels) > 0 {
			title = m.DeclaredLabels[0]
		}
		pol := terminals.ParsePolicy(m.DeclaredLabels)
		sess = &previewSession{
			id: hex.EncodeToString(pid), space: tid, state: state,
			title: title, publish: string(pol.Publish), join: string(pol.Join),
			assets: previewAssetRefs(tid, env.Frames), born: time.Now(),
		}
		// The fetcher (PM-2): the envelope's IngressHints and the link's
		// relay are exactly what the session used to discard. Best-effort —
		// a session without a fetcher still reads text and serves what the
		// node already holds.
		if f, err := newSessionFetcher(tid, relayAddr, env.IngressHints, r.root); err == nil {
			sess.fetcher = f
			go f.run(r.stop)
		}
		r.previews.put(sess)
	}

	out := &PostPreview{PreviewID: sess.id, Space: tid, Document: *doc,
		SpaceTitle: sess.title, Publish: sess.publish, Join: sess.join}
	pub, ok := sess.state.PublicationByID(*doc)
	switch {
	case !ok:
		// Normal, not an error: published after the last push, or the
		// owner is offline. Not "nothing arrived" and not "no such post".
		out.State = PreviewMissingDoc
		out.Reason = "the space arrived, but this post is not in it yet"
	case pub.Archived:
		out.State = PreviewArchived
		out.Reason = "this post was withdrawn by its author"
		out.Pub = &pub
	default:
		out.State = PreviewResolved
		out.Pub = &pub
	}
	// Only what the person actually OPENED becomes requestable (PM-1): the
	// allowlist grows by this publication's graph, never by "everything the
	// projection mentions". An archived post's graph is deliberately NOT
	// added — its withdrawn media is not offered for fetch.
	if sess.fetcher != nil && out.State == PreviewResolved && pub.Document != nil {
		sess.fetcher.extendGraph(pub.Document.LiveAssetIDs(), sess.assets)
	}
	return out, nil
}

// previewAssetRefs collects the asset refs a projection's block frames
// carry, so the session can serve what the node happens to hold. The
// frames were already signature-verified by Materialize; a ref is only a
// lookup into the content-addressed store, which re-verifies on retrieve.
func previewAssetRefs(tid id.TerminalID, frames [][]byte) map[string]*schemas.AssetRef {
	out := map[string]*schemas.AssetRef{}
	for _, frame := range frames {
		fe, err := signal.Decode(frame)
		if err != nil || fe.Terminal != tid || !schemas.IsBlockSchema(fe.Schema) {
			continue
		}
		for _, ref := range schemas.ExtractAssetRefs(fe.Schema, fe.Payload) {
			if ref != nil {
				out[ref.PublicIDHex()] = ref
			}
		}
	}
	return out
}

// FollowPublicSpace is the explicit continuation after reading (PS-4c):
// persist a reader replica of the space behind a reference. The link's
// relay becomes this SPACE's source address (recorded by the projection
// fetch) and never the global setting — a Follow from a message card must
// not reconfigure the node.
func (r *Runtime) FollowPublicSpace(reference string) (id.TerminalID, error) {
	relayAddr, tid, _, err := ParsePublicLink(strings.TrimPrefix(strings.TrimSpace(reference), "qs:"))
	if err != nil {
		return id.TerminalID{}, err
	}
	if err := r.OpenPublicSpace(tid, relayAddr); err != nil {
		return id.TerminalID{}, err
	}
	return tid, nil
}

// ---- HTTP ----

func (a *APIServer) handlePublicPreview(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Reference string `json:"reference"`
	}](r)
	if err != nil || strings.TrimSpace(body.Reference) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("reference required"))
		return
	}
	pv, err := a.rt.PreviewPublicPublication(body.Reference)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	out := map[string]any{
		"preview_id":  pv.PreviewID,
		"space_id":    pv.Space.Hex(),
		"document_id": hex.EncodeToString(pv.Document[:]),
		"state":       pv.State,
		"space_title": pv.SpaceTitle,
		"publish":     pv.Publish,
		"join":        pv.Join,
	}
	if pv.Reason != "" {
		out["reason"] = pv.Reason
	}
	if pv.Pub != nil {
		out["publication"] = map[string]any{
			"document":   documentToJSON(pv.Pub.Document),
			"author":     pv.Pub.Author.Hex(),
			"created_at": pv.Pub.CreatedAt,
			"archived":   pv.Pub.Archived,
		}
	}
	writeJSON(w, out)
}

// handlePreviewAsset serves an asset OUT OF THE SESSION: the ref came from
// the verified projection, the bytes come from the content-addressed store
// (integrity re-verified on retrieve), and a blob the node does not hold
// answers with the honest state rather than a broken image. No transient
// want/reply fetch in this gate — for an old post the ref itself may be
// unreachable by construction (the pruned-carrier finding), and inventing
// a spinner over that would be a lie with a progress bar.
func (a *APIServer) handlePublicFollow(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Reference string `json:"reference"`
	}](r)
	if err != nil || strings.TrimSpace(body.Reference) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("reference required"))
		return
	}
	tid, err := a.rt.FollowPublicSpace(body.Reference)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{"id": tid.Hex()})
}

func (a *APIServer) handlePreviewAsset(w http.ResponseWriter, r *http.Request) {
	sess := a.rt.previews.get(r.PathValue("pid"))
	if sess == nil {
		httpErr(w, http.StatusNotFound, errors.New("node: this preview is gone — open the post again"))
		return
	}
	aid, err := a.assetID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	// GET is PASSIVE: it serves what the session has and never triggers
	// the swarm — "fetch on GET miss" is the likeliest future regression,
	// and the test suite pins this. Preview media never enters a durable
	// browser layer either: no-store on every answer.
	w.Header().Set("Cache-Control", "no-store")
	if sess.fetcher != nil {
		if data, ref, ok := sess.fetcher.bytesFor(aid); ok {
			serveAssetBytes(w, r, ref, aid, data)
			return
		}
	}
	// Read-through: bytes the node already holds (mirror, prior follow)
	// serve with zero network and zero session budget.
	if ref, ok := sess.assets[aid]; ok {
		var st assets.Store = a.rt.root
		if sess.fetcher != nil {
			st = sess.fetcher.store
		}
		if data, err := assets.RetrieveBytes(st, ref); err == nil {
			serveAssetBytes(w, r, ref, aid, data)
			return
		}
	}
	state, missing, total, reason := FetchNotRequested, 0, 0, ""
	if sess.fetcher != nil {
		state, missing, total, reason = sess.fetcher.status(aid)
		if state == "" {
			httpErr(w, http.StatusNotFound, errors.New("node: this asset is outside the publication's graph"))
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"state": state, "missing": missing, "total": total, "reason": reason,
	})
}

// handlePreviewFetch starts (or reports) one asset's session fetch. The
// POST is the consent; identical concurrent requests coalesce into one
// job, and a retry after silence re-arms it.
func (a *APIServer) handlePreviewFetch(w http.ResponseWriter, r *http.Request) {
	sess := a.rt.previews.get(r.PathValue("pid"))
	if sess == nil {
		httpErr(w, http.StatusNotFound, errors.New("node: this preview is gone — open the post again"))
		return
	}
	aid, err := a.assetID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if sess.fetcher == nil {
		httpErr(w, http.StatusConflict, errors.New("node: this preview has no fetch pipeline"))
		return
	}
	state, missing, total, reason := sess.fetcher.request(aid)
	if state == "" {
		httpErr(w, http.StatusNotFound, errors.New("node: this asset is outside the publication's graph"))
		return
	}
	writeJSON(w, map[string]any{
		"state": state, "missing": missing, "total": total, "reason": reason,
	})
}

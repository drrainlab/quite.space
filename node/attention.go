// QuietRank on the node: discovery, ranking, and device-local persistence.
//
// Discovery deliberately does NOT ride a clock cursor. In a mesh or
// store-and-forward world an event can arrive today carrying a logical clock
// from last month; a cursor would place it "behind" us and it would never be
// judged at all. The seen-set is the source of truth for "already looked at
// this", and a periodic full reconciliation guarantees every event reaches
// the layer no matter how late or out of order it landed.
package node

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/attention"
	"github.com/drrainlab/quiet_places/kernel/reducers"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
)

// errNotFound is returned when a signal or its source event is gone.
var errNotFound = errors.New("node: not found")

// Discovery cadence. The fast path skims the tail of each space every sync
// tick; reconciliation walks everything, rarely.
const (
	attentionTailWindow   = 64
	attentionReconcile    = 3 * time.Minute
	attentionSaveDebounce = 5 * time.Second
)

type attentionState struct {
	mu       sync.Mutex
	engine   *attention.Engine
	loaded   bool
	dirty    bool
	lastSave time.Time
	lastFull time.Time
	// viewing is the space the person currently has open, if any: we do not
	// signal about a room they are already reading.
	viewing id.TerminalID
}

// attn lazily loads the attention state from sealed storage.
func (r *Runtime) attn() *attentionState {
	r.mu.Lock()
	if r.attention == nil {
		r.attention = &attentionState{}
	}
	st := r.attention
	root := r.root
	pol := r.attentionPolicyLocked()
	r.mu.Unlock()

	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.loaded {
		eng := attention.NewEngine(pol)
		if in, err := attention.LoadInbox(root); err == nil {
			eng.Inbox = in
		}
		if m, err := attention.LoadModel(root); err == nil {
			eng.Model = m
		}
		st.engine = eng
		st.loaded = true
	} else {
		st.engine.Policy = pol
	}
	return st
}

// attentionPolicyLocked reads the policy out of the device-local settings
// blob. Caller holds r.mu.
func (r *Runtime) attentionPolicyLocked() attention.Policy {
	var s Settings
	if len(r.ks.Settings) > 0 {
		_ = json.Unmarshal(r.ks.Settings, &s)
	}
	if s.Attention == nil {
		return attention.DefaultPolicy()
	}
	p := *s.Attention
	if p.Mode == "" {
		p.Mode = attention.ModeMinimal
	}
	if p.Spaces == nil {
		p.Spaces = map[string]attention.Scope{}
	}
	return p
}

// AttentionPolicy returns the current policy.
func (r *Runtime) AttentionPolicy() attention.Policy {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attentionPolicyLocked()
}

// SetAttentionPolicy stores the policy in the encrypted settings blob. It
// never becomes an event: what you pay attention to stays on this device.
func (r *Runtime) SetAttentionPolicy(p attention.Policy) error {
	s := r.GetSettings()
	s.Attention = &p
	return r.SetSettings(s)
}

// SetViewing records which space is open, so QuietRank stays quiet about it.
func (r *Runtime) SetViewing(tid id.TerminalID) {
	st := r.attn()
	st.mu.Lock()
	st.viewing = tid
	st.mu.Unlock()
}

// ScanAttention runs one discovery pass over every space. It is called from
// the background sync loop and before serving the inbox.
func (r *Runtime) ScanAttention() {
	st := r.attn()
	st.mu.Lock()
	if st.engine.Policy.Mode == attention.ModeOff {
		st.mu.Unlock()
		return
	}
	full := time.Since(st.lastFull) > attentionReconcile
	if full {
		st.lastFull = time.Now()
	}
	viewing := st.viewing
	st.mu.Unlock()

	now := time.Now().Unix()
	for _, sp := range r.attentionSpaces() {
		r.scanSpace(st, sp, full, viewing, now)
	}
	r.persistAttention(st, false)
}

// attentionSpaceInfo is the per-space context the scan needs, resolved under
// the runtime lock once.
type attentionSpaceInfo struct {
	tid    id.TerminalID
	space  *terminals.Space
	title  string
	names  map[id.PrincipalID]string
	kinds  map[id.PrincipalID]string
	scope  attention.Scope
	myself id.PrincipalID
}

func (r *Runtime) attentionSpaces() []attentionSpaceInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	pol := r.attentionPolicyLocked()
	out := make([]attentionSpaceInfo, 0, len(r.spaces))
	for tid, st := range r.spaces {
		scope := pol.ScopeFor(tid.Hex())
		if scope == attention.ScopeOff {
			continue // a muted space is not even scanned
		}
		meta := r.ks.Spaces[tid]
		names := map[id.PrincipalID]string{}
		kinds := map[id.PrincipalID]string{}
		for _, c := range st.space.MemberCards(0) {
			if c.Name != "" {
				names[c.Principal] = c.Name
			}
			if c.Kind != "" {
				kinds[c.Principal] = c.Kind
			}
		}
		out = append(out, attentionSpaceInfo{
			tid: tid, space: st.space, title: meta.Title,
			names: names, kinds: kinds, scope: scope, myself: r.PrincipalID,
		})
	}
	return out
}

// scanSpace offers new entries to the engine. full=false skims the tail;
// full=true reconciles the whole projection so nothing that arrived late can
// hide behind a cursor.
func (r *Runtime) scanSpace(st *attentionState, info attentionSpaceInfo, full bool, viewing id.TerminalID, now int64) {
	entries := info.space.State.Entries()
	if !full && len(entries) > attentionTailWindow {
		entries = entries[len(entries)-attentionTailWindow:]
	}
	aliases := st.engine.Policy.Aliases

	st.mu.Lock()
	defer st.mu.Unlock()
	viewer := attention.Viewer{
		Principal: info.myself,
		Aliases:   aliases,
		AuthoredByMe: func(e id.EventID) bool {
			ent, ok := info.space.State.EntryByID(e)
			return ok && ent.Author == info.myself
		},
	}
	for i := range entries {
		e := &entries[i]
		if st.engine.Inbox.Judged(e.ID) {
			continue
		}
		c := attention.Candidate{
			EventID: e.ID, SpaceID: info.tid, Author: e.Author,
			Kind: string(e.Kind), CreatedAt: e.CreatedAt, ReceivedAt: now,
		}
		if e.Content.Text != nil {
			c.Text = e.Content.Text.Text
			c.ReplyTo = e.Content.Text.ReplyTo
			c.Mentions = e.Content.Text.Mentions
		} else {
			c.Text = fallbackText(e)
		}
		ctx := attention.Context{
			Viewer: viewer, SpaceTitle: info.title,
			AuthorName:  info.names[e.Author],
			AuthorKind:  info.kinds[e.Author],
			AuthorKnown: info.names[e.Author] != "",
			ViewingNow:  info.tid == viewing,
		}
		if _, ok := st.engine.Judge(c, ctx, now); ok {
			st.dirty = true
		} else {
			st.dirty = true // the seen-set moved even when nothing surfaced
		}
	}
}

// fallbackText gives non-text entries something to rank on — their honest
// fallback string, the same one a text-only terminal would show.
func fallbackText(e *reducers.Entry) string {
	switch {
	case e.Content.Visual != nil:
		return e.Content.Visual.Fallback()
	case e.Content.Video != nil:
		return e.Content.Video.Fallback()
	case e.Content.Voice != nil:
		return e.Content.Voice.Fallback()
	case e.Content.Audio != nil:
		return e.Content.Audio.Fallback()
	case e.Content.File != nil:
		return e.Content.File.Fallback()
	case e.Content.Link != nil:
		return e.Content.Link.Fallback()
	case e.Content.Unknown != nil:
		return e.Content.Unknown.Fallback
	}
	return ""
}

// Signals returns the inbox, newest local arrival first.
func (r *Runtime) Signals() []attention.Signal {
	r.ScanAttention()
	st := r.attn()
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.engine.Inbox.List()
}

// UnseenSignals is the badge count.
func (r *Runtime) UnseenSignals() int {
	st := r.attn()
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.engine.Inbox.Unseen()
}

// MarkSignalsSeen marks one signal, or all when id is empty.
func (r *Runtime) MarkSignalsSeen(sigID string) {
	st := r.attn()
	st.mu.Lock()
	st.engine.Inbox.MarkSeen(sigID)
	st.dirty = true
	st.mu.Unlock()
	r.persistAttention(st, true)
}

// SignalFeedback applies a LEARNING verdict — useful / not mine / should
// have caught this. Policy commands (mute, digest-only) go through
// SetAttentionPolicy instead and never touch the model.
func (r *Runtime) SignalFeedback(sigID string, useful bool) error {
	st := r.attn()
	st.mu.Lock()
	sig, ok := st.engine.Inbox.Find(sigID)
	st.mu.Unlock()
	if !ok {
		return errNotFound
	}
	return r.learnFrom(st, sig.SourceSpace, sig.SourceEvent, useful)
}

// NoticeEvent is the "you should have caught this" path: a POSITIVE example
// taken from an ordinary message. Without it the layer would only ever learn
// to go quiet.
func (r *Runtime) NoticeEvent(tid id.TerminalID, eid id.EventID) error {
	st := r.attn()
	return r.learnFrom(st, tid, eid, true)
}

func (r *Runtime) learnFrom(st *attentionState, tid id.TerminalID, eid id.EventID, useful bool) error {
	r.mu.Lock()
	sp, ok := r.spaces[tid]
	me := r.PrincipalID
	r.mu.Unlock()
	if !ok {
		return errNotFound
	}
	ent, ok := sp.space.State.EntryByID(eid)
	if !ok {
		return errNotFound
	}
	c := attention.Candidate{
		EventID: ent.ID, SpaceID: tid, Author: ent.Author,
		Kind: string(ent.Kind), CreatedAt: ent.CreatedAt,
	}
	if ent.Content.Text != nil {
		c.Text = ent.Content.Text.Text
		c.ReplyTo = ent.Content.Text.ReplyTo
		c.Mentions = ent.Content.Text.Mentions
	} else {
		c.Text = fallbackText(&ent)
	}
	st.mu.Lock()
	st.engine.Feedback(c, attention.Context{
		Viewer: attention.Viewer{Principal: me, Aliases: st.engine.Policy.Aliases},
	}, useful)
	st.dirty = true
	st.mu.Unlock()
	r.persistAttention(st, true)
	return nil
}

// ForgetAttention deletes the learned profile and every stored signal.
func (r *Runtime) ForgetAttention() error {
	st := r.attn()
	st.mu.Lock()
	st.engine.Model = attention.NewModel()
	st.engine.Inbox = attention.NewInbox()
	st.dirty = false
	st.mu.Unlock()
	r.mu.Lock()
	root := r.root
	r.mu.Unlock()
	return attention.DeleteAll(root)
}

// persistAttention writes the sealed blobs, debounced unless forced.
func (r *Runtime) persistAttention(st *attentionState, force bool) {
	st.mu.Lock()
	if !st.dirty || (!force && time.Since(st.lastSave) < attentionSaveDebounce) {
		st.mu.Unlock()
		return
	}
	st.dirty = false
	st.lastSave = time.Now()
	inbox := st.engine.Inbox
	model := st.engine.Model
	st.mu.Unlock()

	r.mu.Lock()
	root := r.root
	r.mu.Unlock()
	_ = attention.SaveInbox(root, inbox)
	_ = attention.SaveModel(root, model)
}

// storage.Root satisfies attention.Sealed.
var _ attention.Sealed = (*storage.Root)(nil)

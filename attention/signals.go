package attention

import (
	"sort"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// Bounds. Signals are capped by BOTH count and age: a ring that only counts
// would hoard a year of stale entries on a quiet node, and one that only
// aged would grow without limit on a loud one.
const (
	MaxSignals   = 500
	MaxSignalAge = 30 * 24 * time.Hour
	// MaxSeen bounds the "already judged" set. Eviction is safe: the
	// already-shown rule keeps an evicted event from resurfacing as new.
	MaxSeen = 4096
)

// Signal is one thing worth the person's attention. SourceSpace/SourceEvent
// always point back at the original — a signal is a pointer, never a
// replacement for the message.
type Signal struct {
	ID          string        `json:"id"`
	SourceSpace id.TerminalID `json:"-"`
	SourceEvent id.EventID    `json:"-"`
	SpaceHex    string        `json:"space"`
	EventHex    string        `json:"event"`
	SpaceTitle  string        `json:"space_title,omitempty"`
	Author      string        `json:"author,omitempty"`
	// AuthorKind (declared terminal kind) bends the chime's timbre; Personal
	// (mention / reply-to-me) picks the richer motif. Neither affects rank.
	AuthorKind string `json:"author_kind,omitempty"`
	Personal   bool   `json:"personal,omitempty"`
	Excerpt     string        `json:"excerpt"`
	Delivery    Delivery      `json:"delivery"`
	Hard        bool          `json:"hard"`
	Reasons     []Reason      `json:"reasons"`
	Score       float64       `json:"score"`
	// CreatedAt is the author's advisory clock; ReceivedAt is the local
	// fact, and is what ordering and budgets use.
	CreatedAt  uint64 `json:"created_at,omitempty"`
	ReceivedAt int64  `json:"received_at"`
	Seen       bool   `json:"seen"`
	// Layer records which layer produced this: "rules" or "lexical" (later
	// "semantic"). Shown honestly in the UI.
	Layer string `json:"layer"`
}

// SeenRecord remembers that an event was already judged, and WHEN it first
// arrived locally. ReceivedAt is written once and never revised — otherwise
// a rescan would reshuffle the inbox and let old events slip past quiet
// hours.
type SeenRecord struct {
	Event      id.EventID `json:"e"`
	ReceivedAt int64      `json:"r"`
}

// Inbox is the device-local signal store.
type Inbox struct {
	Signals []Signal             `json:"signals"`
	Seen    map[id.EventID]int64 `json:"-"`
	seenOrd []id.EventID
}

func NewInbox() *Inbox {
	return &Inbox{Seen: map[id.EventID]int64{}}
}

// FirstSeen returns the local arrival time of an event, recording it on the
// first sighting. This is the single place ReceivedAt is minted.
func (in *Inbox) FirstSeen(e id.EventID, now int64) (at int64, fresh bool) {
	if in.Seen == nil {
		in.Seen = map[id.EventID]int64{}
	}
	if t, ok := in.Seen[e]; ok {
		return t, false
	}
	in.Seen[e] = now
	in.seenOrd = append(in.seenOrd, e)
	for len(in.seenOrd) > MaxSeen {
		oldest := in.seenOrd[0]
		in.seenOrd = in.seenOrd[1:]
		delete(in.Seen, oldest)
	}
	return now, true
}

// Judged reports whether this event has already been through the layer.
func (in *Inbox) Judged(e id.EventID) bool {
	_, ok := in.Seen[e]
	return ok
}

// Add stores a signal, keeping the ring bounded by count and age.
func (in *Inbox) Add(s Signal, now int64) {
	in.Signals = append(in.Signals, s)
	in.prune(now)
}

func (in *Inbox) prune(now int64) {
	cutoff := now - int64(MaxSignalAge/time.Second)
	kept := in.Signals[:0]
	for _, s := range in.Signals {
		if s.ReceivedAt >= cutoff {
			kept = append(kept, s)
		}
	}
	in.Signals = kept
	if len(in.Signals) > MaxSignals {
		in.Signals = in.Signals[len(in.Signals)-MaxSignals:]
	}
}

// List returns signals newest-first by LOCAL arrival — an author's clock
// must not be able to pin their message to the top of your inbox.
func (in *Inbox) List() []Signal {
	out := append([]Signal(nil), in.Signals...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ReceivedAt > out[j].ReceivedAt
	})
	return out
}

// Unseen counts signals the person has not looked at yet.
func (in *Inbox) Unseen() int {
	n := 0
	for _, s := range in.Signals {
		if !s.Seen && s.Delivery != DeliverySuppressed {
			n++
		}
	}
	return n
}

// MarkSeen marks one signal (empty id marks all).
func (in *Inbox) MarkSeen(sigID string) {
	for i := range in.Signals {
		if sigID == "" || in.Signals[i].ID == sigID {
			in.Signals[i].Seen = true
		}
	}
}

// Find returns a stored signal by id.
func (in *Inbox) Find(sigID string) (Signal, bool) {
	for _, s := range in.Signals {
		if s.ID == sigID {
			return s, true
		}
	}
	return Signal{}, false
}

// SeenSnapshot renders the seen-set for persistence, newest last.
func (in *Inbox) SeenSnapshot() []SeenRecord {
	out := make([]SeenRecord, 0, len(in.seenOrd))
	for _, e := range in.seenOrd {
		out = append(out, SeenRecord{Event: e, ReceivedAt: in.Seen[e]})
	}
	return out
}

// LoadSeen restores a persisted seen-set.
func (in *Inbox) LoadSeen(recs []SeenRecord) {
	in.Seen = make(map[id.EventID]int64, len(recs))
	in.seenOrd = in.seenOrd[:0]
	for _, r := range recs {
		if _, dup := in.Seen[r.Event]; dup {
			continue
		}
		in.Seen[r.Event] = r.ReceivedAt
		in.seenOrd = append(in.seenOrd, r.Event)
	}
}

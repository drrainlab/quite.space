// Gateway presence (RB-2). What a node does with the beacons it hears.
//
// Three situations look identical from inside a space: there is no gateway
// on this mesh, the gateway is there and busy, and this radio is on the
// wrong channel. In all three, nothing happens. Presence separates them —
// and does it without ever letting a beacon become authority.
//
// Two rules carry the weight here.
//
// AUTHENTICITY IS NOT AUTHORITY. A beacon that verifies proves only that
// whoever holds that key sent it. Whether this node should believe anything
// it says is the pin decision (ADR-015 §7), which is separate and explicit.
// An unpinned gateway is SHOWN, marked untrusted, with its fingerprint — the
// person compares that against what the operator told them. Hiding it would
// leave them nothing to work with; believing it would be trust on first use.
//
// FRESHNESS NEVER TOUCHES THE WALL CLOCK. A device that has been off the
// internet for days can have a badly wrong system time, and a presence check
// that silently failed on clock drift would be the least debuggable failure
// in this wave. Elapsed time is measured on this device's own monotonic
// clock; the gateway's absolute timestamp is used only to order one of its
// boots against another — a gateway claim against a gateway claim.
//
// And presence is ADVISORY. It changes what a person is shown. It never
// gates the queue: a node that refused to transmit until it had heard a
// gateway would be silent on any segment whose gateway is merely quiet, and
// could never bootstrap at all.
package node

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/transports/bridge"
)

// maxGateways bounds what one carrier can make this node remember. Anyone
// can broadcast a signed beacon under a fresh key, so the untrusted set is
// attacker-controlled by nature.
const maxGateways = 32

// GatewayPresence is one gateway this node has heard, as shown to a person.
type GatewayPresence struct {
	Key         ed25519.PublicKey
	Fingerprint string
	Label       string
	NetworkID   string
	LinkDomain  string

	// Trusted means the key is pinned FOR THIS LINK. A beacon from an
	// unpinned key is still listed — that is the bootstrap ritual, not a
	// failure.
	Trusted    bool
	UplinkUp   bool
	QueueDepth uint64

	BootID     uint64
	Sequence   uint64
	IssuedSlot uint64

	// LastHeard is when a beacon this node had not already seen arrived,
	// measured locally. ValidFor is what the gateway itself promised.
	LastHeard time.Time
	ValidFor  time.Duration
}

// Fresh reports whether the announcement still stands at now, by elapsed
// time on this device's clock — never by comparing timestamps.
func (g GatewayPresence) Fresh(now time.Time) bool {
	if g.ValidFor <= 0 {
		return false
	}
	return now.Sub(g.LastHeard) < g.ValidFor
}

// noteBeacon folds one beacon heard on a link into presence.
//
// arrivedAt is passed in rather than read here so the ordering rules can be
// tested without sleeping. In production it is time.Now(), whose monotonic
// reading is what Fresh later subtracts.
func (r *Runtime) noteBeacon(linkDomain string, raw []byte, arrivedAt time.Time) {
	b, err := bridge.VerifyBeacon(raw)
	if err != nil {
		// Noise, or a forgery. Either way it is not evidence, and it must
		// leave no trace: otherwise anyone on the carrier could invent a
		// gateway by broadcasting rubbish.
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// A configured network id filters; an unconfigured one does not. Refusing
	// by default would hide the only gateway a person can hear from someone
	// who has not set an id yet — and a typo in the id would then be
	// indistinguishable from an absent gateway, which is why the refusals
	// are counted rather than dropped.
	if r.meshNetworkID != "" && b.NetworkID != r.meshNetworkID {
		r.foreignBeacons++
		return
	}

	key := hex.EncodeToString(b.PublicKey)
	if r.gateways == nil {
		r.gateways = map[string]*GatewayPresence{}
	}
	prev, known := r.gateways[key]
	if !known && len(r.gateways) >= maxGateways {
		// Anyone can mint a key and announce under it, so this set is
		// attacker-controlled. Drop the stalest rather than grow without
		// bound; a real gateway announces again.
		r.evictStalestGatewayLocked()
	}
	if known && !supersedes(b, prev) {
		// A repeat, or a replay of something older. Presence is NOT
		// refreshed: a captured packet rebroadcast forever would otherwise
		// make a gateway that has been off for a week look present.
		return
	}

	pin, pinned := r.custodians[linkDomain]
	r.gateways[key] = &GatewayPresence{
		Key:         append(ed25519.PublicKey(nil), b.PublicKey...),
		Fingerprint: fingerprintOf(b.PublicKey),
		Label:       b.Label,
		NetworkID:   b.NetworkID,
		LinkDomain:  linkDomain,
		Trusted:     pinned && string(pin) == string(b.PublicKey),
		UplinkUp:    b.UplinkUp,
		QueueDepth:  b.QueueDepth,
		BootID:      b.BootID,
		Sequence:    b.Sequence,
		IssuedSlot:  b.IssuedSlot,
		LastHeard:   arrivedAt,
		ValidFor:    time.Duration(b.ValidFor) * time.Second,
	}
}

// supersedes decides whether a beacon is genuinely newer than what is held.
//
// Within one boot, the sequence orders announcements. Across boots it cannot:
// a restarted gateway begins again at 1, so a replay of a pre-restart beacon
// would carry a much higher sequence than the current one. Boots are ordered
// by the gateway's OWN stated time instead — one of its claims against
// another of its claims, never against this device's clock, which may be
// wrong by days.
func supersedes(b *bridge.Beacon, prev *GatewayPresence) bool {
	if b.BootID == prev.BootID {
		return b.Sequence > prev.Sequence
	}
	return b.IssuedSlot > prev.IssuedSlot
}

// evictStalestGatewayLocked drops the least recently heard. Caller holds r.mu.
func (r *Runtime) evictStalestGatewayLocked() {
	var oldestKey string
	var oldest time.Time
	for k, g := range r.gateways {
		if oldestKey == "" || g.LastHeard.Before(oldest) {
			oldestKey, oldest = k, g.LastHeard
		}
	}
	delete(r.gateways, oldestKey)
}

// SetMeshNetwork restricts which segment's beacons this node listens to.
// Empty (the default) accepts any, and shows the network id it heard.
func (r *Runtime) SetMeshNetwork(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.meshNetworkID = id
}

// MeshNetwork reports the configured segment id.
func (r *Runtime) MeshNetwork() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.meshNetworkID
}

// ForeignBeacons counts beacons refused for naming another network. A typo
// in the network id would otherwise look exactly like an absent gateway.
func (r *Runtime) ForeignBeacons() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.foreignBeacons
}

// Gateways lists what this node has heard, trusted first, then by label.
func (r *Runtime) Gateways() []GatewayPresence {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]GatewayPresence, 0, len(r.gateways))
	for _, g := range r.gateways {
		p := *g
		// Trust is evaluated NOW, not remembered from when the beacon
		// arrived. Someone who pins a gateway from the screen would
		// otherwise go on being told it is untrusted until the next
		// announcement — up to several minutes on LoRa, and indistinguishable
		// from a button that did nothing.
		pin, pinned := r.custodians[p.LinkDomain]
		p.Trusted = pinned && string(pin) == string(p.Key)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Trusted != out[j].Trusted {
			return out[i].Trusted
		}
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].Fingerprint < out[j].Fingerprint
	})
	return out
}

// GatewaySummary is one line for a person, whose job is to say WHICH silence
// this is.
func (r *Runtime) GatewaySummary(now time.Time) string {
	gws := r.Gateways()
	if len(gws) == 0 {
		if n := r.ForeignBeacons(); n > 0 {
			return fmt.Sprintf("no gateway on this network — but %d beacons "+
				"from another network were heard. Check the network id on "+
				"both sides.", n)
		}
		return "no gateway has announced itself on this mesh. Messages still " +
			"go out; nothing has offered to carry them past the radio yet."
	}
	var lines []string
	for _, g := range gws {
		name := g.Label
		if name == "" {
			name = "gateway " + g.Fingerprint
		}
		var parts []string
		if g.Trusted {
			parts = append(parts, "trusted")
		} else {
			parts = append(parts, "not trusted — pin "+g.Fingerprint+
				" if that matches what the operator gave you")
		}
		if g.UplinkUp {
			parts = append(parts, "internet uplink up")
		} else {
			parts = append(parts, "no internet uplink: it can carry within "+
				"the mesh but cannot reach the relay")
		}
		if g.QueueDepth > 0 {
			parts = append(parts, fmt.Sprintf("%d in its queue", g.QueueDepth))
		}
		if !g.Fresh(now) {
			parts = append(parts, "GONE QUIET, last heard "+
				humanAgo(now.Sub(g.LastHeard)))
		}
		lines = append(lines, name+" — "+strings.Join(parts, " · "))
	}
	return strings.Join(lines, "\n")
}

func humanAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}

// Relay wire protocol v0 (M1.5): CBOR messages over the framed TLS
// connections from transports/lan. The relay reads exactly four things per
// item — a rotating hint, an expiry, a size, ciphertext — and nothing else
// exists in the protocol for it to read (plan §5.4).
//
// Rotating hints: recipients derive Hint(terminal, bucket) locally; the
// relay cannot link buckets to each other or to a terminal without already
// knowing the terminal id. Honest limit: whoever knows a terminal id can
// compute its hints — hints hide identity from strangers, not from
// acquaintances.
package relay

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// HintLen is the rotating hint size in bytes.
const HintLen = 16

// CapLen is the size of a collect capability.
const CapLen = 32

// CollectHint is the whole of PH-1, in one line: a mailbox is ADDRESSED by
// the hash of the capability that drains it.
//
//	hint = SHA256("qp-relay-collect-v1:" ‖ cap)[:HintLen]
//
// Put, Replace and Fetch still address the 16-byte hint, so every WRITER
// path is untouched — anyone who should be able to leave something in a
// mailbox still can. Only draining changes: the collector sends the cap and
// the relay derives the hint itself. The relay compares nothing, verifies
// no signature, knows no accounts, and still cannot tell one mailbox class
// from another. One SHA-256 is the entire enforcement.
//
// The property this buys: you must be able to DERIVE a hint, not merely to
// have OBSERVED one. That is what separates the owner of a mailbox from
// everyone who can see its address go past — and for a public space, the
// address is derivable from the space id, which is the read capability
// itself. Before this, "can read the space" implied "can empty its
// mailboxes".
func CollectHint(cap []byte) []byte {
	h := sha256.New()
	h.Write([]byte("qp-relay-collect-v1:"))
	h.Write(cap)
	return h.Sum(nil)[:HintLen]
}

// Cap derives the collect capability for a terminal's shared mailbox.
// The label differs from LAN discovery hints so the two cannot be joined.
func Cap(terminal id.TerminalID, bucket uint64) []byte {
	h := sha256.New()
	h.Write([]byte("qp-relay-hint-v0:"))
	h.Write(terminal[:])
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], bucket)
	h.Write(b[:])
	return h.Sum(nil)
}

// Hint is where Cap's mailbox lives.
func Hint(terminal id.TerminalID, bucket uint64) []byte {
	return CollectHint(Cap(terminal, bucket))
}

// HintFor derives a PER-RECIPIENT mailbox hint: the box where one device
// collects a terminal's frames pushed for it. The shared Hint(terminal,
// bucket) mailbox is single-reader — destructive Collect means whichever
// space member polls first drains copies meant for the others. Per-recipient
// boxes give every member their own inbox, so many members can poll the same
// relay concurrently (e.g. a 2 s auto-sync cadence) without eating each
// other's mail. The relay stays blind: it still sees only opaque 16-byte
// hints and cannot tell a per-recipient box from a per-terminal one.
// Honest limit, unchanged by PH-1: this capability is derivable by anyone
// who knows the space id AND the device id, so it protects a PRIVATE
// space's inbox (where membership is the gate) and would protect nothing in
// a public one, where both ids travel in the clear. Public spaces therefore
// do not use this path at all — media answers there go to a reply box whose
// capability is random and known only to the device that asked (ReplyCap).
func CapFor(terminal id.TerminalID, recipient id.DeviceID, bucket uint64) []byte {
	h := sha256.New()
	h.Write([]byte("qp-relay-inbox-v0:"))
	h.Write(terminal[:])
	h.Write(recipient[:])
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], bucket)
	h.Write(b[:])
	return h.Sum(nil)
}

// HintFor is where CapFor's inbox lives.
func HintFor(terminal id.TerminalID, recipient id.DeviceID, bucket uint64) []byte {
	return CollectHint(CapFor(terminal, recipient, bucket))
}

// NewReplyCap mints a fresh reply-box capability: CapLen random bytes, kept
// by the device that asks for media and shared with nobody. The holder of a
// wanted blob learns only the HINT (from the want bundle) and can therefore
// deliver into the box without ever being able to empty it — which is the
// difference between answering a request and intercepting one.
func NewReplyCap() ([]byte, error) {
	c := make([]byte, CapLen)
	if _, err := rand.Read(c); err != nil {
		return nil, err
	}
	return c, nil
}

// Bucket returns the hint rotation bucket (6-hour windows, shared with LAN
// discovery granularity).
func Bucket(nowUnix uint64) uint64 { return nowUnix / (6 * 3600) }

// ---- PA-0 public-space mailbox namespaces ----
//
// Three distinct hint derivations keep the relay blind while separating
// traffic classes: the relay sees only opaque 16-byte hints and cannot tell
// an outbox from an inbox from a member mailbox.

// HintPublicOutbox is the owner→everyone mailbox: the signed public
// projection lives here (one item, atomically Replaced). Anyone who knows
// the space id derives it — that IS the read capability of a public space.
func HintPublicOutbox(space id.TerminalID, bucket uint64) []byte {
	h := sha256.New()
	h.Write([]byte("qp-relay-pub-out-v0:"))
	h.Write(space[:])
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], bucket)
	h.Write(b[:])
	return h.Sum(nil)[:HintLen]
}

// IngressShards is the deterministic shard fan-out of the public ingress
// (participants→owner): one author always lands in one shard, and the
// per-hint item cap multiplies by the shard count.
const IngressShards = 8

// IngressShard maps a contributor device to its ingress shard.
func IngressShard(dev id.DeviceID) byte {
	d := sha256.Sum256(dev[:])
	return d[0] % IngressShards
}

// CapPublicIngress is the owner's drain capability for one ingress shard.
// The root is derived from the space SEED (PH-2), which only the owner
// holds, so contributors cannot compute it — they learn the resulting HINT
// from the signed projection instead. That asymmetry is the point: everyone
// may leave a contribution, only the owner may take one.
func CapPublicIngress(root [32]byte, bucket uint64, shard byte) []byte {
	h := hmac.New(sha256.New, root[:])
	h.Write([]byte("qp-relay-pub-in-v1:"))
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], bucket)
	h.Write(b[:])
	h.Write([]byte{shard})
	return h.Sum(nil)
}

// HintPublicIngress is one shard of the participants→owner mailbox
// (append-only Put with short TTL). PRE-PH-2 derivation, kept while the
// projection has no committed hints to publish: derivable from the space id
// alone, which is exactly why PH-2 replaces it.
func HintPublicIngress(space id.TerminalID, bucket uint64, shard byte) []byte {
	h := sha256.New()
	h.Write([]byte("qp-relay-pub-in-v0:"))
	h.Write(space[:])
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], bucket)
	h.Write(b[:])
	h.Write([]byte{shard})
	return CollectHint(h.Sum(nil))
}

// CapPublicIngressLegacy matches HintPublicIngress until PH-2 lands.
func CapPublicIngressLegacy(space id.TerminalID, bucket uint64, shard byte) []byte {
	h := sha256.New()
	h.Write([]byte("qp-relay-pub-in-v0:"))
	h.Write(space[:])
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], bucket)
	h.Write(b[:])
	h.Write([]byte{shard})
	return h.Sum(nil)
}

// Message types.
const (
	MsgPut     = 1
	MsgPutOK   = 2
	MsgCollect = 3
	MsgItems   = 4
	MsgError   = 5
	// LR-2: relay time — the common calibration source for listening
	// sessions. The relay's clock carries no authority beyond being SHARED.
	MsgTime   = 6
	MsgTimeOK = 7
	// PA-0 public mailboxes (append-only table, ADR-009):
	// MsgFetch reads WITHOUT removing (public outbox has many readers);
	// MsgFetchItems is its reply, distinct from MsgItems so a client can
	// never confuse a destructive collect with a fetch;
	// MsgReplace atomically swaps a hint's contents with one item (the
	// projection mailbox holds exactly the latest projection).
	MsgFetch      = 8
	MsgFetchItems = 9
	MsgReplace    = 10
	// PH-1: draining requires a CAPABILITY, not merely knowledge of a hint.
	// MsgCollect (3) is refused outright rather than silently answering with
	// an empty mailbox — an old client deserves a diagnosable error, and a
	// silent empty drain is indistinguishable from "nothing was waiting".
	MsgCollectCap = 11
	// RR-3: the measured-selection probe. One cheap round trip carrying a
	// client nonce; the reply names the relay's protocol range, load class
	// and whether it accepts new sessions, PLUS the wall clock (keyNow) —
	// so selection and clock calibration share one benchmark instead of
	// two. Writes no durable state; metered like a collect.
	MsgProbe   = 12
	MsgProbeOK = 13
)

// RelayProtocolVersion is this build's wire protocol generation. Bumped
// only on a break an old peer cannot skip past.
const RelayProtocolVersion = 1

// Load classes a relay may self-report (advisory — a client never owes
// them blind trust, but respects draining/not-accepting).
const (
	LoadNormal     = "normal"
	LoadBusy       = "busy"
	LoadOverloaded = "overloaded"
	LoadDraining   = "draining"
)

// Message key table v0 (append-only, ADR-009).
const (
	keyType    = 1
	keyHint    = 2
	keyExpires = 3
	keyBody    = 4
	keyHints   = 5
	keyItems   = 6
	keyReason  = 7
	keyNow     = 8 // relay wall time, unix milliseconds
	keyCaps    = 9 // PH-1 collect capabilities
	// RR-3 probe keys.
	keyNonce     = 10
	keyProtoMin  = 11
	keyProtoMax  = 12
	keyLoad      = 13
	keyAccepting = 14 // 1 = accepting new sessions

	// keyRetryAfterMs — how long a refused caller should wait, in
	// milliseconds, sent with a rate-limit refusal.
	//
	// A DURATION RATHER THAN A DEADLINE, deliberately: the two clocks have
	// never been assumed to agree anywhere else in this protocol, and a
	// timestamp would make a client with a skewed clock either hammer a relay
	// that asked for quiet or sleep for hours. A duration is relative to the
	// moment the answer arrived, which both sides can measure.
	//
	// Append-only, and unknown keys are skipped, so an older relay that never
	// sends it and an older client that never reads it both keep working — the
	// client simply falls back to waiting out the limiter's own window.
	keyRetryAfterMs = 15
)

// Msg is one relay protocol message.
type Msg struct {
	Type    uint64
	Hint    []byte
	Expires uint64
	Body    []byte
	Hints   [][]byte
	Items   [][]byte
	Reason  string
	// RetryAfterMs accompanies a refusal that waiting will fix. Zero means the
	// relay did not say, not that the answer is "immediately".
	RetryAfterMs uint64
	Now          uint64   // unix ms (MsgTimeOK / MsgProbeOK)
	Caps         [][]byte // PH-1: collect capabilities (MsgCollectCap)
	// RR-3 probe fields.
	Nonce     []byte
	ProtoMin  uint64
	ProtoMax  uint64
	Load      string
	Accepting uint64
}

// Encode serializes a message.
func (m *Msg) Encode() []byte {
	n := 1
	if len(m.Hint) > 0 {
		n++
	}
	if m.Expires != 0 {
		n++
	}
	if m.Body != nil {
		n++
	}
	if len(m.Hints) > 0 {
		n++
	}
	if m.Items != nil {
		n++
	}
	if m.Reason != "" {
		n++
	}
	if m.RetryAfterMs != 0 {
		n++
	}
	if m.Now != 0 {
		n++
	}
	if len(m.Caps) > 0 {
		n++
	}
	if len(m.Nonce) > 0 {
		n++
	}
	if m.ProtoMin != 0 {
		n++
	}
	if m.ProtoMax != 0 {
		n++
	}
	if m.Load != "" {
		n++
	}
	if m.Accepting != 0 {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, keyType)
	buf = codec.AppendUint(buf, m.Type)
	if len(m.Hint) > 0 {
		buf = codec.AppendUint(buf, keyHint)
		buf = codec.AppendBytes(buf, m.Hint)
	}
	if m.Expires != 0 {
		buf = codec.AppendUint(buf, keyExpires)
		buf = codec.AppendUint(buf, m.Expires)
	}
	if m.Body != nil {
		buf = codec.AppendUint(buf, keyBody)
		buf = codec.AppendBytes(buf, m.Body)
	}
	if len(m.Hints) > 0 {
		buf = codec.AppendUint(buf, keyHints)
		buf = codec.AppendArray(buf, len(m.Hints))
		for _, h := range m.Hints {
			buf = codec.AppendBytes(buf, h)
		}
	}
	if m.Items != nil {
		buf = codec.AppendUint(buf, keyItems)
		buf = codec.AppendArray(buf, len(m.Items))
		for _, it := range m.Items {
			buf = codec.AppendBytes(buf, it)
		}
	}
	if m.Reason != "" {
		buf = codec.AppendUint(buf, keyReason)
		buf = codec.AppendText(buf, m.Reason)
	}
	if m.RetryAfterMs != 0 {
		buf = codec.AppendUint(buf, keyRetryAfterMs)
		buf = codec.AppendUint(buf, m.RetryAfterMs)
	}
	if m.Now != 0 {
		buf = codec.AppendUint(buf, keyNow)
		buf = codec.AppendUint(buf, m.Now)
	}
	if len(m.Caps) > 0 {
		buf = codec.AppendUint(buf, keyCaps)
		buf = codec.AppendArray(buf, len(m.Caps))
		for _, c := range m.Caps {
			buf = codec.AppendBytes(buf, c)
		}
	}
	if len(m.Nonce) > 0 {
		buf = codec.AppendUint(buf, keyNonce)
		buf = codec.AppendBytes(buf, m.Nonce)
	}
	if m.ProtoMin != 0 {
		buf = codec.AppendUint(buf, keyProtoMin)
		buf = codec.AppendUint(buf, m.ProtoMin)
	}
	if m.ProtoMax != 0 {
		buf = codec.AppendUint(buf, keyProtoMax)
		buf = codec.AppendUint(buf, m.ProtoMax)
	}
	if m.Load != "" {
		buf = codec.AppendUint(buf, keyLoad)
		buf = codec.AppendText(buf, m.Load)
	}
	if m.Accepting != 0 {
		buf = codec.AppendUint(buf, keyAccepting)
		buf = codec.AppendUint(buf, m.Accepting)
	}
	return buf
}

// DecodeMsg parses a message.
func DecodeMsg(data []byte) (*Msg, error) {
	d := codec.NewDecoder(data)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	m := &Msg{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case keyType:
			m.Type, er = d.ReadUint()
		case keyHint:
			var b []byte
			b, er = d.ReadBytes()
			m.Hint = append([]byte(nil), b...)
		case keyExpires:
			m.Expires, er = d.ReadUint()
		case keyNow:
			m.Now, er = d.ReadUint()
		case keyRetryAfterMs:
			m.RetryAfterMs, er = d.ReadUint()
		case keyReason:
			// READ, because it was written and never read. The encoder has
			// always sent the reason a relay refused for, and the decoder
			// skipped the key — so every refusal arrived at the client as
			// `relay: ` with nothing after it. Diagnostics said nothing, and
			// anything wanting to tell a rate limit from a malformed request
			// had to guess from context, which is how classification by
			// substring gets invented in the first place.
			m.Reason, er = d.ReadText()
		case keyBody:
			var b []byte
			b, er = d.ReadBytes()
			m.Body = append([]byte(nil), b...)
		case keyHints:
			var cnt int
			cnt, er = d.ReadArray()
			if er != nil {
				return nil, er
			}
			for range cnt {
				h, e := d.ReadBytes()
				if e != nil {
					return nil, e
				}
				m.Hints = append(m.Hints, append([]byte(nil), h...))
			}
		case keyCaps:
			var cnt int
			cnt, er = d.ReadArray()
			if er != nil {
				return nil, er
			}
			for range cnt {
				c, e := d.ReadBytes()
				if e != nil {
					return nil, e
				}
				m.Caps = append(m.Caps, append([]byte(nil), c...))
			}
		case keyItems:
			var cnt int
			cnt, er = d.ReadArray()
			if er != nil {
				return nil, er
			}
			m.Items = [][]byte{}
			for range cnt {
				it, e := d.ReadBytes()
				if e != nil {
					return nil, e
				}
				m.Items = append(m.Items, append([]byte(nil), it...))
			}
		case keyNonce:
			var b []byte
			b, er = d.ReadBytes()
			m.Nonce = append([]byte(nil), b...)
		case keyProtoMin:
			m.ProtoMin, er = d.ReadUint()
		case keyProtoMax:
			m.ProtoMax, er = d.ReadUint()
		case keyLoad:
			m.Load, er = d.ReadText()
		case keyAccepting:
			m.Accepting, er = d.ReadUint()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if m.Type == 0 {
		return nil, errors.New("relay: message without type")
	}
	return m, nil
}

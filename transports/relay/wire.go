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
	"crypto/sha256"
	"encoding/binary"
	"errors"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// HintLen is the rotating hint size in bytes.
const HintLen = 16

// Hint derives the relay mailbox hint for a terminal in a time bucket.
// The label differs from LAN discovery hints so the two cannot be joined.
func Hint(terminal id.TerminalID, bucket uint64) []byte {
	h := sha256.New()
	h.Write([]byte("qp-relay-hint-v0:"))
	h.Write(terminal[:])
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], bucket)
	h.Write(b[:])
	return h.Sum(nil)[:HintLen]
}

// HintFor derives a PER-RECIPIENT mailbox hint: the box where one device
// collects a terminal's frames pushed for it. The shared Hint(terminal,
// bucket) mailbox is single-reader — destructive Collect means whichever
// space member polls first drains copies meant for the others. Per-recipient
// boxes give every member their own inbox, so many members can poll the same
// relay concurrently (e.g. a 2 s auto-sync cadence) without eating each
// other's mail. The relay stays blind: it still sees only opaque 16-byte
// hints and cannot tell a per-recipient box from a per-terminal one.
func HintFor(terminal id.TerminalID, recipient id.DeviceID, bucket uint64) []byte {
	h := sha256.New()
	h.Write([]byte("qp-relay-inbox-v0:"))
	h.Write(terminal[:])
	h.Write(recipient[:])
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], bucket)
	h.Write(b[:])
	return h.Sum(nil)[:HintLen]
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

// HintPublicIngress is one shard of the participants→owner mailbox
// (append-only Put with short TTL; the owner Fetches all shards).
func HintPublicIngress(space id.TerminalID, bucket uint64, shard byte) []byte {
	h := sha256.New()
	h.Write([]byte("qp-relay-pub-in-v0:"))
	h.Write(space[:])
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], bucket)
	h.Write(b[:])
	h.Write([]byte{shard})
	return h.Sum(nil)[:HintLen]
}

// Message types.
const (
	msgPut     = 1
	msgPutOK   = 2
	msgCollect = 3
	msgItems   = 4
	msgError   = 5
	// LR-2: relay time — the common calibration source for listening
	// sessions. The relay's clock carries no authority beyond being SHARED.
	msgTime   = 6
	msgTimeOK = 7
	// PA-0 public mailboxes (append-only table, ADR-009):
	// msgFetch reads WITHOUT removing (public outbox has many readers);
	// msgFetchItems is its reply, distinct from msgItems so a client can
	// never confuse a destructive collect with a fetch;
	// msgReplace atomically swaps a hint's contents with one item (the
	// projection mailbox holds exactly the latest projection).
	msgFetch      = 8
	msgFetchItems = 9
	msgReplace    = 10
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
	Now     uint64 // unix ms (msgTimeOK)
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
	if m.Now != 0 {
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
	if m.Now != 0 {
		buf = codec.AppendUint(buf, keyNow)
		buf = codec.AppendUint(buf, m.Now)
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

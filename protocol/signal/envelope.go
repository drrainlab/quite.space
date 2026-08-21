// Package signal implements Signal Envelope v0 (engineering plan §12,
// ADR-003/ADR-004): construction and signing by authors, and structural
// verification of received frames without ever re-serializing them.
package signal

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// Envelope wire key table v0. Append-only (ADR-009): keys are never reused.
const (
	keyVersion         = 1
	keyTerminal        = 2
	keyPrincipal       = 3
	keyDevice          = 4
	keySequence        = 5
	keyPrevious        = 6
	keySchema          = 7
	keyCreatedAt       = 8
	keyLogicalClock    = 9
	keyProducedBy      = 10
	keyHumanApproved   = 11
	keySourceTerminal  = 12
	keyPayloadEncoding = 13
	keyPayload         = 14
	keyPriority        = 15
	keyExpiresAt       = 16
	keyMaxForwards     = 17
	keySignature       = 18
)

// Authorship marks who or what produced an event (plan §10.2; ADR-008).
type Authorship uint8

const (
	AuthorshipUnknown Authorship = iota
	AuthorshipHuman
	AuthorshipHumanWithAI
	AuthorshipAIAgent
	AuthorshipDeterministicBot
	AuthorshipSensor
	AuthorshipImported
)

func (a Authorship) String() string {
	switch a {
	case AuthorshipHuman:
		return "human"
	case AuthorshipHumanWithAI:
		return "human_with_ai_assistance"
	case AuthorshipAIAgent:
		return "ai_agent"
	case AuthorshipDeterministicBot:
		return "deterministic_bot"
	case AuthorshipSensor:
		return "sensor"
	case AuthorshipImported:
		return "imported"
	default:
		return "unknown"
	}
}

// Priority lanes (plan §17.4). Lower value = higher priority.
type Priority uint8

const (
	PrioritySecurity Priority = iota + 1
	PriorityEmergency
	PriorityCommand
	PriorityMessage // default lane
	PriorityStatePatch
	PriorityTelemetry
	PriorityManifest
	PriorityBlob
)

// PayloadEncoding declares how the payload bytes are wrapped.
type PayloadEncoding uint8

const (
	// PayloadCBOR is plaintext canonical CBOR. Used by M0; a terminal whose
	// manifest declares encrypted_output must not use it.
	PayloadCBOR PayloadEncoding = 1
	// PayloadEncrypted is XChaCha20-Poly1305 ciphertext under an epoch key
	// (ADR-005).
	PayloadEncrypted PayloadEncoding = 2
	// PayloadInstrumentSealed is ciphertext under the INSTRUMENT epoch —
	// the space's second key lineage (QI-0, ADR-025). It exists so an
	// instrument can speak into a private space without ever holding the
	// conversation epoch: telemetry is sealed to a key that members and
	// instruments share and the relay does not, while the human
	// conversation stays sealed to a key no instrument has seen. The
	// envelope names the lineage precisely because two lineages may reuse
	// the same epoch NUMBERS; a decoder must never guess which map to
	// consult. Old builds fall through their encrypted-branch and treat
	// the payload as opaque — tolerated, never fatal (compat-pinned).
	PayloadInstrumentSealed PayloadEncoding = 3
)

// MaxFrameLen bounds a full envelope frame (M0.1 size limits).
const MaxFrameLen = 256 * 1024

// Envelope is the parsed view of a Signal. The wire truth is always the
// canonical frame bytes; this struct exists for construction and for reading
// known fields.
type Envelope struct {
	Version         uint64
	Terminal        id.TerminalID
	Principal       id.PrincipalID
	Device          id.DeviceID
	Sequence        uint64
	Previous        *id.EventID // nil for the first event of a chain
	Schema          string
	CreatedAt       uint64 // unix seconds, advisory only (ADR-004)
	LogicalClock    uint64 // Lamport clock, ordering-authoritative
	ProducedBy      Authorship
	HumanApproved   bool
	SourceTerminal  *id.TerminalID // provenance origin, if relayed/derived
	PayloadEncoding PayloadEncoding
	Payload         []byte
	Priority        Priority
	ExpiresAt       uint64 // unix seconds; 0 = no expiry
	MaxForwards     uint64 // wire key; read via ForwardingScope (ADR-015)
	Signature       []byte // 64 bytes, by the device key
}

// ForwardingScope is the ADR-015 reading of the MaxForwards wire field: an
// AUTHOR-DECLARED forwarding class, never a mutable hop counter — the field
// sits inside the signed map, so no hop may decrement it without breaking
// the signature. Loop prevention comes from seen-caches and split-horizon,
// not from hop budgets.
type ForwardingScope int

const (
	// CustodyAllowed (wire 0, the default): the frame may be stored and
	// forwarded by custody holders (bridges, relay push).
	CustodyAllowed ForwardingScope = iota
	// NoCustody (wire 1): custody holders refuse to store-and-forward this
	// frame — fit for presence and ephemeral telemetry. Direct link
	// delivery (including node→bridge hops that do NOT take custody) is
	// unaffected.
	NoCustody
)

// ForwardingScope reads the envelope's forwarding class. Values ≥2 are
// reserved and treated as CustodyAllowed until a future ADR defines them.
func (e *Envelope) ForwardingScope() ForwardingScope {
	if e.MaxForwards == 1 {
		return NoCustody
	}
	return CustodyAllowed
}

// Expired reports whether the envelope's author-declared expiry has passed.
// Expiry is CUSTODY expiry (ADR-015): ingest always accepts history —
// custody holders use this to refuse storing or spending airtime on the
// frame, never to punch holes in contiguous chains.
func (e *Envelope) Expired(nowUnix uint64) bool {
	return e.ExpiresAt != 0 && nowUnix >= e.ExpiresAt
}

// encodeBody writes the canonical map, with or without the signature entry
// (ADR-003: the signature is computed over the encoding with the signature
// field absent).
func (e *Envelope) encodeBody(withSignature bool) ([]byte, error) {
	if e.Schema == "" {
		return nil, errors.New("signal: schema required")
	}
	if e.Sequence == 0 {
		return nil, errors.New("signal: sequence starts at 1")
	}
	if (e.Sequence == 1) != (e.Previous == nil) {
		return nil, errors.New("signal: previous must be set iff sequence > 1")
	}
	if e.PayloadEncoding == 0 {
		return nil, errors.New("signal: payload encoding required")
	}
	if e.Priority == 0 {
		return nil, errors.New("signal: priority required")
	}
	if withSignature && len(e.Signature) != ed25519.SignatureSize {
		return nil, errors.New("signal: missing signature")
	}

	// Count entries explicitly: absent-when-zero fields keep the canonical
	// form unique (ADR-003).
	n := 0
	if e.Version != 0 {
		n++
	}
	n += 3 // terminal, principal, device
	n++    // sequence
	if e.Previous != nil {
		n++
	}
	n++ // schema
	if e.CreatedAt != 0 {
		n++
	}
	n++ // logical_clock
	if e.ProducedBy != AuthorshipUnknown {
		n++
	}
	if e.HumanApproved {
		n++
	}
	if e.SourceTerminal != nil {
		n++
	}
	n += 2 // payload_encoding, payload
	n++    // priority
	if e.ExpiresAt != 0 {
		n++
	}
	if e.MaxForwards != 0 {
		n++
	}
	if withSignature {
		n++
	}

	buf := make([]byte, 0, 128+len(e.Payload))
	buf = codec.AppendMap(buf, n)
	if e.Version != 0 {
		buf = codec.AppendUint(buf, keyVersion)
		buf = codec.AppendUint(buf, e.Version)
	}
	buf = codec.AppendUint(buf, keyTerminal)
	buf = codec.AppendBytes(buf, e.Terminal[:])
	buf = codec.AppendUint(buf, keyPrincipal)
	buf = codec.AppendBytes(buf, e.Principal[:])
	buf = codec.AppendUint(buf, keyDevice)
	buf = codec.AppendBytes(buf, e.Device[:])
	buf = codec.AppendUint(buf, keySequence)
	buf = codec.AppendUint(buf, e.Sequence)
	if e.Previous != nil {
		buf = codec.AppendUint(buf, keyPrevious)
		buf = codec.AppendBytes(buf, e.Previous[:])
	}
	buf = codec.AppendUint(buf, keySchema)
	buf = codec.AppendText(buf, e.Schema)
	if e.CreatedAt != 0 {
		buf = codec.AppendUint(buf, keyCreatedAt)
		buf = codec.AppendUint(buf, e.CreatedAt)
	}
	buf = codec.AppendUint(buf, keyLogicalClock)
	buf = codec.AppendUint(buf, e.LogicalClock)
	if e.ProducedBy != AuthorshipUnknown {
		buf = codec.AppendUint(buf, keyProducedBy)
		buf = codec.AppendUint(buf, uint64(e.ProducedBy))
	}
	if e.HumanApproved {
		buf = codec.AppendUint(buf, keyHumanApproved)
		buf = codec.AppendBool(buf, true)
	}
	if e.SourceTerminal != nil {
		buf = codec.AppendUint(buf, keySourceTerminal)
		buf = codec.AppendBytes(buf, e.SourceTerminal[:])
	}
	buf = codec.AppendUint(buf, keyPayloadEncoding)
	buf = codec.AppendUint(buf, uint64(e.PayloadEncoding))
	buf = codec.AppendUint(buf, keyPayload)
	buf = codec.AppendBytes(buf, e.Payload)
	buf = codec.AppendUint(buf, keyPriority)
	buf = codec.AppendUint(buf, uint64(e.Priority))
	if e.ExpiresAt != 0 {
		buf = codec.AppendUint(buf, keyExpiresAt)
		buf = codec.AppendUint(buf, e.ExpiresAt)
	}
	if e.MaxForwards != 0 {
		buf = codec.AppendUint(buf, keyMaxForwards)
		buf = codec.AppendUint(buf, e.MaxForwards)
	}
	if withSignature {
		buf = codec.AppendUint(buf, keySignature)
		buf = codec.AppendBytes(buf, e.Signature)
	}
	return buf, nil
}

// SigningBytes returns the canonical encoding the device key signs.
func (e *Envelope) SigningBytes() ([]byte, error) { return e.encodeBody(false) }

// Sign signs the envelope with the author's device private key and returns
// the full canonical frame. The device public key must equal e.Device.
func (e *Envelope) Sign(devicePriv ed25519.PrivateKey) ([]byte, error) {
	pub := devicePriv.Public().(ed25519.PublicKey)
	if !bytes.Equal(pub, e.Device[:]) {
		return nil, errors.New("signal: signing key does not match device id")
	}
	body, err := e.SigningBytes()
	if err != nil {
		return nil, err
	}
	e.Signature = ed25519.Sign(devicePriv, body)
	return e.encodeBody(true)
}

// Decode parses a frame strictly, skipping unknown keys (must-ignore,
// ADR-009). It validates structure only; use VerifyFrame for authenticity.
func Decode(frame []byte) (*Envelope, error) {
	if len(frame) > MaxFrameLen {
		return nil, fmt.Errorf("signal: frame of %d exceeds %d", len(frame), MaxFrameLen)
	}
	d := codec.NewDecoder(frame)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	e := &Envelope{}
	var seen struct {
		terminal, principal, device, sequence, schema, clock, encoding, payload, priority, signature bool
	}
	read32 := func(dst []byte) error {
		b, err := d.ReadBytes()
		if err != nil {
			return err
		}
		if len(b) != id.Size {
			return fmt.Errorf("signal: id field must be %d bytes, got %d", id.Size, len(b))
		}
		copy(dst, b)
		return nil
	}
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case keyVersion:
			e.Version, err = d.ReadUint()
		case keyTerminal:
			err = read32(e.Terminal[:])
			seen.terminal = true
		case keyPrincipal:
			err = read32(e.Principal[:])
			seen.principal = true
		case keyDevice:
			err = read32(e.Device[:])
			seen.device = true
		case keySequence:
			e.Sequence, err = d.ReadUint()
			seen.sequence = true
		case keyPrevious:
			var prev id.EventID
			err = read32(prev[:])
			e.Previous = &prev
		case keySchema:
			e.Schema, err = d.ReadText()
			seen.schema = true
		case keyCreatedAt:
			e.CreatedAt, err = d.ReadUint()
		case keyLogicalClock:
			e.LogicalClock, err = d.ReadUint()
			seen.clock = true
		case keyProducedBy:
			var v uint64
			v, err = d.ReadUint()
			if v > uint64(AuthorshipImported) {
				// Unknown authorship values degrade to unknown, never to a
				// stronger claim (invariant §2.4).
				v = uint64(AuthorshipUnknown)
			}
			e.ProducedBy = Authorship(v)
		case keyHumanApproved:
			e.HumanApproved, err = d.ReadBool()
		case keySourceTerminal:
			var src id.TerminalID
			err = read32(src[:])
			e.SourceTerminal = &src
		case keyPayloadEncoding:
			var v uint64
			v, err = d.ReadUint()
			e.PayloadEncoding = PayloadEncoding(v)
			seen.encoding = true
		case keyPayload:
			var b []byte
			b, err = d.ReadBytes()
			e.Payload = b
			seen.payload = true
		case keyPriority:
			var v uint64
			v, err = d.ReadUint()
			e.Priority = Priority(v)
			seen.priority = true
		case keyExpiresAt:
			e.ExpiresAt, err = d.ReadUint()
		case keyMaxForwards:
			e.MaxForwards, err = d.ReadUint()
		case keySignature:
			var b []byte
			b, err = d.ReadBytes()
			if err == nil && len(b) != ed25519.SignatureSize {
				return nil, errors.New("signal: signature must be 64 bytes")
			}
			e.Signature = b
			seen.signature = true
		default:
			err = d.SkipItem()
		}
		if err != nil {
			return nil, err
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	switch {
	case !seen.terminal, !seen.principal, !seen.device:
		return nil, errors.New("signal: missing author fields")
	case !seen.sequence, !seen.clock:
		return nil, errors.New("signal: missing chain fields")
	case !seen.schema, !seen.encoding, !seen.payload, !seen.priority:
		return nil, errors.New("signal: missing payload fields")
	case !seen.signature:
		return nil, errors.New("signal: missing signature")
	}
	if e.Sequence == 0 {
		return nil, errors.New("signal: sequence starts at 1")
	}
	if (e.Sequence == 1) != (e.Previous == nil) {
		return nil, errors.New("signal: previous must be set iff sequence > 1")
	}
	return e, nil
}

// SignedBytes reconstructs, from a received frame, exactly the bytes the
// author signed: the same map with the signature entry spliced out and the
// header count decremented. Unknown fields are preserved byte-for-byte, so
// verification works across schema evolution (ADR-003, ADR-009).
func SignedBytes(frame []byte) ([]byte, error) {
	d := codec.NewDecoder(frame)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	type entry struct{ key, val []byte }
	entries := make([]entry, 0, m.Len())
	sigSeen := false
	for {
		keyStart := d.Pos()
		k, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		keyBytes := frame[keyStart:d.Pos()]
		val, err := d.ReadRawItem()
		if err != nil {
			return nil, err
		}
		if k == keySignature {
			sigSeen = true
			continue
		}
		entries = append(entries, entry{keyBytes, val})
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if !sigSeen {
		return nil, errors.New("signal: frame has no signature entry")
	}
	out := codec.AppendMap(make([]byte, 0, len(frame)), len(entries))
	for _, e := range entries {
		out = append(out, e.key...)
		out = append(out, e.val...)
	}
	return out, nil
}

// VerifyFrame checks the device signature over a received frame. The caller
// must separately check that the device is certified by the principal and
// not revoked (kernel/identity).
func VerifyFrame(frame []byte, e *Envelope) error {
	body, err := SignedBytes(frame)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(e.Device[:]), body, e.Signature) {
		return errors.New("signal: invalid signature")
	}
	return nil
}

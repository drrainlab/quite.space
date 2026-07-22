// Package manifest implements the Terminal Manifest (engineering plan §4):
// a signed, versioned interaction contract. Manifests are signed by the
// terminal key (ADR-001); revision chains are hash-linked like events.
package manifest

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/drrainlab/quiet_places/protocol/capability"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// Kind is the primary UI projection hint (ADR-001: grants nothing).
type Kind uint8

const (
	KindUnknown Kind = iota
	KindHuman
	KindSpace
	KindBot
	KindAgent
	KindSensor
	KindActuator
	KindGateway
	KindRelay
	KindArchive
)

func (k Kind) String() string {
	names := []string{"unknown", "human", "space", "bot", "agent", "sensor",
		"actuator", "gateway", "relay", "archive"}
	if int(k) < len(names) {
		return names[k]
	}
	return "unknown"
}

// IOMode is the data-plane summary (plan §6.1). Real permissions are the
// atomic capability list; IOMode exists for projection and cross-checks.
type IOMode uint8

const (
	IOUnknown IOMode = iota
	IOSourceOnly
	IOSinkOnly
	IODuplex
	IORelayOnly
	IOArchiveOnly
	IOSilentObserver
	IOOfflineExporter
)

func (m IOMode) String() string {
	names := []string{"unknown", "source_only", "sink_only", "duplex",
		"relay_only", "archive_only", "silent_observer", "offline_exporter"}
	if int(m) < len(names) {
		return names[m]
	}
	return "unknown"
}

// AgencyMode declares who drives the terminal (plan §4, §10).
type AgencyMode uint8

const (
	AgencyUnknown AgencyMode = iota
	AgencyHuman
	AgencyDeterministic
	AgencyAIAgent
)

// Autonomy levels A0–A4 (plan §10.1). A4 is excluded from MVP.
type Autonomy uint8

const (
	AutonomyNone Autonomy = iota
	AutonomyAssistive
	AutonomyDelegated
	AutonomyAutonomous
	AutonomyPhysicalControl // rejected by validation in v0
)

// Wire key table v0 (append-only, ADR-009).
const (
	keyProtocolVersion = 1
	keyTerminal        = 2
	keyController      = 3
	keyKind            = 4
	keyDeclaredLabels  = 5
	keyIOMode          = 6
	keyCapabilities    = 7
	keyPublishes       = 8
	keyAccepts         = 9
	keyCommands        = 10
	keyQueries         = 11
	keyAgencyMode      = 12
	keyAIPresent       = 13
	keyAutonomy        = 14
	keyRetentionSecs   = 15
	keyAnnounceTTL     = 16
	keyEncryptedOutput = 17
	keyRevision        = 18
	keyPrevious        = 19
	keySignature       = 20
)

// MaxLabels and MaxSchemas bound manifest size for low-bandwidth announce.
const (
	MaxLabels  = 32
	MaxSchemas = 64
)

// Manifest is the parsed view; the wire truth is the canonical frame.
type Manifest struct {
	ProtocolVersion uint64
	Terminal        id.TerminalID
	Controller      id.PrincipalID
	Kind            Kind
	DeclaredLabels  []string
	IOMode          IOMode
	Capabilities    []string // atomic capability names (plan §6.2)
	Publishes       []string // payload schema ids this terminal emits
	Accepts         []string // payload schema ids this terminal consumes
	Commands        []string // command schema ids it executes
	Queries         []string // query schema ids it answers
	AgencyMode      AgencyMode
	AIPresent       bool
	Autonomy        Autonomy
	RetentionSecs   uint64 // 0 = participant_defined
	AnnounceTTL     uint64 // seconds; presence honesty (plan §8.3)
	EncryptedOutput bool
	Revision        uint64
	Previous        *id.Hash // hash of the previous manifest frame
	Signature       []byte   // 64 bytes, by the terminal key
}

func appendTextArray(buf []byte, key uint64, items []string) []byte {
	if len(items) == 0 {
		return buf
	}
	buf = codec.AppendUint(buf, key)
	buf = codec.AppendArray(buf, len(items))
	for _, s := range items {
		buf = codec.AppendText(buf, s)
	}
	return buf
}

func (m *Manifest) encodeBody(withSignature bool) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if withSignature && len(m.Signature) != ed25519.SignatureSize {
		return nil, errors.New("manifest: missing signature")
	}
	n := 0
	if m.ProtocolVersion != 0 {
		n++
	}
	n += 2 // terminal, controller
	if m.Kind != KindUnknown {
		n++
	}
	if len(m.DeclaredLabels) > 0 {
		n++
	}
	if m.IOMode != IOUnknown {
		n++
	}
	if len(m.Capabilities) > 0 {
		n++
	}
	if len(m.Publishes) > 0 {
		n++
	}
	if len(m.Accepts) > 0 {
		n++
	}
	if len(m.Commands) > 0 {
		n++
	}
	if len(m.Queries) > 0 {
		n++
	}
	if m.AgencyMode != AgencyUnknown {
		n++
	}
	if m.AIPresent {
		n++
	}
	if m.Autonomy != AutonomyNone {
		n++
	}
	if m.RetentionSecs != 0 {
		n++
	}
	if m.AnnounceTTL != 0 {
		n++
	}
	if m.EncryptedOutput {
		n++
	}
	n++ // revision
	if m.Previous != nil {
		n++
	}
	if withSignature {
		n++
	}

	buf := make([]byte, 0, 256)
	buf = codec.AppendMap(buf, n)
	if m.ProtocolVersion != 0 {
		buf = codec.AppendUint(buf, keyProtocolVersion)
		buf = codec.AppendUint(buf, m.ProtocolVersion)
	}
	buf = codec.AppendUint(buf, keyTerminal)
	buf = codec.AppendBytes(buf, m.Terminal[:])
	buf = codec.AppendUint(buf, keyController)
	buf = codec.AppendBytes(buf, m.Controller[:])
	if m.Kind != KindUnknown {
		buf = codec.AppendUint(buf, keyKind)
		buf = codec.AppendUint(buf, uint64(m.Kind))
	}
	buf = appendTextArray(buf, keyDeclaredLabels, m.DeclaredLabels)
	if m.IOMode != IOUnknown {
		buf = codec.AppendUint(buf, keyIOMode)
		buf = codec.AppendUint(buf, uint64(m.IOMode))
	}
	buf = appendTextArray(buf, keyCapabilities, m.Capabilities)
	buf = appendTextArray(buf, keyPublishes, m.Publishes)
	buf = appendTextArray(buf, keyAccepts, m.Accepts)
	buf = appendTextArray(buf, keyCommands, m.Commands)
	buf = appendTextArray(buf, keyQueries, m.Queries)
	if m.AgencyMode != AgencyUnknown {
		buf = codec.AppendUint(buf, keyAgencyMode)
		buf = codec.AppendUint(buf, uint64(m.AgencyMode))
	}
	if m.AIPresent {
		buf = codec.AppendUint(buf, keyAIPresent)
		buf = codec.AppendBool(buf, true)
	}
	if m.Autonomy != AutonomyNone {
		buf = codec.AppendUint(buf, keyAutonomy)
		buf = codec.AppendUint(buf, uint64(m.Autonomy))
	}
	if m.RetentionSecs != 0 {
		buf = codec.AppendUint(buf, keyRetentionSecs)
		buf = codec.AppendUint(buf, m.RetentionSecs)
	}
	if m.AnnounceTTL != 0 {
		buf = codec.AppendUint(buf, keyAnnounceTTL)
		buf = codec.AppendUint(buf, m.AnnounceTTL)
	}
	if m.EncryptedOutput {
		buf = codec.AppendUint(buf, keyEncryptedOutput)
		buf = codec.AppendBool(buf, true)
	}
	buf = codec.AppendUint(buf, keyRevision)
	buf = codec.AppendUint(buf, m.Revision)
	if m.Previous != nil {
		buf = codec.AppendUint(buf, keyPrevious)
		buf = codec.AppendBytes(buf, m.Previous[:])
	}
	if withSignature {
		buf = codec.AppendUint(buf, keySignature)
		buf = codec.AppendBytes(buf, m.Signature)
	}
	return buf, nil
}

// Validate enforces structural rules that hold for every manifest.
func (m *Manifest) Validate() error {
	if m.Revision == 0 {
		return errors.New("manifest: revision starts at 1")
	}
	if (m.Revision == 1) != (m.Previous == nil) {
		return errors.New("manifest: previous must be set iff revision > 1")
	}
	if len(m.DeclaredLabels) > MaxLabels {
		return fmt.Errorf("manifest: more than %d labels", MaxLabels)
	}
	for _, lists := range [][]string{m.Publishes, m.Accepts, m.Commands, m.Queries} {
		if len(lists) > MaxSchemas {
			return fmt.Errorf("manifest: more than %d schema ids", MaxSchemas)
		}
	}
	for _, c := range m.Capabilities {
		if !capability.Known(c) {
			return fmt.Errorf("manifest: unknown capability %q", c)
		}
	}
	if m.Autonomy >= AutonomyPhysicalControl {
		return errors.New("manifest: autonomy A4 (physical_control) is not accepted in v0")
	}
	// Cross-checks: the IOMode summary must not promise more than the
	// capability list (capabilities before assumptions, invariant §2.2).
	caps := capability.NewSet(m.Capabilities...)
	if m.IOMode == IOSourceOnly {
		if caps.Has(capability.SignalReceive) || caps.Has(capability.CommandReceive) ||
			caps.Has(capability.CommandExecute) || len(m.Accepts) > 0 || len(m.Commands) > 0 {
			return errors.New("manifest: source_only terminal declares receive/command surface")
		}
	}
	if len(m.Commands) > 0 && !caps.Has(capability.CommandReceive) {
		return errors.New("manifest: command schemas without command.receive capability")
	}
	if m.AgencyMode == AgencyAIAgent && !m.AIPresent {
		return errors.New("manifest: ai_agent agency requires ai_present (plan §10)")
	}
	return nil
}

// SigningBytes returns the canonical encoding the terminal key signs.
func (m *Manifest) SigningBytes() ([]byte, error) { return m.encodeBody(false) }

// Sign signs with the terminal private key and returns the canonical frame.
func (m *Manifest) Sign(terminalPriv ed25519.PrivateKey) ([]byte, error) {
	pub := terminalPriv.Public().(ed25519.PublicKey)
	if !bytes.Equal(pub, m.Terminal[:]) {
		return nil, errors.New("manifest: signing key does not match terminal id")
	}
	body, err := m.SigningBytes()
	if err != nil {
		return nil, err
	}
	m.Signature = ed25519.Sign(terminalPriv, body)
	return m.encodeBody(true)
}

// Hash returns the manifest frame hash used to link revisions.
func Hash(frame []byte) id.Hash { return id.HashOf(frame) }

// Decode parses a manifest frame, skipping unknown keys (ADR-009).
func Decode(frame []byte) (*Manifest, error) {
	d := codec.NewDecoder(frame)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	m := &Manifest{}
	var seenTerminal, seenController, seenRevision, seenSig bool
	read32 := func(dst []byte) error {
		b, err := d.ReadBytes()
		if err != nil {
			return err
		}
		if len(b) != id.Size {
			return fmt.Errorf("manifest: id field must be %d bytes", id.Size)
		}
		copy(dst, b)
		return nil
	}
	readTexts := func() ([]string, error) {
		n, err := d.ReadArray()
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, n)
		for range n {
			s, err := d.ReadText()
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, nil
	}
	for {
		k, ok, err := mr.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case keyProtocolVersion:
			m.ProtocolVersion, err = d.ReadUint()
		case keyTerminal:
			err = read32(m.Terminal[:])
			seenTerminal = true
		case keyController:
			err = read32(m.Controller[:])
			seenController = true
		case keyKind:
			var v uint64
			v, err = d.ReadUint()
			if v > uint64(KindArchive) {
				v = uint64(KindUnknown)
			}
			m.Kind = Kind(v)
		case keyDeclaredLabels:
			m.DeclaredLabels, err = readTexts()
		case keyIOMode:
			var v uint64
			v, err = d.ReadUint()
			if v > uint64(IOOfflineExporter) {
				v = uint64(IOUnknown)
			}
			m.IOMode = IOMode(v)
		case keyCapabilities:
			m.Capabilities, err = readTexts()
		case keyPublishes:
			m.Publishes, err = readTexts()
		case keyAccepts:
			m.Accepts, err = readTexts()
		case keyCommands:
			m.Commands, err = readTexts()
		case keyQueries:
			m.Queries, err = readTexts()
		case keyAgencyMode:
			var v uint64
			v, err = d.ReadUint()
			m.AgencyMode = AgencyMode(v)
		case keyAIPresent:
			m.AIPresent, err = d.ReadBool()
		case keyAutonomy:
			var v uint64
			v, err = d.ReadUint()
			m.Autonomy = Autonomy(v)
		case keyRetentionSecs:
			m.RetentionSecs, err = d.ReadUint()
		case keyAnnounceTTL:
			m.AnnounceTTL, err = d.ReadUint()
		case keyEncryptedOutput:
			m.EncryptedOutput, err = d.ReadBool()
		case keyRevision:
			m.Revision, err = d.ReadUint()
			seenRevision = true
		case keyPrevious:
			var h id.Hash
			err = read32(h[:])
			m.Previous = &h
		case keySignature:
			var b []byte
			b, err = d.ReadBytes()
			if err == nil && len(b) != ed25519.SignatureSize {
				return nil, errors.New("manifest: signature must be 64 bytes")
			}
			m.Signature = b
			seenSig = true
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
	if !seenTerminal || !seenController || !seenRevision || !seenSig {
		return nil, errors.New("manifest: missing required fields")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// SignedBytes splices the signature entry out of a received frame (same
// verbatim rule as signal.SignedBytes, ADR-003).
func SignedBytes(frame []byte) ([]byte, error) {
	d := codec.NewDecoder(frame)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	type entry struct{ key, val []byte }
	entries := make([]entry, 0, mr.Len())
	sigSeen := false
	for {
		keyStart := d.Pos()
		k, ok, err := mr.Next()
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
		return nil, errors.New("manifest: frame has no signature entry")
	}
	out := codec.AppendMap(make([]byte, 0, len(frame)), len(entries))
	for _, e := range entries {
		out = append(out, e.key...)
		out = append(out, e.val...)
	}
	return out, nil
}

// VerifyFrame checks the terminal-key signature over a received frame.
func VerifyFrame(frame []byte, m *Manifest) error {
	body, err := SignedBytes(frame)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(m.Terminal[:]), body, m.Signature) {
		return errors.New("manifest: invalid signature")
	}
	return nil
}

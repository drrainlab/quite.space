// Pinned custodians (TN-B, ADR-015 §7): a node honors a bridge's signed
// custody ACK only when the custodian public key is PINNED for the link
// domain the receipt arrived on. TOFU is forbidden by default; rotation is
// an explicit pin update. A valid signature under an unpinned key records
// nothing (a local unconfirmed observation at most).
package node

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/bridge"
)

// custodianFile holds the pins beside the encrypted store. Public keys are
// not secrets, and keeping them in a plain file is deliberate: a trust
// decision someone cannot read back is a trust decision they cannot audit.
const custodianFile = "custodians.pins"

// CustodianPin is one trusted gateway key, as shown to a person.
type CustodianPin struct {
	LinkDomain string
	Key        ed25519.PublicKey
	Label      string
	PinnedAt   time.Time
	// Replaced is the fingerprint of the key this pin displaced, if any.
	// Rotation in the beta is manual, and a trust decision that changes with
	// no record is indistinguishable from one that changed by itself.
	Replaced string
}

// Fingerprint is the short form to show and to compare by eye.
func (p CustodianPin) Fingerprint() string { return fingerprintOf(p.Key) }

func fingerprintOf(pub ed25519.PublicKey) string {
	if len(pub) == 0 {
		return ""
	}
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:4])
}

// PinCustodian trusts a custodian public key for a link domain label
// (e.g. "lan", "radio", "relay:host"). Caller supplies the key out-of-band
// (the bridge prints it at startup). The pin is written to disk before this
// returns: a pin that survives only until the next restart is a pin that
// quietly stops being honoured, and the symptom — messages that never leave
// "sending" — points nowhere near the cause.
func (r *Runtime) PinCustodian(linkDomain string, pub ed25519.PublicKey) error {
	return r.pinCustodian(linkDomain, pub, "")
}

// PinCustodianLabelled pins with a human label ("beta gateway on the roof").
func (r *Runtime) PinCustodianLabelled(linkDomain string, pub ed25519.PublicKey,
	label string) error {
	return r.pinCustodian(linkDomain, pub, label)
}

func (r *Runtime) pinCustodian(linkDomain string, pub ed25519.PublicKey,
	label string) error {

	if len(pub) != ed25519.PublicKeySize {
		return errors.New("node: custodian key must be 32 bytes")
	}
	if strings.TrimSpace(linkDomain) == "" {
		return errors.New("node: a pin needs a link domain (radio, lan, relay:HOST)")
	}
	if strings.ContainsAny(linkDomain, " \t\n") {
		return errors.New("node: a link domain cannot contain spaces")
	}
	r.mu.Lock()
	if r.custodians == nil {
		r.custodians = map[string][]byte{}
	}
	if r.custodianPins == nil {
		r.custodianPins = map[string]CustodianPin{}
	}
	pin := CustodianPin{
		LinkDomain: linkDomain,
		Key:        append(ed25519.PublicKey(nil), pub...),
		Label:      label,
		PinnedAt:   time.Now(),
	}
	if old, ok := r.custodianPins[linkDomain]; ok && !bytes.Equal(old.Key, pub) {
		pin.Replaced = fingerprintOf(old.Key)
		if label == "" {
			pin.Label = old.Label
		}
	}
	r.custodians[linkDomain] = append([]byte(nil), pub...)
	r.custodianPins[linkDomain] = pin
	err := r.saveCustodiansLocked()
	r.mu.Unlock()
	return err
}

// UnpinCustodian withdraws trust from a gateway.
func (r *Runtime) UnpinCustodian(linkDomain string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.custodians, linkDomain)
	delete(r.custodianPins, linkDomain)
	return r.saveCustodiansLocked()
}

// Custodians lists the pinned gateways, sorted by link domain.
func (r *Runtime) Custodians() []CustodianPin {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]CustodianPin, 0, len(r.custodianPins))
	for _, p := range r.custodianPins {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LinkDomain < out[j].LinkDomain })
	return out
}

// CustodianWarnings reports pins that could not be read at startup. An
// unreadable pin is NOT trusted — trust fails closed — but it must also be
// visible, or the node silently becomes one that trusts no gateway.
func (r *Runtime) CustodianWarnings() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.custodianWarn...)
}

func (r *Runtime) custodianPath() string {
	return CustodianPath(r.dataDir)
}

// CustodianPath is where a node keeps its pins. Exported so a CLI can manage
// trust WITHOUT the passphrase and without stopping the node: the file holds
// public keys only, so there is nothing in it the store needs to protect.
func CustodianPath(dataDir string) string {
	return filepath.Join(dataDir, custodianFile)
}

// LoadCustodianFile reads pins from disk. Lines it cannot parse become
// warnings and are NOT trusted — trust fails closed.
func LoadCustodianFile(path string) ([]CustodianPin, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var pins []CustodianPin
	var warn []string
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pin, err := parseCustodianLine(line)
		if err != nil {
			warn = append(warn, fmt.Sprintf("line %d (%s): %v — that gateway "+
				"is NOT trusted", n+1, firstField(line), err))
			continue
		}
		pins = append(pins, pin)
	}
	sort.Slice(pins, func(i, j int) bool { return pins[i].LinkDomain < pins[j].LinkDomain })
	return pins, warn, nil
}

// SaveCustodianFile writes pins atomically. A half-written file on the next
// boot would silently unpin a gateway, and "silently" is the whole problem
// with losing a pin.
func SaveCustodianFile(path string, pins []CustodianPin) error {
	sort.Slice(pins, func(i, j int) bool { return pins[i].LinkDomain < pins[j].LinkDomain })
	var b strings.Builder
	b.WriteString("# Quiet Spaces custodian pins (ADR-015 §7).\n" +
		"# One trusted gateway key per link domain. TOFU is forbidden: a\n" +
		"# signature under a key that is not here proves nothing, no matter\n" +
		"# how valid it is. These are PUBLIC keys — safe to read, safe to\n" +
		"# copy, and readable on purpose so trust can be audited by eye.\n" +
		"#\n" +
		"# <link-domain> <public key hex> [pinned=<unix>] [replaced=<fp>] [label=...]\n")
	for _, p := range pins {
		fmt.Fprintf(&b, "%s %x pinned=%d", p.LinkDomain, p.Key, p.PinnedAt.Unix())
		if p.Replaced != "" {
			fmt.Fprintf(&b, " replaced=%s", p.Replaced)
		}
		if p.Label != "" {
			fmt.Fprintf(&b, " label=%s", strings.ReplaceAll(p.Label, "\n", " "))
		}
		b.WriteString("\n")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// saveCustodiansLocked writes the pins atomically. Caller holds r.mu.
func (r *Runtime) saveCustodiansLocked() error {
	pins := make([]CustodianPin, 0, len(r.custodianPins))
	for _, p := range r.custodianPins {
		pins = append(pins, p)
	}
	return SaveCustodianFile(r.custodianPath(), pins)
}

// loadCustodians restores the pins at startup. Caller holds no lock.
func (r *Runtime) loadCustodians() {
	pins, warn, err := LoadCustodianFile(r.custodianPath())
	if err != nil {
		r.mu.Lock()
		r.custodianWarn = append(r.custodianWarn,
			"custodian pins could not be read ("+err.Error()+
				"): no gateway is trusted until this is fixed")
		r.mu.Unlock()
		return
	}
	byDomain := make(map[string]CustodianPin, len(pins))
	keys := make(map[string][]byte, len(pins))
	for _, p := range pins {
		byDomain[p.LinkDomain] = p
		keys[p.LinkDomain] = append([]byte(nil), p.Key...)
	}
	r.mu.Lock()
	r.custodianPins = byDomain
	if r.custodians == nil {
		r.custodians = map[string][]byte{}
	}
	maps.Copy(r.custodians, keys)
	r.custodianWarn = append(r.custodianWarn, warn...)
	r.mu.Unlock()
}

// ReloadCustodians re-reads the pin file, so trust edited from the CLI while
// the node runs takes effect without a restart.
func (r *Runtime) ReloadCustodians() {
	r.mu.Lock()
	r.custodians = map[string][]byte{}
	r.custodianPins = nil
	r.custodianWarn = nil
	r.mu.Unlock()
	r.loadCustodians()
}

func firstField(line string) string {
	if i := strings.IndexAny(line, " \t"); i > 0 {
		return line[:i]
	}
	return line
}

func parseCustodianLine(line string) (CustodianPin, error) {
	// A label is a human phrase — "roof gateway", "Henk's Pi" — so it takes
	// the rest of the line. Splitting it on spaces like the other fields
	// silently truncated it to its first word.
	label := ""
	if i := strings.Index(line, " label="); i >= 0 {
		label = strings.TrimSpace(line[i+len(" label="):])
		line = line[:i]
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return CustodianPin{}, errors.New("expected `<link-domain> <public key hex>`")
	}
	raw, err := hex.DecodeString(fields[1])
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return CustodianPin{}, errors.New("not a 32-byte hex public key")
	}
	pin := CustodianPin{LinkDomain: fields[0], Key: ed25519.PublicKey(raw), Label: label}
	for _, f := range fields[2:] {
		key, value, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		switch key {
		case "pinned":
			if sec, err := strconv.ParseInt(value, 10, 64); err == nil {
				pin.PinnedAt = time.Unix(sec, 0)
			}
		case "replaced":
			pin.Replaced = value
		}
	}
	if pin.PinnedAt.IsZero() {
		pin.PinnedAt = time.Unix(1, 0) // present but unknown
	}
	return pin, nil
}

// handleCustodyReceipt verifies a receipt against the pin for the link it
// arrived on and records accepted_by_relay for the covered events. Caller
// holds r.mu (invoked from the engine callback under the pump lock).
func (r *Runtime) handleCustodyReceipt(tid id.TerminalID, raw []byte) {
	st, ok := r.spaces[tid]
	if !ok {
		return
	}
	rec, err := bridge.DecodeReceipt(raw)
	if err != nil {
		return // bad signature: nothing recorded
	}
	pin, ok := r.custodians[r.curLink]
	if !ok || !bytes.Equal(pin, rec.PublicKey) {
		return // unpinned key: local observation only, never a claim
	}
	// The ledger is the authority on whether this receipt is about the
	// hand-off currently running. Signature and pin say the receipt is
	// AUTHENTIC; only the attempt token says it is CURRENT, and treating
	// the first as the second is how a stale acknowledgement completes an
	// attempt it never saw.
	now := time.Now()
	for _, eid := range rec.FrameIDs {
		switch r.applyReceiptToLedger(eid, rec, now) {
		case ReceiptAudited, ReceiptUnbound, ReceiptConflicted:
			// Recorded, but it moves nothing: skip the ladder too.
			continue
		}
	}
	if rec.Kind.Lapsed() {
		// The gateway is WITHDRAWING a claim it made earlier: it held these
		// frames and could not keep them to the promised time. The delivery
		// ladder is closed and has no rung for "was carried, then wasn't",
		// so nothing is un-recorded — a receipt that was true when issued
		// stays true. What changes is what the person is shown: this is
		// recorded locally so the UI can stop implying the message is still
		// somewhere on its way, and so the RB-1 delivery ledger can retry.
		for _, eid := range rec.FrameIDs {
			r.noteCustodyLapsed(eid, tid, rec.Instance)
		}
		return
	}
	if rec.ExpiresAt != 0 && uint64(time.Now().Unix()) >= rec.ExpiresAt {
		return
	}
	for _, eid := range rec.FrameIDs {
		_ = st.space.Trust.RecordTransportReceipt(eid, tid, claims.DeliveryAcceptedByRelay)
		r.trackCarried(eid, "bridge")
	}
}

// CustodyLapse is a gateway's withdrawal of a custody claim, kept locally.
type CustodyLapse struct {
	Space    id.TerminalID
	Instance string
	At       time.Time
}

// maxCustodyLapses bounds the local record (diagnostics, not a log).
const maxCustodyLapses = 512

// noteCustodyLapsed records a withdrawal. Caller holds r.mu.
func (r *Runtime) noteCustodyLapsed(eid id.EventID, tid id.TerminalID, instance string) {
	if r.custodyLapses == nil {
		r.custodyLapses = map[id.EventID]CustodyLapse{}
	}
	if len(r.custodyLapses) >= maxCustodyLapses {
		for k := range r.custodyLapses {
			delete(r.custodyLapses, k)
			break
		}
	}
	r.custodyLapses[eid] = CustodyLapse{Space: tid, Instance: instance, At: time.Now()}
}

// CustodyLapsed reports whether a gateway withdrew custody of an event.
func (r *Runtime) CustodyLapsed(eid id.EventID) (CustodyLapse, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.custodyLapses[eid]
	return l, ok
}

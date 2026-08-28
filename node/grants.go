package node

// SPACE GRANTS — ADR-024, mechanism 2: the identity plane that carries a
// membership from the device that acquired it to the person's other
// certified devices, WITHOUT passing through the new space (a device
// cannot receive an event inside a space it does not know exists — the
// bootstrap circle is real).
//
// The shape is the freight continued by other means, sibling to sibling:
//
//	phone joins X → grant = (meta + manifest + epochs), signed by the
//	granting DEVICE, HPKE-sealed to the sibling's certified X25519 →
//	put into the sibling's identity mailbox on the sibling's own relay →
//	the sibling fetches, verifies same-principal + unrevoked, installs,
//	and begins ordinary space sync — which publishes its certificate
//	into X, and THAT is the acknowledgement.
//
// HELD UNTIL OBSERVED (ADR-023): there is no ack message. The granting
// side keeps offering — the pending set is DERIVED, never stored: a
// grant is owed exactly while a certified, unrevoked sibling has not
// yet been seen authoring in the space. Derivation survives restarts by
// construction, heals pre-existing installs the moment they upgrade,
// and cannot leak state the log does not show. Re-offers are cheap: the
// sealed bytes are cached per (space, sibling) so the relay's
// byte-identical dedup absorbs every repeat within a process lifetime.
//
// What deliberately does NOT happen here: grants are never FORWARDED (a
// sibling re-granting what it was granted would make this gossip);
// device-local state never rides; and a public space rides only its
// meta — it has no epochs to carry.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/drrainlab/quiet_places/kernel/crypto"
	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relay"
)

const (
	grantVersion = 1
	grantFields  = 8
	grantSigCtx  = "qp-space-grant-v0:"
	// identitySetVersion marks the second message the identity plane
	// carries: the person's certified set itself — every certificate and
	// revocation this device holds for its own principal. Without it the
	// sibling set converges hub-and-spoke: A pairs B, later A pairs C,
	// and B learns of C only if it happens to share a space with A. The
	// owner's transitivity test names the failure exactly: a "many
	// devices" claim that is really one hub with spokes.
	identitySetVersion = 2
	identitySetSigCtx  = "qp-identity-set-v0:"
	// refusalSetVersion marks the third message this plane carries: the
	// people this person declined to hear from (ADR-027). It rides here
	// for the same reason a space grant does — a relationship belongs to
	// the PERSON (ADR-024), so declining on the phone must silence the
	// laptop. It is the person's own state, sealed to their own devices,
	// and it names nobody to anybody else.
	refusalSetVersion = 3
	refusalSetSigCtx  = "qp-refusal-set-v0:"
)

// spaceGrant is what one sibling tells another about a membership the
// person acquired. Everything in it is the person's own state.
type spaceGrant struct {
	Terminal      id.TerminalID
	Title         string
	Visibility    string
	ManifestFrame []byte
	Epochs        []crypto.EpochKey
	LocalTitle    string
	Unnamed       bool
	Grantor       id.DeviceID
	Signature     []byte
}

func encodeGrantBody(g *spaceGrant) []byte {
	var buf []byte
	buf = codec.AppendArray(buf, grantFields+1)
	buf = codec.AppendUint(buf, grantVersion)
	buf = codec.AppendBytes(buf, g.Terminal[:])
	buf = codec.AppendText(buf, g.Title)
	buf = codec.AppendText(buf, g.Visibility)
	buf = codec.AppendBytes(buf, g.ManifestFrame)
	buf = codec.AppendArray(buf, len(g.Epochs))
	for _, ek := range g.Epochs {
		buf = codec.AppendArray(buf, 2)
		buf = codec.AppendUint(buf, ek.N)
		buf = codec.AppendBytes(buf, ek.Key[:])
	}
	buf = codec.AppendText(buf, g.LocalTitle)
	buf = codec.AppendBool(buf, g.Unnamed)
	buf = codec.AppendBytes(buf, g.Grantor[:])
	return buf
}

func signGrant(g *spaceGrant, key ed25519.PrivateKey) {
	g.Signature = ed25519.Sign(key, append([]byte(grantSigCtx), encodeGrantBody(g)...))
}

func decodeGrant(data []byte) (*spaceGrant, error) {
	bad := errors.New("node: malformed space grant")
	d := codec.NewDecoder(data)
	n, err := d.ReadArray()
	if err != nil || n < grantFields+1 {
		return nil, bad
	}
	v, err := d.ReadUint()
	if err != nil || v != grantVersion {
		return nil, bad
	}
	g := &spaceGrant{}
	raw, err := d.ReadBytes()
	if err != nil || len(raw) != len(g.Terminal) {
		return nil, bad
	}
	copy(g.Terminal[:], raw)
	if g.Title, err = d.ReadText(); err != nil {
		return nil, bad
	}
	if g.Visibility, err = d.ReadText(); err != nil {
		return nil, bad
	}
	if g.ManifestFrame, err = d.ReadBytes(); err != nil {
		return nil, bad
	}
	ec, err := d.ReadArray()
	if err != nil {
		return nil, bad
	}
	for i := 0; i < ec; i++ {
		fc, err := d.ReadArray()
		if err != nil || fc < 2 {
			return nil, bad
		}
		var ek crypto.EpochKey
		if ek.N, err = d.ReadUint(); err != nil {
			return nil, bad
		}
		if raw, err = d.ReadBytes(); err != nil || len(raw) != len(ek.Key) {
			return nil, bad
		}
		copy(ek.Key[:], raw)
		for k := 2; k < fc; k++ {
			if err := d.SkipItem(); err != nil {
				return nil, bad
			}
		}
		g.Epochs = append(g.Epochs, ek)
	}
	if g.LocalTitle, err = d.ReadText(); err != nil {
		return nil, bad
	}
	if g.Unnamed, err = d.ReadBool(); err != nil {
		return nil, bad
	}
	if raw, err = d.ReadBytes(); err != nil || len(raw) != len(g.Grantor) {
		return nil, bad
	}
	copy(g.Grantor[:], raw)
	if g.Signature, err = d.ReadBytes(); err != nil {
		return nil, bad
	}
	for k := grantFields + 1; k < n; k++ {
		if err := d.SkipItem(); err != nil {
			return nil, bad
		}
	}
	return g, nil
}

// identitySet is the certified-set announcement: cert and revocation
// frames for the sender's own principal, signed by the sending device.
type identitySet struct {
	CertFrames [][]byte
	RevFrames  [][]byte
	Grantor    id.DeviceID
	Signature  []byte
}

func encodeIdentitySetBody(is *identitySet) []byte {
	var buf []byte
	buf = codec.AppendArray(buf, 4)
	buf = codec.AppendUint(buf, identitySetVersion)
	buf = codec.AppendArray(buf, len(is.CertFrames))
	for _, f := range is.CertFrames {
		buf = codec.AppendBytes(buf, f)
	}
	buf = codec.AppendArray(buf, len(is.RevFrames))
	for _, f := range is.RevFrames {
		buf = codec.AppendBytes(buf, f)
	}
	buf = codec.AppendBytes(buf, is.Grantor[:])
	return buf
}

func decodeIdentitySet(data []byte) (*identitySet, error) {
	bad := errors.New("node: malformed identity set")
	d := codec.NewDecoder(data)
	n, err := d.ReadArray()
	if err != nil || n < 4 {
		return nil, bad
	}
	v, err := d.ReadUint()
	if err != nil || v != identitySetVersion {
		return nil, bad
	}
	is := &identitySet{}
	cc, err := d.ReadArray()
	if err != nil {
		return nil, bad
	}
	for i := 0; i < cc; i++ {
		f, err := d.ReadBytes()
		if err != nil {
			return nil, bad
		}
		is.CertFrames = append(is.CertFrames, f)
	}
	rc, err := d.ReadArray()
	if err != nil {
		return nil, bad
	}
	for i := 0; i < rc; i++ {
		f, err := d.ReadBytes()
		if err != nil {
			return nil, bad
		}
		is.RevFrames = append(is.RevFrames, f)
	}
	raw, err := d.ReadBytes()
	if err != nil || len(raw) != len(is.Grantor) {
		return nil, bad
	}
	copy(is.Grantor[:], raw)
	if is.Signature, err = d.ReadBytes(); err != nil {
		return nil, bad
	}
	for k := 4; k < n; k++ {
		if err := d.SkipItem(); err != nil {
			return nil, bad
		}
	}
	return is, nil
}

// planeTag is the deterministic identity of one LOGICAL plane message,
// stable across restarts precisely because sealing is not: the sender
// fetches the recipient's mailbox once per process and skips messages
// whose tag already sits there, so a hundred restarts leave one physical
// copy per logical grant instead of a quota's worth. To an observer the
// tag is an opaque hash keyed on ids it does not hold; the only fact it
// leaks — "same logical message repeated" — is exactly what the repeats
// themselves already show.
func planeTag(ctx string, parts ...[]byte) []byte {
	h := sha256.New()
	h.Write([]byte(ctx))
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)[:16]
}

func encodeSignedGrant(g *spaceGrant) []byte {

	return append(encodeGrantBody(g), codec.AppendBytes(nil, g.Signature)...)
}

// refusalSet is the person's standing refusals, travelling to their own
// other devices.
type refusalSet struct {
	Refusals  []storage.RefusalRecord
	Grantor   id.DeviceID
	Signature []byte
}

func encodeRefusalSetBody(rs *refusalSet) []byte {
	var buf []byte
	buf = codec.AppendArray(buf, 3)
	buf = codec.AppendUint(buf, refusalSetVersion)
	buf = codec.AppendArray(buf, len(rs.Refusals))
	for _, rf := range rs.Refusals {
		buf = codec.AppendArray(buf, 3)
		buf = codec.AppendBytes(buf, rf.Principal[:])
		buf = codec.AppendText(buf, rf.Reason)
		buf = codec.AppendUint(buf, uint64(rf.At))
	}
	buf = codec.AppendBytes(buf, rs.Grantor[:])
	return buf
}

func decodeRefusalSet(data []byte) (*refusalSet, error) {
	bad := errors.New("node: malformed refusal set")
	d := codec.NewDecoder(data)
	n, err := d.ReadArray()
	if err != nil || n < 3 {
		return nil, bad
	}
	v, err := d.ReadUint()
	if err != nil || v != refusalSetVersion {
		return nil, bad
	}
	rs := &refusalSet{}
	cnt, err := d.ReadArray()
	if err != nil {
		return nil, bad
	}
	for range cnt {
		fields, err := d.ReadArray()
		if err != nil || fields < 3 {
			return nil, bad
		}
		var rf storage.RefusalRecord
		raw, err := d.ReadBytes()
		if err != nil || len(raw) != len(rf.Principal) {
			return nil, bad
		}
		copy(rf.Principal[:], raw)
		if rf.Reason, err = d.ReadText(); err != nil {
			return nil, bad
		}
		at, err := d.ReadUint()
		if err != nil {
			return nil, bad
		}
		rf.At = int64(at)
		for i := 3; i < fields; i++ {
			if err := d.SkipItem(); err != nil {
				return nil, bad
			}
		}
		rs.Refusals = append(rs.Refusals, rf)
	}
	raw, err := d.ReadBytes()
	if err != nil || len(raw) != len(rs.Grantor) {
		return nil, bad
	}
	copy(rs.Grantor[:], raw)
	if rs.Signature, err = d.ReadBytes(); err != nil {
		return nil, bad
	}
	return rs, nil
}

func (r *Runtime) sealedRefusalSetLocked(dev id.DeviceID) []byte {
	cert, ok := r.ident.certificateFor(dev)
	if !ok {
		return nil
	}
	rs := &refusalSet{
		Refusals: append([]storage.RefusalRecord(nil), r.ks.Refusals...),
		Grantor:  r.Device.ID,
	}
	rs.Signature = ed25519.Sign(r.Device.SignKey(),
		append([]byte(refusalSetSigCtx), encodeRefusalSetBody(rs)...))
	plain := append(encodeRefusalSetBody(rs), codec.AppendBytes(nil, rs.Signature)...)
	enc, ct, err := crypto.SealTo(cert.X25519Pub,
		append([]byte(grantSigCtx), dev[:]...), plain)
	if err != nil {
		return nil
	}
	var sealed []byte
	sealed = codec.AppendArray(sealed, 3)
	sealed = codec.AppendBytes(sealed, enc)
	sealed = codec.AppendBytes(sealed, ct)
	sealed = codec.AppendBytes(sealed, planeTag(refusalSetSigCtx, dev[:],
		encodeRefusalSetBody(rs)))
	return sealed
}

// installRefusalSet merges a sibling's refusals. UNION, never replacement:
// two devices may each have refused somebody while apart, and the person
// meant both. A refusal is only ever lifted by the person saying so.
func (r *Runtime) installRefusalSet(plain []byte) error {
	rs, err := decodeRefusalSet(plain)
	if err != nil {
		return err
	}
	r.mu.Lock()
	cert, ok := r.ident.certificateFor(rs.Grantor)
	r.mu.Unlock()
	if !ok {
		return errors.New("node: refusals from an uncertified device")
	}
	if !bytes.Equal(cert.Principal[:], r.PrincipalID[:]) {
		return errors.New("node: refusals from another principal")
	}
	if !ed25519.Verify(ed25519.PublicKey(rs.Grantor[:]),
		append([]byte(refusalSetSigCtx), encodeRefusalSetBody(rs)...), rs.Signature) {
		return errors.New("node: refusal set signature does not verify")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := false
	for _, in := range rs.Refusals {
		found := false
		for i := range r.ks.Refusals {
			if r.ks.Refusals[i].Principal == in.Principal {
				found = true
				// The later word wins: a person may rewrite what they say.
				if in.At > r.ks.Refusals[i].At {
					r.ks.Refusals[i] = in
					changed = true
				}
			}
		}
		if !found {
			r.ks.Refusals = append(r.ks.Refusals, in)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return r.saveKeystore()
}

// grantState is the in-memory half: which siblings have been OBSERVED in
// which space (derived from the log), and the sealed-bytes cache that
// keeps re-offers byte-identical for the relay's dedup.
type grantState struct {
	seen    map[id.TerminalID]map[id.DeviceID]bool
	scanned map[id.TerminalID]int // log length at last author scan
	sealed  map[id.TerminalID]map[id.DeviceID][]byte
	// setSent: how many (certs+revs) records the identity-set offered to
	// each sibling carried. The set is re-offered when it grows — a new
	// sibling certified, a device revoked — and once per process start
	// (restart re-seals, the mailbox TTL and per-hint quota bound the
	// accumulation; recorded in ADR-024).
	setSent  map[id.DeviceID]int
	setBytes map[id.DeviceID][]byte
	// refusalsSent: how many refusals the set offered to each sibling
	// carried, re-offered when it grows or changes.
	refusalsSent map[id.DeviceID]int
	// tagChecked: siblings whose mailbox this process has already read
	// for logical-message tags (once is enough; the seal cache covers the
	// rest of the process lifetime).
	tagChecked map[id.DeviceID]bool
	refusals   int
	// refused: sealed-envelope hash → refusal reason. The mailbox is
	// non-destructive, so a refused envelope WILL be fetched again every
	// cycle; without memory that is one log line per envelope per two
	// seconds, forever. The bytes are immutable, so most verdicts are
	// too — the one refusal that can heal is "uncertified", because
	// certificate knowledge only grows, and that one is retried.
	refused map[[32]byte]string
	// busy: the plane heartbeat is single-flight. Two sync loops overlap
	// for a moment around every SetSettings, and the tag scan holds no
	// lock across its relay round trip — one rider at a time is the rule
	// that makes the rest of this file single-threaded in practice.
	//
	// ATOMIC, and the release is a plain Store, deliberately: the first
	// version cleared it in a defer that took r.mu — and when a nil-map
	// write panicked UNDER r.mu, the unwind ran that defer, deadlocked
	// on the lock it was dying inside of, and froze the whole runtime
	// with the panic message still unprinted. A defer that runs during
	// unwinding must never need a lock the panicking body may hold.
	busy atomic.Bool
}

// offerItem is one pending plane message bound for one sibling.
type offerItem struct {
	dev  id.DeviceID
	tid  id.TerminalID
	body []byte
	eps  []storage.Route
}

func (r *Runtime) grantsInit() {
	if r.grants == nil {
		r.grants = &grantState{
			seen:         map[id.TerminalID]map[id.DeviceID]bool{},
			scanned:      map[id.TerminalID]int{},
			sealed:       map[id.TerminalID]map[id.DeviceID][]byte{},
			setSent:      map[id.DeviceID]int{},
			refusalsSent: map[id.DeviceID]int{},
			setBytes:     map[id.DeviceID][]byte{},
			tagChecked:   map[id.DeviceID]bool{},
			refused:      map[[32]byte]string{},
		}
	}
}

// offerGrants runs once per sync cycle on the granting side. The pending
// set is derived fresh: certified unrevoked siblings not yet observed
// authoring in a space are owed the space.
func (r *Runtime) offerGrants() {
	r.mu.Lock()
	r.grantsInit()
	if !r.grants.busy.CompareAndSwap(false, true) {
		r.mu.Unlock()
		return
	}
	defer r.grants.busy.Store(false)
	self := r.Device.ID
	// The person's devices, minus the faces of self that are not devices
	// of the person's hand (agent, gateway), minus this device.
	sibs := r.ownDevicesLocked()
	var offers []offerItem
	for tid, meta := range r.ks.Spaces {
		if meta.LocalOnly {
			continue
		}
		st, ok := r.spaces[tid]
		if !ok {
			continue
		}
		r.refreshGrantSeenLocked(tid, st)
		for _, dev := range sibs {
			if dev == self || r.grants.seen[tid][dev] {
				continue
			}
			body := r.sealedGrantLocked(tid, meta, st, dev)
			if body == nil {
				continue
			}
			offers = append(offers, offerItem{dev: dev, tid: tid, body: body,
				eps: append([]storage.Route(nil), r.ks.PeerRoutes[dev]...)})
		}
	}
	// THE CERTIFIED SET ITSELF travels the same plane (transitivity: A
	// pairs C later — B must learn C's certificate even when A owns no
	// space they share). Re-offered when the set grows and once per
	// process start; carries revocations too, or C would never hear that
	// B died without a shared room to hear it in.
	setSize := len(r.ks.Certs) + len(r.ks.Revs)
	for _, dev := range sibs {
		if dev == self || r.grants.setSent[dev] >= setSize {
			continue
		}
		body := r.sealedIdentitySetLocked(dev)
		if body == nil {
			continue
		}
		r.grants.setSent[dev] = setSize
		offers = append(offers, offerItem{dev: dev, body: body,
			eps: append([]storage.Route(nil), r.ks.PeerRoutes[dev]...)})
	}
	// THE REFUSALS TRAVEL THE SAME PLANE (ADR-027). A person who declined
	// somebody on one device must not be knocked on again from the other:
	// the refusal answers on their behalf everywhere, or it answers
	// nowhere.
	refusalCount := len(r.ks.Refusals)
	for _, dev := range sibs {
		if dev == self || r.grants.refusalsSent[dev] == refusalCount || refusalCount == 0 {
			continue
		}
		body := r.sealedRefusalSetLocked(dev)
		if body == nil {
			continue
		}
		r.grants.refusalsSent[dev] = refusalCount
		offers = append(offers, offerItem{dev: dev, body: body,
			eps: append([]storage.Route(nil), r.ks.PeerRoutes[dev]...)})
	}
	own := ""
	r.mu.Unlock()

	if len(offers) == 0 {
		return
	}
	own = r.ResolvePersonalRelay()
	now := uint64(time.Now().Unix())
	expires := now + uint64(DefaultRelayTTL/time.Second)
	present := r.mailboxTags(offers, own, now)
	for _, o := range offers {
		if tag := envelopeTag(o.body); tag != "" && present[tag] {
			continue // this logical message already sits in the mailbox
		}
		// The sibling's own relay when the book states one; this node's
		// relay as the tentative courtesy otherwise — siblings usually
		// share one, and the same honesty rules as delivery apply: a
		// guess is an attempt, and the derived pending set IS the hold.
		ep := own
		for _, rt := range o.eps {
			if rt.Transport == "relay" && rt.Endpoint != "" {
				ep = rt.Endpoint
				break
			}
		}
		if ep == "" {
			continue
		}
		// The plane obeys the relay's own deadline like every other
		// caller: asking while throttled is the thing being asked to
		// stop, and the derived pending set makes waiting free.
		if _, yes := r.relayThrottled(ep); yes {
			continue
		}
		hint := relay.HintIdentityPlane(o.dev, relay.Bucket(now))
		body := o.body
		_ = r.withRelayControl(ep, func(client *relay.Client) error {
			_, err := client.Put(hint, expires, body)
			return err
		})
	}
}

// refreshGrantSeenLocked rescans a space's authors only when its log has
// grown — append-only logs make "seen" monotone.
func (r *Runtime) refreshGrantSeenLocked(tid id.TerminalID, st *spaceState) {
	n := st.space.Log.Len()
	if r.grants.scanned[tid] == n {
		return
	}
	seen := r.grants.seen[tid]
	if seen == nil {
		seen = map[id.DeviceID]bool{}
		r.grants.seen[tid] = seen
	}
	_ = st.space.Log.Replay(func(a eventlog.Applied) error {
		seen[a.Env.Device] = true
		return nil
	})
	r.grants.scanned[tid] = n
}

// sealedGrantLocked builds (once) the signed, sealed grant for one
// sibling. Sealing is randomized, so the bytes are cached: every re-offer
// within this process is byte-identical and the relay dedups it free.
func (r *Runtime) sealedGrantLocked(tid id.TerminalID, meta storage.SpaceMeta,
	st *spaceState, dev id.DeviceID) []byte {
	if cached := r.grants.sealed[tid][dev]; cached != nil {
		return cached
	}
	cert, ok := r.ident.certificateFor(dev)
	if !ok {
		return nil // certified sibling set only — no cert, no grant
	}
	g := &spaceGrant{
		Terminal: tid, Title: meta.Title, Visibility: meta.Visibility,
		ManifestFrame: meta.ManifestFrame,
		Epochs:        append([]crypto.EpochKey(nil), r.ks.Epochs[tid]...),
		LocalTitle:    meta.LocalTitle, Unnamed: meta.Unnamed,
		Grantor: r.Device.ID,
	}
	if len(g.ManifestFrame) == 0 {
		g.ManifestFrame = st.space.ManifestFrame
	}
	signGrant(g, r.Device.SignKey())
	enc, ct, err := crypto.SealTo(cert.X25519Pub,
		append([]byte(grantSigCtx), dev[:]...), encodeSignedGrant(g))
	if err != nil {
		return nil
	}
	var sealed []byte
	sealed = codec.AppendArray(sealed, 3)
	sealed = codec.AppendBytes(sealed, enc)
	sealed = codec.AppendBytes(sealed, ct)
	sealed = codec.AppendBytes(sealed, planeTag(grantSigCtx, tid[:], dev[:], r.Device.ID[:]))
	if r.grants.sealed[tid] == nil {
		r.grants.sealed[tid] = map[id.DeviceID][]byte{}
	}
	r.grants.sealed[tid][dev] = sealed
	return sealed
}

// sealedIdentitySetLocked builds (per set size, per process) the sealed
// certified-set announcement for one sibling. Caller holds r.mu.
func (r *Runtime) sealedIdentitySetLocked(dev id.DeviceID) []byte {
	cert, ok := r.ident.certificateFor(dev)
	if !ok {
		return nil
	}
	is := &identitySet{Grantor: r.Device.ID}
	for _, rec := range r.ks.Certs {
		is.CertFrames = append(is.CertFrames, rec.Frame)
	}
	for _, rec := range r.ks.Revs {
		is.RevFrames = append(is.RevFrames, rec.Frame)
	}
	is.Signature = ed25519.Sign(r.Device.SignKey(),
		append([]byte(identitySetSigCtx), encodeIdentitySetBody(is)...))
	plain := append(encodeIdentitySetBody(is), codec.AppendBytes(nil, is.Signature)...)
	enc, ct, err := crypto.SealTo(cert.X25519Pub,
		append([]byte(grantSigCtx), dev[:]...), plain)
	if err != nil {
		return nil
	}
	var sealed []byte
	sealed = codec.AppendArray(sealed, 3)
	sealed = codec.AppendBytes(sealed, enc)
	sealed = codec.AppendBytes(sealed, ct)
	// The set's tag includes a digest of its contents: a GROWN set is a
	// new logical message and must land beside the old one.
	digest := sha256.New()
	for _, f := range is.CertFrames {
		digest.Write(f)
	}
	for _, f := range is.RevFrames {
		digest.Write(f)
	}
	sealed = codec.AppendBytes(sealed, planeTag(identitySetSigCtx, dev[:], r.Device.ID[:], digest.Sum(nil)))
	r.grants.setBytes[dev] = sealed
	return sealed
}

// envelopeTag reads the deterministic tag off a sealed envelope ("" for
// the tagless first-generation shape).
func envelopeTag(sealed []byte) string {
	d := codec.NewDecoder(sealed)
	n, err := d.ReadArray()
	if err != nil || n < 3 {
		return ""
	}
	if _, err := d.ReadBytes(); err != nil {
		return ""
	}
	if _, err := d.ReadBytes(); err != nil {
		return ""
	}
	tag, err := d.ReadBytes()
	if err != nil {
		return ""
	}
	return string(tag)
}

// mailboxTags reads, once per sibling per process, which logical plane
// messages already sit in that sibling's mailbox — so restarts do not
// stack quota-deep piles of reseals of the same grant.
func (r *Runtime) mailboxTags(offers []offerItem, own string, now uint64) map[string]bool {
	present := map[string]bool{}
	r.mu.Lock()
	r.grantsInit()
	// A COPY, not an alias: the closure below writes the live map under
	// the lock while this loop reads; an aliased read outside it is the
	// data race the 1B race run caught.
	checked := make(map[id.DeviceID]bool, len(r.grants.tagChecked))
	for k, v := range r.grants.tagChecked {
		checked[k] = v
	}
	r.mu.Unlock()
	seen := map[id.DeviceID]bool{}
	for _, o := range offers {
		if seen[o.dev] || checked[o.dev] {
			continue
		}
		seen[o.dev] = true
		ep := own
		for _, rt := range o.eps {
			if rt.Transport == "relay" && rt.Endpoint != "" {
				ep = rt.Endpoint
				break
			}
		}
		if ep == "" {
			continue
		}
		b := relay.Bucket(now)
		hints := [][]byte{relay.HintIdentityPlane(o.dev, b)}
		if b > 0 {
			hints = append(hints, relay.HintIdentityPlane(o.dev, b-1))
		}
		_ = r.withRelayControl(ep, func(client *relay.Client) error {
			items, err := client.Fetch(hints)
			if err != nil {
				return err
			}
			for _, it := range items {
				if tag := envelopeTag(it); tag != "" {
					present[tag] = true
				}
			}
			r.mu.Lock()
			r.grants.tagChecked[o.dev] = true
			r.mu.Unlock()
			return nil
		})
	}
	return present
}

// fetchGrants runs once per sync cycle on the receiving side: read the
// identity mailbox (Fetch — non-destructive), install what verifies.
func (r *Runtime) fetchGrants(addr string) {
	if addr == "" {
		return
	}
	if _, yes := r.relayThrottled(addr); yes {
		return // the relay named a deadline; the mailbox keeps
	}
	r.mu.Lock()
	r.grantsInit()
	r.mu.Unlock()
	if !r.grants.busy.CompareAndSwap(false, true) {
		return
	}
	defer r.grants.busy.Store(false)
	now := uint64(time.Now().Unix())
	b := relay.Bucket(now)
	hints := [][]byte{relay.HintIdentityPlane(r.Device.ID, b)}
	if b > 0 {
		hints = append(hints, relay.HintIdentityPlane(r.Device.ID, b-1))
	}
	var items [][]byte
	err := r.withRelayControl(addr, func(client *relay.Client) error {
		got, err := client.Fetch(hints)
		items = got
		return err
	})
	if err != nil {
		return
	}
	for _, item := range items {
		key := sha256.Sum256(item)
		r.mu.Lock()
		r.grantsInit()
		prev, seen := r.grants.refused[key]
		r.mu.Unlock()
		if seen && !strings.Contains(prev, "uncertified") {
			continue // immutable bytes, monotonic verdict: still refused
		}
		if err := r.installGrant(item); err != nil {
			r.mu.Lock()
			r.grantsInit()
			if !seen {
				r.grants.refusals++
			}
			if len(r.grants.refused) < 512 {
				r.grants.refused[key] = err.Error()
			}
			r.mu.Unlock()
			if !seen {
				log.Printf("node: a space grant was refused: %v", err)
			}
		} else if seen {
			r.mu.Lock()
			delete(r.grants.refused, key)
			r.mu.Unlock()
		}
	}
}

// installGrant opens, verifies and installs one sealed grant. Idempotent:
// a space already attached is a clean no-op, which is what makes the
// sender's held re-offers safe.
func (r *Runtime) installGrant(sealed []byte) error {
	d := codec.NewDecoder(sealed)
	n, err := d.ReadArray()
	if err != nil || n < 2 {
		return errors.New("node: grant envelope malformed")
	}
	enc, err := d.ReadBytes()
	if err != nil {
		return errors.New("node: grant envelope malformed")
	}
	ct, err := d.ReadBytes()
	if err != nil {
		return errors.New("node: grant envelope malformed")
	}
	plain, err := crypto.OpenFrom(r.Device.X25519Priv(),
		append([]byte(grantSigCtx), r.Device.ID[:]...), enc, ct)
	if err != nil {
		return errors.New("node: grant not sealed to this device")
	}
	// TWO MESSAGES RIDE THIS PLANE; the version inside the plaintext
	// decides. Both pass the same trust gate below before anything is
	// believed.
	switch peekPlaneVersion(plain) {
	case identitySetVersion:
		return r.installIdentitySet(plain)
	case refusalSetVersion:
		return r.installRefusalSet(plain)
	}
	g, err := decodeGrant(plain)
	if err != nil {
		return err
	}

	// THE TRUST GATE. The grantor must be a certified, unrevoked device
	// of THIS device's own principal — a cross-principal grant is not a
	// grant, it is an invitation wearing the wrong clothes, and it is
	// refused regardless of how valid its signature is.
	r.mu.Lock()
	cert, ok := r.ident.certificateFor(g.Grantor)
	r.mu.Unlock()
	if !ok {
		return errors.New("node: grant from an uncertified device")
	}
	if !bytes.Equal(cert.Principal[:], r.PrincipalID[:]) {
		return errors.New("node: grant from another principal")
	}
	if err := r.ident.store.Admit(r.PrincipalID, g.Grantor, uint64(time.Now().Unix())); err != nil {
		return errors.New("node: grant from a revoked device")
	}
	pub := ed25519.PublicKey(g.Grantor[:])
	sig := g.Signature
	unsigned := encodeGrantBody(g)
	if !ed25519.Verify(pub, append([]byte(grantSigCtx), unsigned...), sig) {
		return errors.New("node: grant signature does not verify")
	}

	// Install — the adoptAccepted recipe, which is the pass flow's own.
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.spaces[g.Terminal]; exists {
		return nil // idempotent: the observation loop will stop the offers
	}
	s, err := terminals.OpenReplicaAt(g.Terminal, r.root.EventsDir(g.Terminal))
	if err != nil {
		return err
	}
	s.ManifestFrame = g.ManifestFrame
	if len(g.Epochs) > 0 {
		s.EnablePrivate(r.Device)
		s.RestoreEpochs(g.Epochs)
	}
	r.Self.ResumeChain(s)
	r.ks.Spaces[g.Terminal] = storage.SpaceMeta{
		Title: g.Title, Visibility: g.Visibility,
		ManifestFrame: g.ManifestFrame,
		LocalTitle:    g.LocalTitle, Unnamed: g.Unnamed,
	}
	r.attach(g.Terminal, s)
	if _, _, err := r.Self.PublishManifest(s); err != nil {
		return err
	}
	// The certificate into the space is the ACKNOWLEDGEMENT: the granting
	// sibling observes it and stops offering.
	r.publishCertLocked(s)
	r.persistEpochsLocked(g.Terminal, s)
	return r.saveKeystore()
}

// peekPlaneVersion reads the leading version of a plane plaintext without
// consuming it.
func peekPlaneVersion(plain []byte) uint64 {
	d := codec.NewDecoder(plain)
	if _, err := d.ReadArray(); err != nil {
		return 0
	}
	v, err := d.ReadUint()
	if err != nil {
		return 0
	}
	return v
}

// installIdentitySet merges a sibling's certified-set announcement: the
// same trust gate as a space grant, then every frame verified on its own
// root signature and bound to THIS principal before it becomes a record.
func (r *Runtime) installIdentitySet(plain []byte) error {
	is, err := decodeIdentitySet(plain)
	if err != nil {
		return err
	}
	r.mu.Lock()
	cert, ok := r.ident.certificateFor(is.Grantor)
	r.mu.Unlock()
	if !ok {
		return errors.New("node: identity set from an uncertified device")
	}
	if !bytes.Equal(cert.Principal[:], r.PrincipalID[:]) {
		return errors.New("node: identity set from another principal")
	}
	if err := r.ident.store.Admit(r.PrincipalID, is.Grantor, uint64(time.Now().Unix())); err != nil {
		return errors.New("node: identity set from a revoked device")
	}
	pub := ed25519.PublicKey(is.Grantor[:])
	if !ed25519.Verify(pub, append([]byte(identitySetSigCtx), encodeIdentitySetBody(is)...), is.Signature) {
		return errors.New("node: identity set signature does not verify")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	changed := false
	have := map[id.DeviceID]bool{}
	for _, rec := range r.ks.Certs {
		have[rec.Device] = true
	}
	for _, f := range is.CertFrames {
		c, err := identity.DecodeCertificate(f)
		if err != nil || !bytes.Equal(c.Principal[:], r.PrincipalID[:]) {
			continue // another principal's paper does not enter this set
		}
		if err := r.ident.store.AddCertificate(c); err != nil {
			continue
		}
		if !have[c.Device] {
			r.ks.Certs = append(r.ks.Certs, storage.CertRecord{Device: c.Device, Frame: f})
			have[c.Device] = true
			changed = true
		}
	}
	haveRev := map[id.DeviceID]bool{}
	for _, rec := range r.ks.Revs {
		haveRev[rec.Device] = true
	}
	for _, f := range is.RevFrames {
		rv, err := identity.DecodeRevocation(f)
		if err != nil || !bytes.Equal(rv.Principal[:], r.PrincipalID[:]) {
			continue
		}
		if err := r.ident.store.AddRevocation(rv); err != nil {
			continue
		}
		if !haveRev[rv.Device] {
			r.ks.Revs = append(r.ks.Revs, storage.RevRecord{Device: rv.Device, Frame: f})
			haveRev[rv.Device] = true
			changed = true
		}
	}
	if changed {
		return r.saveKeystore()
	}
	return nil
}

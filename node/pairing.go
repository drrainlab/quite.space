// Pairing at the runtime (MD-1/MD-2): the ceremony gets hands, and the
// freight gets contents.
//
// THE ORDER OF THE EXCHANGE IS LOAD-BEARING (plan, MD-1). Admitting the
// child to owned spaces ROTATES their epochs, so a freight assembled before
// that step ships keys that are already superseded and the child arrives
// deaf in exactly the spaces its owner controls:
//
//	1  child   mints DeviceSeed + X25519 locally, sends ONLY public halves
//	2  parent  CertifyPublic{principal, childDevice, childX25519}
//	3  parent  AddMember(child, xpub) + RotateEpoch for every OWNED space
//	4  parent  snapshot POST-ROTATION epochs + space metadata
//	5  parent  seal the freight under the ceremony's key and send it
//	6  child   writes its keystore; its OWN self terminal and routes come
//	           from its own first Open, never from a copy
//
// WHAT TRAVELS: the principal id (public), the child's certificate, the
// display name, and per space — title, visibility, the signed manifest, and
// post-rotation epoch keys. WHAT NEVER TRAVELS, each for a stated reason:
// the PrincipalSeed and TerminalSeeds (root and controller authority stay
// home — decision 5, or revocation is theatre), the parent's device
// secrets, the SelfTerminalSeed (each device is its own terminal, decision
// 6), routes, sagas, and counters (device-local by doctrine). The freight
// test asserts the absences against the actual bytes.
package node

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/drrainlab/quiet_places/kernel/crypto"
	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/pairing"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/lan"
)

// ErrNotAuthority is the MD-2 line: a secondary reads and writes as the
// person, but adding devices is root authority, and root stays home.
var ErrNotAuthority = errors.New("node: only the authority device can pair new devices — this one holds no root seed")

// ---- the freight document ----

const (
	freightVersion  = 1
	freightFields   = 5
	freightSpFields = 4
	enrollFields    = 2
)

type freightSpace struct {
	Terminal      id.TerminalID
	Title         string
	Visibility    string
	ManifestFrame []byte
	Epochs        []crypto.EpochKey
}

type freightDoc struct {
	Principal   id.PrincipalID
	CertFrame   []byte
	DisplayName string
	Spaces      []freightSpace
}

// buildFreightLocked snapshots what the child needs, AFTER the rotations.
// Caller holds r.mu. LocalOnly spaces stay home: the assistant's room is
// this device's room (AI-0), and under decision 6 the child grows its own.
func (r *Runtime) buildFreightLocked() []byte {
	doc := freightDoc{Principal: r.PrincipalID, DisplayName: r.ks.DisplayName}
	for _, rec := range r.ks.Certs {
		if rec.Device == r.Device.ID {
			doc.CertFrame = rec.Frame // replaced by the child's below; see sendFreight
		}
	}
	var buf []byte
	buf = codec.AppendArray(buf, freightFields)
	buf = codec.AppendUint(buf, freightVersion)
	buf = codec.AppendBytes(buf, doc.Principal[:])
	buf = codec.AppendBytes(buf, doc.CertFrame)
	buf = codec.AppendText(buf, doc.DisplayName)
	spaces := make([]freightSpace, 0, len(r.ks.Spaces))
	for tid, meta := range r.ks.Spaces {
		if meta.LocalOnly {
			continue
		}
		spaces = append(spaces, freightSpace{
			Terminal: tid, Title: meta.Title, Visibility: meta.Visibility,
			ManifestFrame: meta.ManifestFrame, Epochs: r.ks.Epochs[tid],
		})
	}
	buf = codec.AppendArray(buf, len(spaces))
	for _, sp := range spaces {
		buf = codec.AppendArray(buf, freightSpFields+1)
		buf = codec.AppendBytes(buf, sp.Terminal[:])
		buf = codec.AppendText(buf, sp.Title)
		buf = codec.AppendText(buf, sp.Visibility)
		buf = codec.AppendBytes(buf, sp.ManifestFrame)
		buf = codec.AppendArray(buf, len(sp.Epochs))
		for _, ek := range sp.Epochs {
			buf = codec.AppendArray(buf, 2)
			buf = codec.AppendUint(buf, ek.N)
			buf = codec.AppendBytes(buf, ek.Key[:])
		}
	}
	return buf
}

func decodeFreight(data []byte) (*freightDoc, error) {
	bad := errors.New("node: malformed pairing freight")
	d := codec.NewDecoder(data)
	n, err := d.ReadArray()
	if err != nil || n < freightFields {
		return nil, bad
	}
	v, err := d.ReadUint()
	if err != nil || v != freightVersion {
		return nil, bad
	}
	doc := &freightDoc{}
	raw, err := d.ReadBytes()
	if err != nil || len(raw) != len(doc.Principal) {
		return nil, bad
	}
	copy(doc.Principal[:], raw)
	if doc.CertFrame, err = d.ReadBytes(); err != nil {
		return nil, bad
	}
	if doc.DisplayName, err = d.ReadText(); err != nil {
		return nil, bad
	}
	count, err := d.ReadArray()
	if err != nil {
		return nil, bad
	}
	for i := 0; i < count; i++ {
		fc, err := d.ReadArray()
		if err != nil || fc < freightSpFields+1 {
			return nil, bad
		}
		var sp freightSpace
		if raw, err = d.ReadBytes(); err != nil || len(raw) != len(sp.Terminal) {
			return nil, bad
		}
		copy(sp.Terminal[:], raw)
		if sp.Title, err = d.ReadText(); err != nil {
			return nil, bad
		}
		if sp.Visibility, err = d.ReadText(); err != nil {
			return nil, bad
		}
		if sp.ManifestFrame, err = d.ReadBytes(); err != nil {
			return nil, bad
		}
		ec, err := d.ReadArray()
		if err != nil {
			return nil, bad
		}
		for j := 0; j < ec; j++ {
			pc, err := d.ReadArray()
			if err != nil || pc < 2 {
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
			for k := 2; k < pc; k++ {
				if err := d.SkipItem(); err != nil {
					return nil, bad
				}
			}
			sp.Epochs = append(sp.Epochs, ek)
		}
		doc.Spaces = append(doc.Spaces, sp)
	}
	return doc, nil
}

// ---- the parent's side ----

// PairingHost is one offer's life at the runtime: a listener the offer
// points at, and the single-use ceremony behind it.
type PairingHost struct {
	r        *Runtime
	node     *lan.Node
	ceremony *pairing.ParentCeremony
	offer    []byte
	conns    chan *lan.Conn
	port     int
	beacon   [32]byte
	stop     chan struct{}
}

// BeginPairing mints the offer and opens the door it names. Authority only:
// the offer promises a certificate, and a secondary cannot sign one.
func (r *Runtime) BeginPairing(bind string, nowUnix uint64) (*PairingHost, error) {
	r.mu.Lock()
	authority := r.Principal != nil
	r.mu.Unlock()
	if !authority {
		return nil, ErrNotAuthority
	}
	node, err := lan.NewNode()
	if err != nil {
		return nil, err
	}
	conns := make(chan *lan.Conn, 4)
	port, err := node.Listen(bind, func(c *lan.Conn) { conns <- c })
	if err != nil {
		node.Close()
		return nil, err
	}
	// THE BIND AND THE ADVERTISEMENT ARE TWO DIFFERENT QUESTIONS, and
	// conflating them was measured on a live pairing: the guessed address
	// followed the default route onto a VPN's utun, the listener bound to
	// THAT address alone, and no packet from the room could ever land —
	// whatever the offer said, whatever the firewall allowed. A wildcard
	// bind listens wherever the child manages to arrive; the host in the
	// offer is only the first guess, and the beacon corrects it.
	host, _, _ := net.SplitHostPort(bind)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = advertiseHost()
	}
	offer, err := pairing.NewOffer(identity.NewRand(),
		net.JoinHostPort(host, fmt.Sprint(port)), nowUnix)
	if err != nil {
		node.Close()
		return nil, err
	}
	return &PairingHost{
		r: r, node: node, ceremony: pairing.NewParentCeremony(offer),
		offer: offer.Encode(), conns: conns, port: port,
		beacon: pairing.BeaconID(offer.Secret), stop: make(chan struct{}),
	}, nil
}

// Announce broadcasts the pairing beacon on udpAddr until the host closes:
// the fallback for the address DHCP moved. Same wire shape as the space
// beacons, under an identity only the offer's holder can derive.
func (h *PairingHost) Announce(udpAddr string) {
	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	n := binary.BigEndian.Uint64(nonce[:])
	tid := id.TerminalID(h.beacon)
	go func() {
		tick := time.NewTicker(1 * time.Second)
		defer tick.Stop()
		for {
			now := uint64(time.Now().Unix())
			_ = lan.AnnounceOnce(udpAddr, h.port,
				[][]byte{lan.Hint(tid, lan.Bucket(now))}, n)
			select {
			case <-h.stop:
				return
			case <-tick.C:
			}
		}
	}()
}

// OfferBytes is what the sound or the QR carries.
func (h *PairingHost) OfferBytes() []byte { return h.offer }

// Close shuts the door. Idempotent; an unspent offer simply dies with it.
func (h *PairingHost) Close() {
	select {
	case <-h.stop:
	default:
		close(h.stop)
	}
	h.node.Close()
}

// PairingAttempt is one dial-in that survived the hellos. Digits go on the
// parent's screen; Approve is for after the human says yes.
type PairingAttempt struct {
	Digits string

	host    *PairingHost
	session *pairing.ParentSession
}

// Accept admits the next dial-in through the hello exchange. A stranger's
// failed attempt returns an error and costs the offer nothing — call Accept
// again for the next dial within the window.
func (h *PairingHost) Accept(nowUnix uint64) (*PairingAttempt, error) {
	select {
	case conn := <-h.conns:
		s, err := h.ceremony.Run(conn, nowUnix)
		if err != nil {
			return nil, err
		}
		return &PairingAttempt{Digits: s.Digits, host: h, session: s}, nil
	case <-time.After(pairing.OfferTTLSeconds * time.Second):
		return nil, pairing.ErrOfferExpired
	}
}

// Approve is the parent's human saying yes: the ceremony confirms, the
// enrollment arrives, and steps 2-5 of the exchange run in their
// load-bearing order.
func (a *PairingAttempt) Approve(nowUnix uint64) error {
	if err := a.session.AwaitChildConfirm(); err != nil {
		return err
	}
	if err := a.session.Confirm(); err != nil {
		return err
	}
	enroll, err := a.session.AwaitSealed()
	if err != nil {
		return err
	}
	childDev, childX, err := decodeEnrollment(enroll)
	if err != nil {
		return err
	}

	r := a.host.r
	r.mu.Lock()
	if r.Principal == nil {
		r.mu.Unlock()
		return ErrNotAuthority
	}
	// 2 — certify the public halves; the child's private keys never left it.
	cert := r.Principal.CertifyPublic(childDev, childX, nowUnix, 0)
	frame, err := cert.Encode()
	if err != nil {
		r.mu.Unlock()
		return err
	}
	r.ks.Certs = append(r.ks.Certs, storage.CertRecord{Device: childDev, Frame: frame})
	_ = r.ident.store.AddCertificate(cert)
	// 3 — the child joins every OWNED space, and each join rotates: a stolen
	// recording of yesterday's epochs must not read tomorrow.
	for tid, st := range r.spaces {
		meta := r.ks.Spaces[tid]
		if !meta.Owned || meta.LocalOnly {
			continue
		}
		st.space.AddMember(childDev, childX)
		if _, err := r.Self.RotateEpoch(st.space); err != nil {
			r.mu.Unlock()
			return fmt.Errorf("node: rotating %s for the new device: %w", tid, err)
		}
		r.persistEpochsLocked(tid, st.space)
		// A CURATED space keys its writers on (principal, DEVICE), so the
		// person's new device must be written into its own owned rooms or it
		// arrives able to read and forbidden to speak — the first paired
		// phone measured exactly that. Content, not administration: MD-2's
		// line (a secondary cannot administer) is about what the CHILD may
		// do, and this is the AUTHORITY doing it.
		if pol := st.space.Policy(); pol.Publish == terminals.PublishCurated {
			title, character := st.space.Character()
			next := pol
			next.Writers = append(append([]terminals.WriterBinding(nil), pol.Writers...),
				terminals.WriterBinding{Principal: r.PrincipalID, Device: childDev})
			if err := st.space.ReviseManifest(title, character, next); err == nil {
				meta.ManifestFrame = st.space.ManifestFrame
				r.ks.Spaces[tid] = meta
			}
		}
		// The certificate travels in the log too (its second role): peers
		// learn the new device the ordinary way.
		r.publishCertLocked(st.space)
	}
	// 4 — snapshot AFTER the rotations, never before.
	doc := r.buildFreightLocked()
	doc = replaceFreightCert(doc, frame)
	if err := r.saveKeystore(); err != nil {
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()

	// 5 — seal and send.
	return a.session.SendSealed(doc)
}

// replaceFreightCert swaps the parent's own certificate placeholder for the
// CHILD's freshly signed one — the one the child's keystore must hold to
// know its own principal.
func replaceFreightCert(doc []byte, childCert []byte) []byte {
	parsed, err := decodeFreight(doc)
	if err != nil {
		return doc
	}
	parsed.CertFrame = childCert
	var buf []byte
	buf = codec.AppendArray(buf, freightFields)
	buf = codec.AppendUint(buf, freightVersion)
	buf = codec.AppendBytes(buf, parsed.Principal[:])
	buf = codec.AppendBytes(buf, parsed.CertFrame)
	buf = codec.AppendText(buf, parsed.DisplayName)
	buf = codec.AppendArray(buf, len(parsed.Spaces))
	for _, sp := range parsed.Spaces {
		buf = codec.AppendArray(buf, freightSpFields+1)
		buf = codec.AppendBytes(buf, sp.Terminal[:])
		buf = codec.AppendText(buf, sp.Title)
		buf = codec.AppendText(buf, sp.Visibility)
		buf = codec.AppendBytes(buf, sp.ManifestFrame)
		buf = codec.AppendArray(buf, len(sp.Epochs))
		for _, ek := range sp.Epochs {
			buf = codec.AppendArray(buf, 2)
			buf = codec.AppendUint(buf, ek.N)
			buf = codec.AppendBytes(buf, ek.Key[:])
		}
	}
	return buf
}

func encodeEnrollment(dev id.DeviceID, xpub [32]byte) []byte {
	var buf []byte
	buf = codec.AppendArray(buf, enrollFields)
	buf = codec.AppendBytes(buf, dev[:])
	buf = codec.AppendBytes(buf, xpub[:])
	return buf
}

func decodeEnrollment(data []byte) (id.DeviceID, [32]byte, error) {
	var dev id.DeviceID
	var xpub [32]byte
	bad := errors.New("node: malformed pairing enrollment")
	d := codec.NewDecoder(data)
	n, err := d.ReadArray()
	if err != nil || n < enrollFields {
		return dev, xpub, bad
	}
	raw, err := d.ReadBytes()
	if err != nil || len(raw) != len(dev) {
		return dev, xpub, bad
	}
	copy(dev[:], raw)
	if raw, err = d.ReadBytes(); err != nil || len(raw) != len(xpub) {
		return dev, xpub, bad
	}
	copy(xpub[:], raw)
	return dev, xpub, nil
}

// ---- the child's side ----

// JoinAsPairedDevice runs the whole child half against an EMPTY data dir:
// dial the offer, show the digits, confirm, mint keys at home, send the
// public halves, receive the freight, write the keystore. The next
// node.Open of that dir is a secondary of the person.
func JoinAsPairedDevice(dir string, passphrase, offerBytes []byte,
	approve func(digits string) bool, nowUnix uint64) error {
	return JoinAsPairedDeviceVia(dir, passphrase, offerBytes, "", approve, nowUnix)
}

// JoinAsPairedDeviceVia is JoinAsPairedDevice with a beacon fallback: when
// the offer's address does not answer — DHCP moved the parent — the child
// listens on announceAddr for the beacon only the offer's holder can derive,
// and dials the address it names instead.
func JoinAsPairedDeviceVia(dir string, passphrase, offerBytes []byte,
	announceAddr string, approve func(digits string) bool, nowUnix uint64) error {

	// The same rule as restore, for the same reason: pairing writes an
	// identity, and identities are never written over each other.
	if err := requireEmptyDir(dir); err != nil {
		return err
	}
	offer, err := pairing.DecodeOffer(offerBytes)
	if err != nil {
		return err
	}
	node, err := lan.NewNode()
	if err != nil {
		return err
	}
	defer node.Close()
	// The offer's address is the FAST PATH and the beacon is the truth: DHCP,
	// VPNs and firewalls all rewrite the guess inside the code, so the child
	// ALTERNATES — a bounded dial, then a listen for the beacon only the
	// offer's holder can derive — for as long as the offer lives. One path
	// failing while another would have worked must never end the ceremony
	// early; the sixty-second expiry is the one clock that does.
	var conn *lan.Conn
	dialErr := errors.New("never dialled")
	addr := offer.Addr
	for conn == nil {
		if uint64(time.Now().Unix()) >= offer.ExpiresAt {
			return fmt.Errorf("node: the offer expired before the parent was reachable (last attempt at %s: %v)", addr, dialErr)
		}
		if conn, dialErr = node.Dial(addr); dialErr == nil {
			break
		}
		if announceAddr == "" {
			return dialErr
		}
		if heard, berr := awaitPairingBeacon(announceAddr, pairing.BeaconID(offer.Secret)); berr == nil {
			addr = heard // and loop: the next dial goes where the beacon points
		}
	}
	defer conn.Close()

	cs, err := pairing.RunChildCeremony(offer, conn, nowUnix)
	if err != nil {
		return err
	}
	if !approve(cs.Digits) {
		return errors.New("node: pairing declined at the digits")
	}
	if err := cs.Confirm(); err != nil {
		return err
	}
	if err := cs.AwaitParentConfirm(); err != nil {
		return err
	}

	// 1 — mint at home; only the public halves leave.
	dev, err := identity.NewDevice(identity.NewRand())
	if err != nil {
		return err
	}
	if err := cs.SendSealed(encodeEnrollment(dev.ID, dev.X25519Pub)); err != nil {
		return err
	}
	raw, err := cs.AwaitSealed()
	if err != nil {
		return err
	}
	doc, err := decodeFreight(raw)
	if err != nil {
		return err
	}
	// The certificate is verified BEFORE anything is written: it must be
	// root-signed, for THIS device, by THE principal the freight claims.
	cert, err := identity.DecodeCertificate(doc.CertFrame)
	if err != nil {
		return err
	}
	if err := cert.Verify(); err != nil {
		return err
	}
	if cert.Device != dev.ID || cert.Principal != doc.Principal {
		return errors.New("node: the freight's certificate does not name this device and this principal")
	}

	// 6 — the keystore, with the ABSENCES that make it a secondary: no
	// PrincipalSeed, no TerminalSeeds, no SelfTerminalSeed. Its own self
	// terminal, ingress and routes grow at its own first Open.
	root, err := storage.Open(dir, passphrase)
	if err != nil {
		return err
	}
	ks := &storage.Keystore{
		DeviceSeed:    dev.Seed(),
		DeviceX25519:  dev.X25519Priv(),
		DisplayName:   doc.DisplayName,
		Certs:         []storage.CertRecord{{Device: dev.ID, Frame: doc.CertFrame}},
		TerminalSeeds: map[id.TerminalID][]byte{},
		Epochs:        map[id.TerminalID][]crypto.EpochKey{},
		Spaces:        map[id.TerminalID]storage.SpaceMeta{},
		Forgotten:     map[id.TerminalID]int64{},
		PeerRoutes:    map[id.DeviceID][]storage.Route{},
		PublicPublish: map[id.TerminalID]storage.PublicPublishState{},
	}
	for _, sp := range doc.Spaces {
		ks.Spaces[sp.Terminal] = storage.SpaceMeta{
			Title: sp.Title, Visibility: sp.Visibility,
			ManifestFrame: sp.ManifestFrame,
			// NEVER Owned: controller authority did not travel, and the
			// metadata must not claim what the keystore cannot back.
		}
		ks.Epochs[sp.Terminal] = sp.Epochs
	}
	return root.SaveKeystore(ks)
}

// awaitPairingBeacon listens for the parent's pairing beacon and returns the
// address it names. Bounded well inside the offer's own life.
func awaitPairingBeacon(udpAddr string, beacon [32]byte) (string, error) {
	tid := id.TerminalID(beacon)
	found := make(chan string, 1)
	_, stop, err := lan.ListenAnnounces(udpAddr, func(a lan.Announcement) {
		if lan.MatchHint(a, tid, uint64(time.Now().Unix())) {
			select {
			case found <- a.Addr:
			default:
			}
		}
	})
	if err != nil {
		return "", err
	}
	defer stop()
	select {
	case addr := <-found:
		return addr, nil
	case <-time.After(10 * time.Second):
		return "", errors.New("node: no pairing beacon within ten seconds")
	}
}

// advertiseHost picks the address the offer names: a private IPv4 on a real
// interface, never a tunnel. 198.18.0.0/15 is excluded by name — it is the
// benchmarking range VPNs squat on, and an offer advertising it sends the
// child into a wall (measured). The UDP-dial trick is only the LAST resort,
// because it follows the default route — which is exactly what a VPN owns.
func advertiseHost() string {
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, ifc := range ifaces {
			if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 ||
				ifc.Flags&net.FlagPointToPoint != 0 {
				continue // tunnels are point-to-point; rooms are not
			}
			addrs, err := ifc.Addrs()
			if err != nil {
				continue
			}
			for _, a := range addrs {
				ipn, ok := a.(*net.IPNet)
				if !ok {
					continue
				}
				ip4 := ipn.IP.To4()
				if ip4 == nil || !ip4.IsPrivate() {
					continue
				}
				if ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19) {
					continue // the VPN squat range, excluded by name
				}
				return ip4.String()
			}
		}
	}
	if c, err := net.Dial("udp", "192.0.2.1:1"); err == nil {
		defer c.Close()
		if h, _, err := net.SplitHostPort(c.LocalAddr().String()); err == nil {
			return h
		}
	}
	return "127.0.0.1"
}

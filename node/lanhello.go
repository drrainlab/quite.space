// LAN hello (T6-LAN / L1): a device names its key on one TLS session.
//
// The LAN link is deliberately anonymous at the transport layer — the TLS
// cert is ephemeral and claims nothing (see transports/lan). What T6 needs
// is one fact the transport cannot give: WHICH device is on the other end,
// proved, so a delivery decision may treat "on this wire" as "reaches that
// device". The hello is that proof: an Ed25519 signature over keying
// material exported from the very TLS session it rides. The material is
// session-unique and derivable only by the two parties, so the signature
// cannot be replayed onto another connection and cannot be relayed by a
// person in the middle — their two sessions export different material.
//
// What a verified hello DOES NOT do:
//   - it never enters the keystore — the binding is observed reachability,
//     valid while the link lives and evicted with it (dropConn);
//   - it grants nothing — membership, epochs and event authenticity are
//     the space's business; the binding only routes copies;
//   - it is not gossip — it binds a device to THIS link, never to an
//     address someone else could dial (ADR-015: topology stays local).
package node

import (
	"crypto/ed25519"

	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// lanHelloLabel is both the TLS exporter label and the signature domain.
const lanHelloLabel = "qp-lan-hello-v0"

// helloPayload keys (append-only).
const (
	helloKeyDevice = 1
	helloKeySig    = 2
)

// sessionBound is the capability a link must have for a hello to mean
// anything: keying material only the two live parties can derive.
type sessionBound interface {
	SessionBinding(label string) ([]byte, bool)
}

// sendLANHello introduces this device on a freshly adopted link, when the
// link can bind a signature to its session. Links that cannot (radio
// wrappers, loopback test pairs) simply stay anonymous — nothing breaks,
// no route candidate forms.
func (r *Runtime) sendLANHello(c link) {
	sb, ok := c.(sessionBound)
	if !ok {
		return
	}
	ekm, ok := sb.SessionBinding(lanHelloLabel)
	if !ok {
		return
	}
	msg := append([]byte(lanHelloLabel+":"), ekm...)
	sig := ed25519.Sign(r.Device.SignKey(), msg)
	buf := codec.AppendMap(nil, 2)
	buf = codec.AppendUint(buf, helloKeyDevice)
	buf = codec.AppendBytes(buf, r.Device.ID[:])
	buf = codec.AppendUint(buf, helloKeySig)
	buf = codec.AppendBytes(buf, sig)
	// Every packet on a link rides the fragment grammar — sendMsg wraps sync
	// messages the same way, and a bare message is dropped as malformed.
	pkts, err := kernelsync.FragmentStream(0, kernelsync.EncodeHelloMessage(buf), 0)
	if err != nil {
		return
	}
	for _, p := range pkts {
		_ = c.Send(p)
	}
}

// noteLANHello verifies a received hello against THIS link's session and,
// on success, records the observed binding device → link. A hello that does
// not verify records nothing and says nothing — an unauthenticated LAN is
// full of noise, and noise must not shape delivery.
func (r *Runtime) noteLANHello(c link, payload []byte) {
	sb, ok := c.(sessionBound)
	if !ok {
		return
	}
	ekm, ok := sb.SessionBinding(lanHelloLabel)
	if !ok {
		return
	}
	var devBytes, sig []byte
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return
	}
	for {
		k, more, e := m.Next()
		if e != nil {
			return
		}
		if !more {
			break
		}
		switch k {
		case helloKeyDevice:
			devBytes, e = d.ReadBytes()
		case helloKeySig:
			sig, e = d.ReadBytes()
		default:
			e = d.SkipItem()
		}
		if e != nil {
			return
		}
	}
	if len(devBytes) != id.Size || len(sig) != ed25519.SignatureSize {
		return
	}
	msg := append([]byte(lanHelloLabel+":"), ekm...)
	if !ed25519.Verify(ed25519.PublicKey(devBytes), msg, sig) {
		return
	}
	var dev id.DeviceID
	copy(dev[:], devBytes)
	if dev == r.Device.ID {
		return // our own reflection is not a peer
	}
	r.mu.Lock()
	if r.lanPeers == nil {
		r.lanPeers = map[id.DeviceID]link{}
	}
	r.lanPeers[dev] = c
	r.mu.Unlock()
}

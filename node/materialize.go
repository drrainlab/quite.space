// The Space-free swarm core (PM-0).
//
// Everything a wanter needs to pull public media — push wants to the
// ingress shards, drain a reply box, drive the 2-phase manifest→chunks
// ladder — over the SAME wire, the same verification and the same chunk
// rules the persistent paths use. Nothing here takes a Space, a keystore
// or the runtime lock: the transient post session (PM-2) is the first
// consumer, and the persistent fetchLoop migrates onto this core in its
// own later gate. No protocol rule is duplicated; the primitives
// (bundle, relay, assets) were always space-free — this file is the loop
// logic that used to be tangled with r.spaces.
//
// The wanter identity here is ANY 32 bytes. PH-1's stated intent was "a
// per-request random address names nobody", and holders never read the
// wanter field on the public path (the reply box is the only address) —
// but the persistent reader still ships its device id in the clear. A
// session mints a random identity instead: the id picks an ingress shard
// and nothing else, and any 32 bytes shard equally well.
package node

import (
	"crypto/rand"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/transports/bundle"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// swarmWantTTL bounds how long a want advertisement sits at the relay.
// Short on purpose: a session lives minutes, and abandoned wants should
// not invite answers for hours (the ingress mailbox is a shared resource
// with a 64-items-per-hint cap).
const swarmWantTTL = 15 * time.Minute

// newSwarmIdentity mints a random wanter id. It picks an ingress shard
// and names nobody.
func newSwarmIdentity() (id.DeviceID, error) {
	var w id.DeviceID
	_, err := rand.Read(w[:])
	return w, err
}

// swarmPushWants advertises wanted blob hashes on the space's public
// ingress shard, answered into the reply box. The hints are the
// PUBLISHER's commitment from the verified projection envelope
// ([current bucket × 8 shards][next bucket × 8 shards]) — deriving the
// address locally is exactly what PH-1 took away.
//
// Wants must arrive SORTED by the caller: the relay's Put is
// content-idempotent, so a byte-stable bundle re-Put occupies one slot
// instead of minting a new one per call.
func swarmPushWants(client *relay.Client, tid id.TerminalID,
	ingressHints [][]byte, wanter id.DeviceID, wants [][]byte, replyCap []byte) error {

	if len(wants) == 0 || len(ingressHints) < 8 {
		return nil
	}
	shard := relay.IngressShard(wanter)
	hint := ingressHints[shard]
	if len(hint) == 0 {
		return nil
	}
	boxHint := relay.CollectHint(replyCap)
	body := bundle.EncodeWithReplyBox(tid, nil, nil, wants, wanter[:], boxHint)
	expires := uint64(time.Now().Unix()) + uint64(swarmWantTTL/time.Second)
	_, err := client.Put(hint, expires, body)
	return err
}

// swarmCollect drains the session's reply box and stores ONLY expected
// blobs. A holder can put anything into a box whose hint it saw; an
// unexpected hash is dropped BEFORE PutBlob and never spends the
// caller's budget — promotion-time filtering alone would still let a
// hostile holder eat the memory cap.
//
// admit is the budget gate, called with each candidate's size before it
// is stored; a false answer skips the blob.
func swarmCollect(client *relay.Client, replyCap []byte,
	expected func(id.Hash) bool, admit func(int) bool, sink assets.Store) int {

	items, err := client.Collect([][]byte{replyCap})
	if err != nil {
		return 0
	}
	stored := 0
	for _, item := range items {
		parts, err := bundle.DecodeParts(item)
		if err != nil {
			continue
		}
		for _, blob := range parts.Blobs {
			h := id.HashOf(blob)
			if !expected(h) {
				continue // unsolicited: dropped before it costs anything
			}
			if sink.HasBlob(h) {
				continue
			}
			if !admit(len(blob)) {
				continue
			}
			if _, err := sink.PutBlob(blob); err == nil {
				stored++
			}
		}
	}
	return stored
}

// swarmMissing is one step of the 2-phase ladder: what to want NEXT for
// this ref against this store, and whether it is already complete. The
// manifest is the sole bootstrap hash — chunk ids exist only after it
// decrypts (assets.LoadManifest cross-checks it against the ref, so a
// holder cannot substitute manifests).
func swarmMissing(store assets.Store, ref *schemas.AssetRef) (wants []id.Hash, complete bool, err error) {
	res, err := assets.Missing(store, ref)
	if err != nil {
		return nil, false, err
	}
	if res.ManifestMissing {
		return []id.Hash{*ref.ManifestWireID}, false, nil
	}
	if len(res.MissingChunks) == 0 {
		return nil, true, nil
	}
	return res.MissingChunks, false, nil
}

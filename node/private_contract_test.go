package node

import (
	"fmt"
	"testing"

	"github.com/drrainlab/quiet_places/transports/relay"
)

// PA-1.3 private contract: a PRIVATE space must never touch any PUBLIC
// mailbox. Public projections (HintPublicOutbox) and community ingress
// (HintPublicIngress) are exclusively a public-space concern; a private
// space that happens to relay-sync must leave both namespaces empty.
func TestPrivateSpaceNeverTouchesPublicMailboxes(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()

	// A private space (the default) with a private member and some content.
	tid, err := owner.CreateSpace("Sanctum")
	if err != nil {
		t.Fatal(err)
	}
	if sp, ok := owner.spaceForTest(tid); !ok || sp.Policy().IsPublic() {
		t.Fatal("CreateSpace produced a public policy")
	}
	if _, err := owner.Say(tid, "for our eyes only", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := owner.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	// Drive a full sync cycle (push + pull), twice for good measure.
	owner.relaySyncOnce(addr)
	owner.relaySyncOnce(addr)

	client, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	now := uint64(1_700_000_000)
	var publicHints [][]byte
	for _, b := range []uint64{relay.Bucket(now), relay.Bucket(now) - 1} {
		publicHints = append(publicHints, relay.HintPublicOutbox(tid, b))
		for sh := byte(0); sh < relay.IngressShards; sh++ {
			publicHints = append(publicHints, relay.HintPublicIngress(tid, b, sh))
		}
	}
	// Also cover the live buckets the node would actually use.
	for _, b := range []uint64{relayBucketNow(), relayBucketNow() - 1} {
		publicHints = append(publicHints, relay.HintPublicOutbox(tid, b))
		for sh := byte(0); sh < relay.IngressShards; sh++ {
			publicHints = append(publicHints, relay.HintPublicIngress(tid, b, sh))
		}
	}
	items, err := client.Fetch(publicHints)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("private space wrote %d item(s) into public mailboxes", len(items))
	}
}

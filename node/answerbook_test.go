package node

// The answer book: a want that re-rides every cycle is answered once per
// window, not once per copy — the mailbox quota was the ceiling photos
// hit on the owner's Mac.

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

func TestAWantRepeatedEveryCycleIsAnsweredOncePerWindow(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	holder := openRuntime(t, t.TempDir(), "holder")
	defer holder.Close()
	tid := openPublicSpaceForMirror(t, holder, "Photos")
	held := emitVisual(t, holder, tid, randBytes(t, 200_000), 4096)
	if held.ManifestWireID == nil {
		t.Fatal("test needs the manifest path")
	}
	client, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	box, err := relay.NewReplyCap()
	if err != nil {
		t.Fatal(err)
	}
	hint := relay.CollectHint(box)
	want := [][]byte{held.ManifestWireID[:]}

	// The same want, five cycles in a row, nobody collecting in between —
	// the shape of a phone asking every two seconds while its holder is
	// busy. One answer lands; the other four are the book saying "sent".
	first, err := holder.answerWants(client, tid, nil, want, hint, true)
	if err != nil || first == 0 {
		t.Fatalf("the first ask was not answered: %d bytes, %v", first, err)
	}
	for i := 0; i < 4; i++ {
		n, err := holder.answerWants(client, tid, nil, want, hint, true)
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("ask %d was answered again with %d bytes — the mailbox fills with duplicates", i+2, n)
		}
	}
	got, err := client.Collect([][]byte{box})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("the mailbox holds %d items, want exactly one answer", len(got))
	}

	// After the window a lost answer is repaired: the book forgets.
	holder.mu.Lock()
	for _, page := range holder.answered {
		for h := range page {
			page[h] = time.Now().Add(-answerRepeatAfter - time.Second)
		}
	}
	holder.mu.Unlock()
	again, err := holder.answerWants(client, tid, nil, want, hint, true)
	if err != nil || again == 0 {
		t.Fatalf("after the window the ask was not answered again: %d bytes, %v", again, err)
	}
}

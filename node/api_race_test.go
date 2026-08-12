package node

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// The read side of the API and the relay-sync goroutine touch the same
// replica. Sync writes it under r.mu (PullFromRelay -> AttachSyncApply ->
// Space.absorb -> registry Upsert); the handlers project it. An accessor that
// returned the *Space and released the lock let those two run at once, which
// in Go is not stale data but a fatal "concurrent map read and map write" —
// the process dies, mid-request, on a node doing nothing unusual (auto sync
// is on by default).
//
// So this test does the ordinary thing on purpose: one node syncs from a
// relay on a tight cadence while every read-side projection is polled. It
// asserts nothing about the JSON — under -race, the detector is the
// assertion, and a projection reading outside r.mu trips it.
//
// Run it as: go test -race -run TestReadProjectionsDoNotRaceRelaySync ./node/
func TestReadProjectionsDoNotRaceRelaySync(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := "127.0.0.1:" + itoa(port)

	// writer produces events elsewhere; reader is the node under test — it
	// only ever learns through the relay, which is the path that writes its
	// replica from a goroutine the handlers know nothing about.
	writer := openRuntime(t, t.TempDir(), "alice")
	defer writer.Close()
	reader := openRuntime(t, t.TempDir(), "bob")
	defer reader.Close()

	tid, err := writer.CreateSpace("Contention")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := writer.MintInvite(tid, reader.Device.ID, reader.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}

	api, err := NewAPIServer(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := httptest.NewServer(api.Handler())
	defer httpSrv.Close()
	sid := tid.Hex()

	get := func(path string) {
		req, _ := http.NewRequest("GET", httpSrv.URL+path, nil)
		req.Header.Set("X-QP-Token", api.Token())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return // the server is shutting down at the end of the run
		}
		resp.Body.Close()
	}

	// Every read-side projection that used to hold a replica pointer with no
	// lock. They are listed by route so a future handler added to this family
	// has an obvious place to be covered.
	paths := []string{
		"/api/spaces",
		"/api/spaces/" + sid + "/messages",
		"/api/spaces/" + sid + "/state",
		"/api/spaces/" + sid + "/members",
		"/api/spaces/" + sid + "/entries",
		"/api/spaces/" + sid + "/publications",
		"/api/spaces/" + sid + "/apps",
		"/api/spaces/" + sid + "/shelf",
		"/api/spaces/" + sid + "/resonance/palette",
	}

	// The real background loop, not a hand-rolled pull: this is what ships.
	reader.applyRelaySync(addr, 5*time.Millisecond)
	defer reader.applyRelaySync("", 0) // stop before the runtimes close

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// The writer keeps the relay supplied. Say grows the log; SetName
	// republishes the self manifest into the space, which is what makes the
	// reader's absorb reach registry Upsert — the exact write the observed
	// race reported against registry.All via MemberCards.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := writer.Say(tid, "keep talking", SayOptions{}); err != nil {
				return
			}
			if i%3 == 0 {
				if err := writer.SetName("alice " + itoa(i)); err != nil {
					return
				}
			}
			if _, _, err := writer.PushToRelay(addr, tid); err != nil {
				return
			}
		}
	}()

	// Several readers, so projections overlap each other as well as sync.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, p := range paths {
					get(p)
				}
			}
		}()
	}

	// Long enough for many sync cycles at a 5ms cadence, short enough to sit
	// in a normal test run.
	time.Sleep(750 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Guard against a vacuous pass: if nothing ever synced, the handlers were
	// projecting an empty replica and never met the writer at all.
	var applied int
	sp, ok := reader.spaceForTest(tid)
	if !ok {
		t.Fatal("reader lost the space")
	}
	reader.mu.Lock()
	applied = len(sp.State.Messages())
	reader.mu.Unlock()
	if applied == 0 {
		t.Fatal("nothing synced through the relay: the race window was never open")
	}
}

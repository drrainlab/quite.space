// The route book's codec: survives a round trip, a record from an older
// build, and a record from a newer one. The keystore bricks whole nodes when
// this is wrong, which is why every case here is the compatibility case.
package storage

import (
	"testing"

	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

func TestRouteBookRoundTrip(t *testing.T) {
	devA := id.DeviceID{1, 2, 3}
	devB := id.DeviceID{9, 8, 7}
	self := []Route{
		{Endpoint: "127.0.0.1:7411", Transport: "relay", Provenance: RouteManual, LearnedAt: 100, LastSeen: 200},
	}
	peers := map[id.DeviceID][]Route{
		devA: {
			{Endpoint: "10.0.0.2:7411", Transport: "relay", Provenance: RouteInvitation, LearnedAt: 300, LastSeen: 400},
			{Endpoint: "10.0.0.3:7411", Transport: "relay", Provenance: RouteLegacy, LearnedAt: 1, LastSeen: 1},
		},
		devB: {
			{Endpoint: "192.168.1.5:7411", Transport: "relay", Provenance: RouteObserved, LearnedAt: 500, LastSeen: 600},
		},
	}

	buf := appendRouteBook(nil, self, peers)
	gotSelf, gotPeers, err := readRouteBook(codec.NewDecoder(buf))
	if err != nil {
		t.Fatal(err)
	}
	if len(gotSelf) != 1 || gotSelf[0] != self[0] {
		t.Fatalf("self ingress did not survive: %+v", gotSelf)
	}
	if len(gotPeers) != 2 || len(gotPeers[devA]) != 2 || len(gotPeers[devB]) != 1 {
		t.Fatalf("peer routes did not survive: %+v", gotPeers)
	}
	if gotPeers[devA][0] != peers[devA][0] || gotPeers[devA][1] != peers[devA][1] {
		t.Fatalf("devA routes mangled: %+v", gotPeers[devA])
	}
}

// A record from an OLDER build: fewer fields per route. The decoder takes
// what is there and leaves zero values for the rest.
func TestRouteFromAShorterRecord(t *testing.T) {
	buf := codec.AppendArray(nil, 2) // endpoint + transport only
	buf = codec.AppendText(buf, "127.0.0.1:7411")
	buf = codec.AppendText(buf, "relay")
	r, err := readRoute(codec.NewDecoder(buf))
	if err != nil {
		t.Fatal(err)
	}
	if r.Endpoint != "127.0.0.1:7411" || r.Transport != "relay" || r.Provenance != 0 {
		t.Fatalf("short record misread: %+v", r)
	}
}

// A record from a NEWER build: extra trailing fields are skipped, never a
// mid-record death — the mistake that made two earlier waves one-way
// upgrades.
func TestRouteFromALongerRecord(t *testing.T) {
	buf := codec.AppendArray(nil, routeFields+2)
	buf = codec.AppendText(buf, "127.0.0.1:7411")
	buf = codec.AppendText(buf, "relay")
	buf = codec.AppendUint(buf, uint64(RouteInvitation))
	buf = codec.AppendUint(buf, 100)
	buf = codec.AppendUint(buf, 200)
	buf = codec.AppendText(buf, "a field from the future")
	buf = codec.AppendUint(buf, 42)
	r, err := readRoute(codec.NewDecoder(buf))
	if err != nil {
		t.Fatal(err)
	}
	if r.Endpoint != "127.0.0.1:7411" || r.Provenance != RouteInvitation || r.LastSeen != 200 {
		t.Fatalf("long record misread: %+v", r)
	}
}

// The whole keystore: a route book written, sealed, reopened. And the
// downgrade case — the decoder loop skips unknown top-level keys, which this
// pins from the writing side (an 18-key store must still satisfy Done()).
func TestKeystoreCarriesTheRouteBook(t *testing.T) {
	root, err := Open(t.TempDir(), []byte("a decent passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := identity.NewPrincipal(identity.NewRand())
	if err != nil {
		t.Fatal(err)
	}
	d, err := identity.NewDevice(identity.NewRand())
	if err != nil {
		t.Fatal(err)
	}
	ks := NewKeystore(p, d)
	dev := id.DeviceID{5, 5, 5}
	ks.SelfIngress = []Route{{Endpoint: "127.0.0.1:1", Transport: "relay", Provenance: RouteManual}}
	ks.PeerRoutes[dev] = []Route{{Endpoint: "127.0.0.1:2", Transport: "relay", Provenance: RouteInvitation}}
	if err := root.SaveKeystore(ks); err != nil {
		t.Fatal(err)
	}
	got, err := root.LoadKeystore()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SelfIngress) != 1 || got.SelfIngress[0].Endpoint != "127.0.0.1:1" {
		t.Fatalf("self ingress lost across seal/open: %+v", got.SelfIngress)
	}
	if len(got.PeerRoutes[dev]) != 1 || got.PeerRoutes[dev][0].Endpoint != "127.0.0.1:2" {
		t.Fatalf("peer routes lost across seal/open: %+v", got.PeerRoutes)
	}
}

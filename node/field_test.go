// SP-3 node gates: the field emits behind canWrite, the SOS fallback law
// at the emit path, geo-bearing objects through the ordinary revision
// machinery, and the projections the map bundle is built from.
package node

import (
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/geo"
	"github.com/drrainlab/quiet_places/protocol/objects"
)

func fieldPt(t *testing.T) geo.Point {
	t.Helper()
	p, err := geo.FromDegrees(59.3321, 18.0412)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFieldEmitsAtNode(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Field")
	if err != nil {
		t.Fatal(err)
	}
	pt := fieldPt(t)

	// Position lands in trust with the ladder's inputs.
	if err := rt.SetPosition(tid, pt, 8, 600); err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.spaceForTest(tid)
	// Marker with a default label from its kind.
	if _, err := rt.PlaceMarker(tid, "hazard", "", pt, nil, 0); err != nil {
		t.Fatal(err)
	}
	ms := sp.State.Markers()
	if len(ms) != 1 || ms[0].Text != "hazard" {
		t.Fatalf("marker default label wrong: %+v", ms)
	}

	// THE SOS FALLBACK LAW: empty note + sos → an emergency-semantic
	// fallback, never a soothing one.
	if _, err := rt.SendCheckin(tid, "", nil, 0, false, true); err != nil {
		t.Fatal(err)
	}
	ci, ok := sp.State.LatestCheckin(rt.PrincipalID)
	if !ok || !ci.SOS || ci.Text != "🆘 SOS" {
		t.Fatalf("SOS fallback wrong: %+v", ci)
	}
	// And the plain default.
	if _, err := rt.SendCheckin(tid, "", nil, 67, true, false); err != nil {
		t.Fatal(err)
	}
	ci, _ = sp.State.LatestCheckin(rt.PrincipalID)
	if ci.SOS || ci.Text != "✓ check-in" || !ci.HasBattery || ci.BatteryPct != 67 {
		t.Fatalf("plain fallback wrong: %+v", ci)
	}

	// A place and a route ride the ordinary objects machinery.
	place := &objects.Record{Kind: "sector", Name: "Sector B4",
		Geo: &objects.GeoShape{Point: pt, RadiusM: 400}}
	if _, _, err := rt.CreateObject(tid, place); err != nil {
		t.Fatal(err)
	}
	route := &objects.Record{Kind: "route", Name: "North ridge"}
	for i := 0; i < objects.MaxRoutePoints; i++ {
		p, err := geo.FromDegrees(59.3+float64(i)*0.01, 18.0)
		if err != nil {
			t.Fatal(err)
		}
		route.Path = append(route.Path, p)
	}
	if _, _, err := rt.CreateObject(tid, route); err != nil {
		t.Fatalf("a route at the measured bound must be creatable: %v", err)
	}
	geoCount := 0
	for _, o := range sp.State.Objects() {
		if o.Record.Geo != nil || len(o.Record.Path) > 0 {
			geoCount++
		}
	}
	if geoCount != 2 {
		t.Fatalf("geo-bearing objects: %d", geoCount)
	}
}

func TestFieldEmitsBehindCanWrite(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Field")
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.spaceForTest(tid)
	sp.ReadOnly = true
	pt := fieldPt(t)
	refused := func(name string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "join this space") {
			t.Fatalf("%s not refused: %v", name, err)
		}
	}
	refused("SetPosition", rt.SetPosition(tid, pt, 0, 0))
	_, err = rt.PlaceMarker(tid, "hazard", "", pt, nil, 0)
	refused("PlaceMarker", err)
	_, err = rt.SendCheckin(tid, "", nil, 0, false, true)
	refused("SendCheckin", err)
}

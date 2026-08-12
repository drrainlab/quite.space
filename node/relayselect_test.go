// Which relay a FRESH INSTALL ends up on, and how it gets there.
//
// Everything here comes from one beta report — "the official relay was not
// picked by default after installing", seen twice — and from the screenshot
// that came with it: a quicklink refused with "nothing waiting under those
// words … or been meant for a different relay". That message was telling the
// literal truth. Four separate things had to be right for a new phone to land
// on the shared relay, and three of them were not.
package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// The subject is THE SHIPPED REGISTRY, so it is the shipped one that is read:
// the suite runs against an empty registry on purpose (see TestMain).
//
// A relay on the developer's own machine is a relay nobody else is at, and it
// wins on measured RTT against every relay on earth. Automatic mode must not
// be able to choose it.
func TestTheDevelopmentRelayIsNeverChosenForAnybody(t *testing.T) {
	cands := autoCandidates(shippedRelayRegistry)
	if len(cands) == 0 {
		t.Fatal("no automatic candidates at all — a fresh install would reach nothing")
	}
	for _, d := range cands {
		if d.ID == "local-dev" || d.LocalLAN() {
			t.Errorf("%s (%s) is offered to automatic selection; a relay for whoever "+
				"stood it up must be named deliberately, never picked for somebody",
				d.ID, d.Endpoint)
		}
	}

	// And it is still THERE: local-dev stays resolvable for a development
	// build and for Custom mode. Unselectable is not the same as absent.
	if _, ok := shippedRelayRegistry.ByID("local-dev"); !ok {
		t.Error("local-dev is gone from the registry — it should be unselectable, not unresolvable")
	}
}

// The shipped registry has to be able to answer a fresh install at all. When
// the regional relays land beside staging-1, this is what says each of them
// is reachable by a new device rather than merely listed.
func TestShippedRegistryOffersAPinnedOfficialRelay(t *testing.T) {
	for _, d := range autoCandidates(shippedRelayRegistry) {
		if d.Official && len(d.SPKIPins) > 0 {
			return
		}
	}
	t.Fatal("no pinned official relay is selectable — a new device has nowhere to meet anybody")
}

// The shape the shipped set has to keep as regions are added.
//
// Ids and endpoints must be unique — a duplicate id would make
// `official:<id>` ambiguous in signed policy, and two rows for one endpoint
// would have it probed twice and scored twice — and there must be relays in
// more than one region, because the backup rule prefers a DIFFERENT one and
// two relays in a single failure domain are one relay with extra steps.
func TestTheShippedSetSpansRegionsAndDoesNotRepeatItself(t *testing.T) {
	ids := map[string]bool{}
	endpoints := map[string]bool{}
	regions := map[string]bool{}
	for _, d := range shippedRelayRegistry.Relays {
		if ids[d.ID] {
			t.Errorf("two registry rows share the id %q — official:%s is ambiguous", d.ID, d.ID)
		}
		ids[d.ID] = true
		if endpoints[d.Endpoint] {
			t.Errorf("two registry rows share the endpoint %q", d.Endpoint)
		}
		endpoints[d.Endpoint] = true
	}
	for _, d := range autoCandidates(shippedRelayRegistry) {
		regions[d.Region] = true
	}
	if len(regions) < 2 {
		t.Errorf("every selectable relay is in region %v — there is no different "+
			"region for a backup to come from", regions)
	}
}

// Every selectable official relay must be pinned. An unpinned public entry
// would be trusted on first sight, which is the one thing the registry's
// SPKI pin sets exist to prevent.
func TestEverySelectableOfficialRelayIsPinned(t *testing.T) {
	for _, d := range autoCandidates(shippedRelayRegistry) {
		if d.Official && len(d.SPKIPins) == 0 && !d.LocalLAN() {
			t.Errorf("%s (%s) is official, selectable and unpinned", d.ID, d.Endpoint)
		}
	}
}

// The retry ladder: fast enough that a phone whose Wi-Fi is still associating
// catches up in seconds, bounded so an afternoon offline is not a probe storm.
func TestReselectBackoffClimbsAndIsBounded(t *testing.T) {
	if got := reselectBackoff(0); got != reselectFirstWait {
		t.Errorf("first retry waits %v, want %v", got, reselectFirstWait)
	}
	prev := time.Duration(0)
	for i := 0; i < 12; i++ {
		got := reselectBackoff(i)
		if got > reselectMaxWait {
			t.Fatalf("attempt %d waits %v, past the %v ceiling", i, got, reselectMaxWait)
		}
		if got < prev {
			t.Fatalf("attempt %d waits %v, less than the previous %v", i, got, prev)
		}
		prev = got
	}
	if prev != reselectMaxWait {
		t.Errorf("the ladder settles at %v, want the %v ceiling", prev, reselectMaxWait)
	}
	// Ten minutes of a network that is not there must cost a bounded number
	// of measurement rounds, not one every couple of seconds.
	var elapsed, rounds time.Duration
	for i := 0; elapsed < 10*time.Minute; i++ {
		elapsed += reselectBackoff(i)
		rounds++
	}
	if rounds > 15 {
		t.Errorf("%v rounds of probing in ten offline minutes — too eager", rounds)
	}
}

// THE FRESH-INSTALL READING OF THE MODE. A new node stores "" and means
// automatic. Three places compared the stored string to "automatic" instead
// of asking relayIsAutomatic, and each got the fresh install exactly wrong.
func TestFreshInstallIsAutomaticEverywhereItIsAsked(t *testing.T) {
	fresh := Settings{} // nothing chosen yet: RelayMode "", Relay ""
	if !relayIsAutomatic(fresh) {
		t.Fatal("a fresh install must read as automatic — everything below follows from this")
	}
	// Someone who typed an address means it, even without setting the mode.
	if relayIsAutomatic(Settings{Relay: "203.0.113.7:7411"}) {
		t.Error("an address with no mode is a custom choice, not an automatic one")
	}
	if !relayIsAutomatic(Settings{RelayMode: "automatic", Relay: "203.0.113.7:7411"}) {
		t.Error("an explicit automatic mode wins over a leftover address")
	}
	if relayIsAutomatic(Settings{RelayMode: "custom"}) {
		t.Error("custom is custom even with no address yet")
	}
}

func TestFreshInstallReportsAutomaticInDiagnostics(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "diag")
	defer rt.Close()

	d := rt.RelayDiagnosticsSnapshot()
	if d.Mode != "automatic" {
		t.Errorf("a fresh install reports mode %q — the one screen somebody opens to "+
			"find out why the relay is not working said the selection had never run", d.Mode)
	}
}

// PersonalRelayRef is signed into a new public space's policy, so getting it
// wrong is not a display bug: the space goes out naming nowhere, and the link
// somebody hands round resolves to nothing on the other side.
func TestFreshInstallSignsAMeasuredRelayIntoANewSpace(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "ref")
	defer rt.Close()

	// Nothing measured yet — honest emptiness, and the case that used to be
	// the ONLY case a fresh device could produce.
	if got := rt.PersonalRelayRef(); got != "" {
		t.Fatalf("before any measurement the ref should be empty, got %q", got)
	}

	// Once selection lands, the space is signed with the official ref — the
	// registry id, so the relay can move hosts without every space revising
	// its policy.
	want := RelayRef{Official: "staging-1"}.String()
	if err := rt.updateRelayState(func(ls *RelayLocalState) {
		ls.SelectedPrimary = want
		ls.LastSelectionUnix = int64(nowUnix())
	}); err != nil {
		t.Fatal(err)
	}
	if got := rt.PersonalRelayRef(); got != want {
		t.Errorf("a space created on a fresh install is signed with %q, want %q — "+
			"an empty ref is a space with nowhere for its traffic to go", got, want)
	}
}

// A SELECTED RELAY THAT DIES. The background sync loop is pointed at one
// address when it starts and never re-resolves it, so before this the node
// went on talking to a corpse: the measured backup sat in relays.json and
// nothing ever moved the personal inbox onto it. One reachable relay in the
// registry hid that completely; a set spread over regions will not.
func TestAFallenRelayIsReplacedWithoutARestart(t *testing.T) {
	host := openRuntime(t, t.TempDir(), "host")
	defer host.Close()
	first, firstAddr := setUpRelay(t, host)
	second, secondAddr := setUpRelay(t, host)
	// Closed ONCE, whichever one the test kills: relayserver.Server panics on a
	// second Close, so a plain defer beside an explicit kill made this test
	// fail or pass depending on which relay measured faster.
	var closeOnce [2]bool
	shut := func(i int, s *relayserver.Server) {
		if !closeOnce[i] {
			closeOnce[i] = true
			s.Close()
		}
	}
	defer func() { shut(0, first); shut(1, second) }()

	withRelayRegistry(t,
		RelayDescriptor{
			ID: "one", Endpoint: firstAddr, Region: "a", Priority: 10,
			ProtocolMin: 1, ProtocolMax: 1, Official: true,
			Roles: []string{RelayRoleBootstrap, RelayRolePersonalInbox},
		},
		RelayDescriptor{
			ID: "two", Endpoint: secondAddr, Region: "b", Priority: 10,
			ProtocolMin: 1, ProtocolMax: 1, Official: true,
			Roles: []string{RelayRoleBootstrap, RelayRolePersonalInbox},
		},
	)

	rt := openRuntime(t, t.TempDir(), "traveller")
	defer rt.Close()

	// Whichever it measured as better, it must have landed on one of them.
	var chosen string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && chosen == "" {
		if a := rt.relaySyncAddr(); a != "" {
			chosen = a
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if chosen != firstAddr && chosen != secondAddr {
		t.Fatalf("selection landed on %q, which is neither relay", chosen)
	}

	// Kill the one it chose and let the pool notice.
	survivor := secondAddr
	if chosen == secondAddr {
		survivor = firstAddr
		shut(1, second)
	} else {
		shut(0, first)
	}

	// Drive the pool into cooldown the way ordinary traffic would: the loop
	// itself is doing this every couple of seconds, but the test must not
	// wait on its cadence to be sure the health word has turned.
	for i := 0; i < poolUnhealthyAfter+1; i++ {
		_ = rt.withRelayControl(chosen, func(c *relay.Client) error {
			_, _, err := c.Time()
			return err
		})
	}
	if got := rt.pool().health(chosen); got != "offline" {
		t.Fatalf("the fallen relay reads as %q — the premise of the test is not set up", got)
	}
	if !rt.relayIsSick() {
		t.Fatal("a relay in cooldown is not reported as sick, so nothing will re-measure")
	}

	// The watcher's own period is a minute, which no test should sit through
	// — what is asserted is that a re-measurement MOVES the loop, which is
	// the part that was missing.
	if primary, _ := rt.runAutoSelection(); primary != "" {
		ref, err := ParseRelayRef(primary)
		if err != nil {
			t.Fatal(err)
		}
		ep, ok := ref.Resolve(BuiltinRelayRegistry())
		if !ok {
			t.Fatalf("re-selection produced %q, which does not resolve", primary)
		}
		if ep != survivor {
			t.Errorf("re-selection stayed on %q with it dead; the survivor is %q", ep, survivor)
		}
	} else {
		t.Error("re-selection found nothing while a healthy relay was up")
	}
}

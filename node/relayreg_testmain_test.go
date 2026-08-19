// The test suite does not dial the internet.
//
// A fresh node measures now (relayIsAutomatic), and a test node is by
// definition fresh — so every one of the hundreds of runtimes this package
// opens probes the registry at startup. Verified before writing this: a
// plain openRuntime reached both official:local-dev and official:staging-1
// within four seconds, and staging-1 is a relay on somebody's VPS.
//
// So the suite runs against an EMPTY registry: nothing to probe, nothing
// to dial, and the same wall-clock on every machine.
//
// Filtering to loopback instead was tried and rejected on measurement. A
// developer with a relay listening on 7411 — the ordinary state while
// working on this — turned every one of those startup probes into a real
// TLS handshake against a real server, and the package went from 474s to
// past the 600s timeout. A suite whose duration depends on what happens to
// be running on the machine is a suite that fails for reasons nobody can
// reproduce.
//
// A test that needs a registry installs the one it means: withRelayRegistry
// for a name that must merely RESOLVE, or the relay it actually stood up
// when it needs a measurement (see relayauto_test.go).
package node

import (
	"os"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/quicklink"
)

// shippedRelayRegistry is what the binary carries, kept for the one test
// whose subject IS the shipped registry.
var shippedRelayRegistry = shippedBuiltinRegistry

func TestMain(m *testing.M) {
	empty := BuiltinRelayRegistry()
	empty.Relays = nil
	setBuiltinRelayRegistry(empty)

	// THE HEARTBEAT IS FASTER HERE — see cadence.go for the measurement
	// that justifies it and the two rules that keep it honest. Set before a
	// single goroutine exists.
	//
	// WHY 200ms. The floor is scheduler noise: 5ms was tried and
	// relaySyncOnce could not finish inside a tick, so the loop fell over
	// itself and the suite got slower. 200ms is ten times the shipped beat
	// and comfortably above that floor.
	//
	// A faster beat also changes WHICH races a test can lose, and that is a
	// feature: TestRelayOutageDoesNotDropOrDuplicateIngress went red here
	// and, after every wrong theory had been ruled out by instruments — pool
	// backoff, relay rate limit, ingress backpressure, all measured healthy —
	// the stranded letter turned out to be addressed to the gateway's OWN
	// device: a real delivery bug in deliverSpaceRouted that the shipped beat
	// had hidden by always letting the owner's frames arrive first.
	//
	// One value for plain and -race builds alike. A slower beat under the
	// detector was tried (500ms) on the theory that instrumented ticks were
	// the cost: no better. The cost under -race was never the ticking; it was
	// scrypt, handled below.
	cadence = 200 * time.Millisecond

	// AND THE KEY DERIVATION IS CHEAP HERE. Under -race a quick-link scrypt
	// costs ~7s instead of ~0.4s, and this suite mints and resolves links
	// hundreds of times: 363 of 601 tests were more than twice as slow under
	// the detector, and no cadence — 200ms or 500ms, both measured — moved
	// that number, because it is compute rather than waiting. The shipped
	// parameters are asserted by quicklink's own TestKeyDerivationIsActuallySlow.
	restoreKDF := quicklink.TestKDFParams(1<<10, 8, 1)

	code := m.Run()
	restoreKDF()
	os.Exit(code)
}

// TestTheShippedCadenceIsTwoSeconds is what lets TestMain lower the beat
// without lowering it for people: the shipped number is a constant, read
// here directly, and this is the one place it is asserted.
//
// IT READS AND NEVER WRITES. An earlier form set cadence back to the
// shipped value for the duration of the test, and the race detector caught
// it: a node from the PREVIOUS test was still ticking, still reading the
// variable, while this one wrote it. The rule cadence.go states — written
// once in TestMain, never again — is the rule this test lives under too.
func TestTheShippedCadenceIsTwoSeconds(t *testing.T) {
	if shippedCadence != 2*time.Second {
		t.Fatalf("the shipped background cadence is %v, not 2s", shippedCadence)
	}
	// relayInterval speaks in seconds at the shipped cadence — checked by
	// arithmetic on the constant, not by installing it.
	if got := relayIntervalAt(shippedCadence, Settings{}); got != 2*time.Second {
		t.Fatalf("an unconfigured node syncs every %v, not 2s", got)
	}
	if got := relayIntervalAt(shippedCadence, Settings{RelaySyncSeconds: 30}); got != 30*time.Second {
		t.Fatalf("a node asked for 30s syncs every %v", got)
	}
}

// withRelayRegistry installs descriptors for the duration of one test. For
// a test that only needs a name to RESOLVE, nothing is dialled — Resolve is
// a lookup.
func withRelayRegistry(t *testing.T, ds ...RelayDescriptor) {
	t.Helper()
	saved := BuiltinRelayRegistry()
	t.Cleanup(func() { setBuiltinRelayRegistry(saved) })
	reg := saved
	reg.Relays = ds
	setBuiltinRelayRegistry(reg)
}

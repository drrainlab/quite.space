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
)

// shippedRelayRegistry is what the binary carries, kept for the one test
// whose subject IS the shipped registry.
var shippedRelayRegistry = shippedBuiltinRegistry

func TestMain(m *testing.M) {
	empty := BuiltinRelayRegistry()
	empty.Relays = nil
	setBuiltinRelayRegistry(empty)
	os.Exit(m.Run())
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

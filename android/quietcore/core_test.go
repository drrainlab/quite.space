// AR-1a's Go-side invariant: STATUS IS ANSWERABLE WHILE THE CORE IS OPENING.
//
// node.Open is seconds on a phone — scrypt plus a full log replay; AR-0
// measured 2.6 s for a 16 000-event log. The first version of this binding
// held ONE mutex across both the open and the status read, so for that whole
// window a host asking "what is the runtime doing" simply blocked. A component
// that cannot see the difference between "opening" and "dead" will guess, and
// it will guess wrong in the direction that offers to start a node which is
// already halfway up.
//
// So there are two locks, and this is the test that keeps them two.
package quietcore

import (
	"encoding/json"
	"testing"
	"time"
)

func statusState(t *testing.T) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(Status()), &m); err != nil {
		t.Fatalf("status is not JSON: %v", err)
	}
	s, _ := m["state"].(string)
	return s
}

func TestStatusAnswersWhileTheCoreIsOpening(t *testing.T) {
	if got := statusState(t); got != StateUnavailable {
		t.Fatalf("before anything: state %q, want %q", got, StateUnavailable)
	}

	dir := t.TempDir()
	done := make(chan error, 1)
	go func() { done <- Start(dir, "ar1a-test-passphrase", "test", false) }()

	// THE ASSERTION IS THAT WE SEE "opening", and that is deliberate.
	//
	// The first version of this test only bounded how long Status may block,
	// and it did not fail when the two locks were merged back into one — an
	// open over an empty directory is ~80 ms here, comfortably inside any
	// timing bound. A test that cannot fail is not a test.
	//
	// Seeing the state IS the discriminator: with one lock, Status cannot
	// return until the open completes, so "opening" is unobservable by
	// construction and this assertion is impossible to satisfy. With two, the
	// poll loop runs thousands of times inside that ~80 ms window.
	sawOpening := false
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			if got := statusState(t); got != StateAlive {
				t.Fatalf("after a successful start: state %q, want %q", got, StateAlive)
			}
			if !sawOpening {
				t.Error("never observed state=opening while an open was in " +
					"flight — Status is answering only after the operation " +
					"finishes, so a host cannot tell 'opening' from 'dead'")
			}
			if err := Stop(); err != nil {
				t.Fatalf("stop: %v", err)
			}
			if got := statusState(t); got != StateUnavailable {
				t.Fatalf("after stop: state %q, want %q", got, StateUnavailable)
			}
			return
		default:
		}

		t0 := time.Now()
		st := statusState(t)
		if d := time.Since(t0); d > 250*time.Millisecond {
			t.Fatalf("Status blocked for %v during an open — the state lock is "+
				"being held across the operation again, and a host cannot tell "+
				"'opening' from 'dead' for as long as that lasts", d)
		}
		if st == StateOpening {
			sawOpening = true
		}
		if time.Now().After(deadline) {
			t.Fatal("the open never finished")
		}
	}
}

// A second owner is refused, and the refusal names the directory. The data-dir
// lock is the real defence, but it must never be REACHED from inside this
// process: two runtimes on one directory is what the lock exists to prevent,
// and the binding must not be the thing that provokes it.
func TestASecondStartIsRefusedRatherThanOpeningATwinRuntime(t *testing.T) {
	dir := t.TempDir()
	if err := Start(dir, "ar1a-test-passphrase", "test", false); err != nil {
		t.Fatal(err)
	}
	defer Stop()

	before := statusState(t)
	if err := Start(dir, "ar1a-test-passphrase", "test", false); err == nil {
		t.Fatal("a second Start was allowed — there are now two owners")
	}
	if got := statusState(t); got != before {
		t.Errorf("a refused Start disturbed the state: %q -> %q", before, got)
	}
	if got := statusState(t); got != StateAlive {
		t.Errorf("state %q after a refused second start, want %q — the running "+
			"node must be unharmed by somebody else's mistake", got, StateAlive)
	}
}

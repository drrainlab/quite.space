package node

import (
	"fmt"
	"sync"
	"testing"
)

// DisplayName used to read r.Self.Manifest with no lock while SetName
// swapped that pointer under r.mu (Participant.Rename copies the manifest
// and replaces the pointer — terminals/persist.go). A pointer-word read
// racing a pointer-word write is undefined behaviour under the Go memory
// model, however innocent it looks, and -race is the arbiter here: this
// test is nothing but the two paths run against each other. It was red
// against the unlocked body before the fix landed.
//
// Every identity read the desktop shell and the Live Plane will add goes
// through the same accessor, which is why this closes now rather than when
// one of them trips it.
func TestRenamingWhileReadingTheNameIsNotARace(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := rt.SetName(fmt.Sprintf("alice-%d", i%7)); err != nil {
				t.Errorf("rename: %v", err)
				return
			}
		}
	}()

	// The reader bounds the test: 2000 reads, then the writer is released.
	for i := 0; i < 2000; i++ {
		if got := rt.DisplayName(); got == "" {
			t.Error("DisplayName returned empty mid-rename")
			break
		}
	}
	close(stop)
	wg.Wait()
}

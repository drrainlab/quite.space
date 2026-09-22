package storage

import (
	"sync"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// Two collects can land the same bytes at the same moment (the cycle's
// pull and the doorbell's, each carrying a copy a different member
// re-offered). Content addressing makes them one file — they must never
// make each other fail, because a failed Put reads as a dead disk upstream
// and halts collection for good.
func TestConcurrentPutsOfTheSameBytesNeverFail(t *testing.T) {
	root, err := Open(t.TempDir(), []byte("a passphrase long enough"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := root.OpenIngressHold(16)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("the same item, collected twice at once")
	meta := HeldIngressMeta{ReceivedAt: 1, Source: IngressRelay}
	for round := 0; round < 50; round++ {
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := h.Put(raw, meta); err != nil {
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: a concurrent Put of identical bytes failed: %v", round, err)
		}
		hid := id.HashOf(raw)
		if err := h.Delete(hid); err != nil {
			t.Fatal(err)
		}
	}
}

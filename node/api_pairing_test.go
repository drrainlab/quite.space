package node

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// The whole seam a screen drives, end to end: begin → offer → the child
// dials out of band → digits appear in the poll → approve → done — and the
// devices list then shows two devices, with revoke wired to the same verb
// the tests above prove.
func TestPairingAPIDrivesTheWholeCeremony(t *testing.T) {
	old := pairingBind
	pairingBind = func() string { return "127.0.0.1:0" }
	t.Cleanup(func() { pairingBind = old })
	parent := openRuntime(t, t.TempDir(), "alice")
	defer parent.Close()
	api, err := NewAPIServer(parent, nil)
	if err != nil {
		t.Fatal(err)
	}

	get := func(h func(w *httptest.ResponseRecorder)) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		h(w)
		var m map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatalf("bad json: %v\n%s", err, w.Body.String())
		}
		return m
	}

	begin := get(func(w *httptest.ResponseRecorder) {
		api.handleBeginPairing(w, httptest.NewRequest("POST", "/api/pairing", nil))
	})
	offer, err := base64.StdEncoding.DecodeString(begin["offer"].(string))
	if err != nil || len(offer) == 0 {
		t.Fatalf("no offer in the begin response: %v", begin)
	}

	childDir := t.TempDir()
	childErr := make(chan error, 1)
	go func() {
		childErr <- JoinAsPairedDevice(childDir, []byte("test passphrase"), offer,
			func(string) bool { return true }, uint64(time.Now().Unix()))
	}()

	// The screen polls until the digits arrive.
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := get(func(w *httptest.ResponseRecorder) {
			api.handlePairingStatus(w, httptest.NewRequest("GET", "/api/pairing", nil))
		})
		if st["stage"] == "digits" {
			if len(st["digits"].(string)) != 6 {
				t.Fatalf("stage digits without six digits: %v", st)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("digits never arrived: %v", st)
		}
		time.Sleep(50 * time.Millisecond)
	}

	get(func(w *httptest.ResponseRecorder) {
		api.handleApprovePairing(w, httptest.NewRequest("POST", "/api/pairing/approve", nil))
	})
	if err := <-childErr; err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		st := get(func(w *httptest.ResponseRecorder) {
			api.handlePairingStatus(w, httptest.NewRequest("GET", "/api/pairing", nil))
		})
		if st["stage"] == "done" {
			break
		}
		if st["stage"] == "failed" || time.Now().After(deadline) {
			t.Fatalf("approval never completed: %v", st)
		}
		time.Sleep(50 * time.Millisecond)
	}

	devs := get(func(w *httptest.ResponseRecorder) {
		api.handleDevices(w, httptest.NewRequest("GET", "/api/devices", nil))
	})
	if got := len(devs["devices"].([]any)); got != 2 {
		t.Fatalf("devices after pairing = %d, want 2", got)
	}
}

package node

// THE FEED POLL MUST BE ALLOWED TO SAY "NOTHING CHANGED". The interface
// asks for /entries every two seconds; before the ETag the answer was
// the whole room every time — every message, every inline preview
// re-encoded to base64, 24 MB over one sitting in a measured session —
// and the phone's webview parsed all of it just to conclude nothing
// moved. The tag is a hash of the exact bytes the poll would receive,
// deliberately NOT of the log head: an asset can finish fetching
// without the log growing, and a log-keyed tag would freeze the screen
// at "fetching…" forever.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

func TestEntriesAnswerNotModifiedUntilSomethingChanges(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("room")
	if err != nil {
		t.Fatal(err)
	}
	say := func(text string) {
		t.Helper()
		payload, err := (&schemas.TextMessage{Text: text}).Encode()
		if err != nil {
			t.Fatal(err)
		}
		if err := rt.withSpace(tid, func(st *spaceState) error {
			_, err := rt.Self.Emit(st.space, schemas.MessageText,
				payload, signal.AuthorshipHuman, 100)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	say("first")

	api, err := NewAPIServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()

	get := func(ifNoneMatch string) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+"/api/spaces/"+tid.Hex()+"/entries", nil)
		req.Header.Set("X-QP-Token", api.Token())
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, body
	}

	resp, body := get("")
	if resp.StatusCode != 200 || len(body) == 0 {
		t.Fatalf("first read: %d, %d bytes", resp.StatusCode, len(body))
	}
	tag := resp.Header.Get("ETag")
	if tag == "" {
		t.Fatal("the poll got no tag to come back with")
	}

	// The steady state: the same world answers with zero bytes.
	resp, body = get(tag)
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("unchanged room answered %d, not 304", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Fatalf("a 304 carried %d bytes", len(body))
	}

	// And the moment anything changes, the stale tag stops matching.
	say("second")
	resp, body = get(tag)
	if resp.StatusCode != 200 {
		t.Fatalf("changed room answered %d to a stale tag", resp.StatusCode)
	}
	if len(body) == 0 {
		t.Fatal("the changed room came back empty")
	}
	if newTag := resp.Header.Get("ETag"); newTag == "" || newTag == tag {
		t.Fatalf("the tag did not move with the content: %q → %q", tag, newTag)
	}
}

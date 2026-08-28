package node

// The two properties a retrying sender stands on: the same client_ref
// never mints twice, and API answers are never cacheable — a heuristically
// cached feed is frozen while every poll "succeeds".

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSayIsIdempotentPerClientRef(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("the workshop")
	if err != nil {
		t.Fatal(err)
	}
	api, err := NewAPIServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := api.Handler()

	post := func(body string) map[string]string {
		req := httptest.NewRequest("POST", "/api/spaces/"+tid.Hex()+"/messages",
			strings.NewReader(body))
		req.Header.Set("X-QP-Token", api.token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("say answered %d: %s", rec.Code, rec.Body)
		}
		var out map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// The double-press, as the wire sees it: one ref, two requests.
	first := post(`{"text":"написал один раз","client_ref":"ref-1"}`)
	second := post(`{"text":"написал один раз","client_ref":"ref-1"}`)
	if first["id"] == "" || first["id"] != second["id"] {
		t.Fatalf("one ref minted two events: %q vs %q", first["id"], second["id"])
	}
	if got := countMsg(t, rt, tid, "написал один раз"); got != 1 {
		t.Fatalf("the log holds %d copies, want exactly 1", got)
	}

	// A different ref is a different message, even with the same words.
	third := post(`{"text":"написал один раз","client_ref":"ref-2"}`)
	if third["id"] == first["id"] {
		t.Fatal("a fresh ref was answered with a stale event")
	}
	if got := countMsg(t, rt, tid, "написал один раз"); got != 2 {
		t.Fatalf("the log holds %d copies, want 2", got)
	}

	// And no ref at all keeps the old contract: every request mints.
	post(`{"text":"без рефа"}`)
	post(`{"text":"без рефа"}`)
	if got := countMsg(t, rt, tid, "без рефа"); got != 2 {
		t.Fatalf("unref'd requests were deduped: %d", got)
	}
}

func TestAPIAnswersAreNeverCacheable(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("the workshop")
	if err != nil {
		t.Fatal(err)
	}
	api, err := NewAPIServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := api.Handler()

	for _, path := range []string{"/api/status", "/api/spaces", "/api/spaces/" + tid.Hex() + "/entries"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-QP-Token", api.token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s answered %d", path, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s carries Cache-Control %q — an ETag without a policy is heuristically cacheable", path, cc)
		}
	}

	// An error is an answer too, and a cached error is a stuck screen.
	req := httptest.NewRequest("GET", "/api/spaces/zzzz/entries", nil)
	req.Header.Set("X-QP-Token", api.token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == 200 {
		t.Fatal("a bad space id was accepted")
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("error answers carry Cache-Control %q", cc)
	}
}

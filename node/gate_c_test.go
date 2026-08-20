package node

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func postMultipart(t *testing.T, url, token, path string, meta map[string]any,
	preview, file []byte) (*http.Response, map[string]string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mj, _ := json.Marshal(meta)
	fw, _ := mw.CreateFormField("metadata")
	fw.Write(mj)
	if preview != nil {
		pw, _ := mw.CreateFormFile("preview", "preview.webp")
		pw.Write(preview)
	}
	if file != nil {
		fw2, _ := mw.CreateFormFile("file", "content.bin")
		fw2.Write(file)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", url+path, &body)
	req.Header.Set("X-QP-Token", token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	return resp, out
}

func TestBlocksAPIEndToEnd(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	api, err := NewAPIServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()

	tid, err := rt.CreateSpace("Media API")
	if err != nil {
		t.Fatal(err)
	}
	sid := tid.Hex()

	call := func(method, path string, body any, out any) int {
		t.Helper()
		var rd bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = *bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, &rd)
		req.Header.Set("X-QP-Token", api.Token())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}

	// Upload a visual block: preview + 100KB file.
	content := randBytes(t, 100_000)
	preview := []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3} // jpeg-ish bytes; server does not sniff
	resp, created := postMultipart(t, srv.URL, api.Token(),
		"/api/spaces/"+sid+"/blocks",
		map[string]any{"kind": "visual", "size": len(content), "media_type": "image/webp",
			"alt": "test picture", "caption": "north ridge", "preview_mime": "image/webp",
			"chunk_kib": 16},
		preview, content)
	if resp.StatusCode != 200 {
		t.Fatalf("visual upload: %d %v", resp.StatusCode, created)
	}

	// Entries: the visual appears with thumb inline and complete asset.
	var entries []entryResp
	if code := call("GET", "/api/spaces/"+sid+"/entries", nil, &entries); code != 200 {
		t.Fatalf("entries: %d", code)
	}
	if len(entries) != 1 || entries[0].Kind != "visual" || entries[0].Alt != "test picture" {
		t.Fatalf("entries wrong: %+v", entries)
	}
	if entries[0].Asset == nil || entries[0].Asset.State != "complete" {
		t.Fatalf("asset state wrong: %+v", entries[0].Asset)
	}
	if entries[0].ThumbB64 == "" || entries[0].Fallback != "test picture" {
		t.Fatal("thumb or fallback missing")
	}

	// Download the original: content matches, headers honest.
	req, _ := http.NewRequest("GET", srv.URL+"/api/spaces/"+sid+"/assets/"+entries[0].Asset.ID, nil)
	req.Header.Set("X-QP-Token", api.Token())
	dresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer dresp.Body.Close()
	if dresp.StatusCode != 200 || dresp.Header.Get("X-Content-Type-Options") != "nosniff" ||
		dresp.Header.Get("Content-Type") != "image/webp" {
		t.Fatalf("download headers: %d %v", dresp.StatusCode, dresp.Header)
	}
	var got bytes.Buffer
	got.ReadFrom(dresp.Body)
	if !bytes.Equal(got.Bytes(), content) {
		t.Fatal("downloaded content differs")
	}

	// Resonance: set → aggregate + own; clear → gone (single register).
	target := entries[0].ID
	if code := call("POST", "/api/spaces/"+sid+"/reactions",
		map[string]any{"target": target, "op": "set",
			"reaction": map[string]any{"kind": "unicode", "value": "🌲"}}, nil); code != 200 {
		t.Fatal("resonance set failed")
	}
	var withReaction []entryResp
	call("GET", "/api/spaces/"+sid+"/entries", nil, &withReaction)
	res := withReaction[0].Resonance
	if res == nil || res.Total != 1 || len(res.Groups) != 1 ||
		res.Groups[0].Value != "🌲" || res.Own == nil || res.Own.Value != "🌲" {
		t.Fatalf("resonance projection: %+v", res)
	}
	if code := call("POST", "/api/spaces/"+sid+"/reactions",
		map[string]any{"target": target, "op": "clear"}, nil); code != 200 {
		t.Fatal("resonance clear failed")
	}
	var afterUnset []entryResp
	call("GET", "/api/spaces/"+sid+"/entries", nil, &afterUnset)
	if r := afterUnset[0].Resonance; r != nil && (r.Total != 0 || r.Own != nil) {
		t.Fatalf("resonance not cleared: %+v", r)
	}

	// Live signal: few hundred bytes, no asset.
	resp, _ = postMultipart(t, srv.URL, api.Token(), "/api/spaces/"+sid+"/blocks",
		map[string]any{"kind": "live_signal", "preset": "slow-pines@1", "seed": 173991,
			"params":   map[string]int{"density": 420, "wind": 180},
			"fallback": "~ slow-pines signal · density 42%"},
		nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("signal post: %d", resp.StatusCode)
	}
	var withSignal []entryResp
	call("GET", "/api/spaces/"+sid+"/entries", nil, &withSignal)
	if len(withSignal) != 2 || withSignal[1].Kind != "live_signal" || withSignal[1].Preset != "slow-pines@1" {
		t.Fatalf("signal entry wrong: %+v", withSignal[len(withSignal)-1])
	}

	// Guardrails: oversized declared size refused before reading the body;
	// dangerous link scheme refused; missing alt refused.
	resp, _ = postMultipart(t, srv.URL, api.Token(), "/api/spaces/"+sid+"/blocks",
		map[string]any{"kind": "file", "size": int64(1) << 40, "filename": "x"}, nil, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("oversize accepted: %d", resp.StatusCode)
	}
	resp, _ = postMultipart(t, srv.URL, api.Token(), "/api/spaces/"+sid+"/blocks",
		map[string]any{"kind": "link", "url": "javascript:alert(1)"}, nil, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("javascript url accepted: %d", resp.StatusCode)
	}
	resp, _ = postMultipart(t, srv.URL, api.Token(), "/api/spaces/"+sid+"/blocks",
		map[string]any{"kind": "visual", "size": 10, "media_type": "image/webp"}, nil, []byte("0123456789"))
	if resp.StatusCode != 400 {
		t.Fatalf("visual without alt accepted: %d", resp.StatusCode)
	}
}

func TestAssetGet409WhenMissing(t *testing.T) {
	rtA := openRuntime(t, t.TempDir(), "alice")
	defer rtA.Close()
	rtB := openRuntime(t, t.TempDir(), "bob")
	defer rtB.Close()

	tid, err := rtA.CreateSpace("Partial")
	if err != nil {
		t.Fatal(err)
	}
	ref := emitVisual(t, rtA, tid, randBytes(t, 200_000), 4096)

	invite, _ := rtA.MintInvite(tid, rtB.Device.ID, rtB.Device.X25519Pub)
	if _, err := rtB.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	// Sync events only (no assets): direct LAN, then query B's API.
	if err := rtA.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if err := rtB.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if err := rtB.ConnectPeer(fmt.Sprintf("127.0.0.1:%d", rtA.LAN().Port)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := rtB.AssetStatus(tid, ref.PublicIDHex()); return err == nil })

	api, err := NewAPIServer(rtB, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET",
		srv.URL+"/api/spaces/"+tid.Hex()+"/assets/"+ref.PublicIDHex(), nil)
	req.Header.Set("X-QP-Token", api.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 for missing asset, got %d", resp.StatusCode)
	}
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	if body["state"] != "manifest_missing" {
		t.Fatalf("honest state missing: %v", body)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never became true")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A PICTURE SENT AS AN ANSWER (block.visual.v1 key 7). The reply edge rides
// the multipart metadata, survives the log, and comes back on the entry the
// way a text reply's does — so the feed's existing quote header lights up
// with no renderer change. The second post proves absence stays absence.
func TestVisualBlockCarriesAReplyEdge(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	api, err := NewAPIServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()
	tid, err := rt.CreateSpace("answers in pictures")
	if err != nil {
		t.Fatal(err)
	}
	sid := tid.Hex()

	questionID, err := rt.Say(tid, "what does the ridge look like at dawn?", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}

	content := bytes.Repeat([]byte{0xAB}, 2048)
	resp, created := postMultipart(t, srv.URL, api.Token(), "/api/spaces/"+sid+"/blocks",
		map[string]any{"kind": "visual", "size": len(content), "media_type": "image/webp",
			"alt": "foggy pines", "reply_to": questionID.Hex()},
		nil, content)
	if resp.StatusCode != 200 {
		t.Fatalf("visual reply upload: %d %v", resp.StatusCode, created)
	}

	// And a picture that answers nobody, to prove the edge never leaks.
	resp, _ = postMultipart(t, srv.URL, api.Token(), "/api/spaces/"+sid+"/blocks",
		map[string]any{"kind": "visual", "size": len(content), "media_type": "image/webp",
			"alt": "just a photo"},
		nil, content)
	if resp.StatusCode != 200 {
		t.Fatalf("plain visual upload: %d", resp.StatusCode)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/api/spaces/"+sid+"/entries", nil)
	req.Header.Set("X-QP-Token", api.Token())
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var entries []entryResp
	json.NewDecoder(r.Body).Decode(&entries)
	r.Body.Close()
	if len(entries) != 3 {
		t.Fatalf("entries: %d, want 3", len(entries))
	}
	byAlt := map[string]entryResp{}
	for _, e := range entries {
		byAlt[e.Alt] = e
	}
	if got := byAlt["foggy pines"].ReplyTo; got != questionID.Hex() {
		t.Fatalf("the reply edge did not come back: %q", got)
	}
	if got := byAlt["just a photo"].ReplyTo; got != "" {
		t.Fatalf("an edge leaked onto a plain photo: %q", got)
	}

	// A malformed edge is refused before anything is emitted.
	resp, _ = postMultipart(t, srv.URL, api.Token(), "/api/spaces/"+sid+"/blocks",
		map[string]any{"kind": "visual", "size": len(content), "media_type": "image/webp",
			"alt": "bad edge", "reply_to": "zz"},
		nil, content)
	if resp.StatusCode != 400 {
		t.Fatalf("malformed reply_to admitted: %d", resp.StatusCode)
	}
}

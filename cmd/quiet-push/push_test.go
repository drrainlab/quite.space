package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testAccount(t *testing.T, tokenURI string) (*ServiceAccount, *rsa.PublicKey) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	raw, _ := json.Marshal(map[string]string{
		"project_id": "quite-test", "client_email": "push@quite-test.iam.gserviceaccount.com",
		"private_key": string(pemKey), "token_uri": tokenURI,
	})
	p := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sa, err := LoadServiceAccount(p)
	if err != nil {
		t.Fatal(err)
	}
	return sa, &k.PublicKey
}

const goodToken = "dGVzdA:APA91bFakeTokenFakeTokenFakeTokenFakeTokenFakeTokenFakeTokenFakeTokenFakeTokenFakeToken_-0123456789"

// One ping becomes one high-priority, contentless FCM message, sent with an
// access token minted from a JWT the account's key signed.
func TestAPingBecomesOneContentlessHighPriorityMessage(t *testing.T) {
	var pub *rsa.PublicKey
	var mints atomic.Int32
	var sent []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			mints.Add(1)
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
				t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
			}
			claims, err := verifyAssertion(r.Form.Get("assertion"), pub)
			if err != nil {
				t.Errorf("assertion does not verify: %v", err)
			}
			if claims["scope"] != fcmScope || claims["iss"] != "push@quite-test.iam.gserviceaccount.com" {
				t.Errorf("claims = %v", claims)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-1", "expires_in": 3600})
		case "/send":
			if r.Header.Get("Authorization") != "Bearer at-1" {
				t.Errorf("authorization = %q", r.Header.Get("Authorization"))
			}
			var m map[string]any
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &m)
			sent = append(sent, m)
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()

	sa, pk := testAccount(t, upstream.URL+"/token")
	pub = pk
	f := NewFCM(sa)
	f.sendURL = upstream.URL + "/send"
	h := NewHandler(f)

	for i := 0; i < 2; i++ { // the second is coalesced away
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/fcm/"+goodToken, strings.NewReader("qp")))
		if rec.Code != 204 {
			t.Fatalf("ping %d: status %d", i, rec.Code)
		}
	}
	if len(sent) != 1 {
		t.Fatalf("forwarded %d message(s) for two pings inside the quiet period, want 1", len(sent))
	}
	msg := sent[0]["message"].(map[string]any)
	if msg["token"] != goodToken {
		t.Errorf("token = %v", msg["token"])
	}
	android := msg["android"].(map[string]any)
	if android["priority"] != "high" {
		t.Errorf("priority = %v — only high priority crosses Doze", android["priority"])
	}
	if _, has := msg["notification"]; has {
		t.Error("a notification block would make Android draw Google's card, not ours")
	}
	data := msg["data"].(map[string]any)
	if len(data) != 1 || data["qp"] != "1" {
		t.Errorf("data = %v — the payload is a fixed marker, nothing else", data)
	}
	if mints.Load() != 1 {
		t.Errorf("access token minted %d times", mints.Load())
	}
}

func TestTheAccessTokenIsRenewedAMinuteEarly(t *testing.T) {
	var mints atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			mints.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "expires_in": 3600})
			return
		}
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	sa, _ := testAccount(t, upstream.URL+"/token")
	f := NewFCM(sa)
	f.sendURL = upstream.URL + "/send"
	clock := time.Now()
	f.now = func() time.Time { return clock }
	f.Send(goodToken)
	clock = clock.Add(58 * time.Minute)
	f.Send(goodToken)
	if mints.Load() != 1 {
		t.Fatalf("minted %d times inside the hour", mints.Load())
	}
	clock = clock.Add(90 * time.Second) // 59.5 min: inside the last minute
	f.Send(goodToken)
	if mints.Load() != 2 {
		t.Fatalf("minted %d times — the token was not renewed before it expired", mints.Load())
	}
}

func TestUpstreamAnswersMapToOutcomes(t *testing.T) {
	var status atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "expires_in": 3600})
			return
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer upstream.Close()
	sa, _ := testAccount(t, upstream.URL+"/token")
	f := NewFCM(sa)
	f.sendURL = upstream.URL + "/send"
	for _, c := range []struct {
		code int
		want Outcome
	}{{200, Delivered}, {404, Gone}, {400, Gone}, {403, Refused}, {500, Failed}, {429, Failed}} {
		status.Store(int32(c.code))
		if got := f.Send(goodToken); got != c.want {
			t.Errorf("upstream %d → %v, want %v", c.code, got, c.want)
		}
	}
}

// Anyone who can guess the URL can call it: what is not a token is not
// forwarded, and nothing about a request is echoed back.
func TestOnlyRegistrationTokensAreForwarded(t *testing.T) {
	var sends atomic.Int32
	h := NewHandler(senderFunc(func(string) Outcome { sends.Add(1); return Delivered }))
	for _, bad := range []string{"short", strings.Repeat("a", 600), goodToken + "/../x", goodToken + "%20", "тест" + goodToken} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/fcm/"+bad, nil))
		// 404 from the shape check, or the mux's own redirect for a
		// traversal — either way NOTHING was forwarded.
		if rec.Code == 204 || sends.Load() != 0 {
			t.Fatalf("%q: status %d, sends %d", bad, rec.Code, sends.Load())
		}
		if rec.Code != 301 && strings.Contains(rec.Body.String(), bad[:4]) {
			t.Fatalf("the refusal echoed the input")
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/fcm/"+goodToken, nil))
	if rec.Code == 204 || sends.Load() != 0 {
		t.Fatalf("GET forwarded (status %d)", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "ok\n") {
		t.Fatalf("healthz: %d %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "dGVzdA") {
		t.Fatal("healthz leaks tokens")
	}
}

func TestAGoneTokenIsReportedAsGone(t *testing.T) {
	h := NewHandler(senderFunc(func(string) Outcome { return Gone }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/fcm/"+goodToken, nil))
	if rec.Code != 410 {
		t.Fatalf("status %d, want 410", rec.Code)
	}
}

type senderFunc func(string) Outcome

func (f senderFunc) Send(t string) Outcome { return f(t) }

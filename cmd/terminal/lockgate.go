package main

// The lock gate stands IN FRONT of the node, not inside it.
//
// That is the whole trick, and it is why this file exists in cmd/ rather
// than in node/. The node has no locked state: `node.Open` either succeeds
// with a passphrase or does not happen, and the API only ever serves an
// open runtime. Rather than teach the API to be half-open — a second
// handler, a swap, a whole state machine inside a package that currently
// has none — the gate takes the port first, asks for four digits, hands
// back the passphrase, and gets out of the way. `node.Open` then runs
// exactly as it always has, and everything downstream is unchanged.
//
// It is also the shape the desktop shell needs later (DS-3's swapHandler),
// so this is not throwaway: the same sequence, with the shell owning the
// window instead of a browser tab.

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/kernel/passcode"
	"github.com/drrainlab/quiet_places/node"
)

//go:embed lockscreen.html
var lockAssets embed.FS

// lockGate serves the passcode screen on addr until somebody gets the code
// right, then returns the passphrase it unwrapped. The listener is closed
// before returning, so the real API can take the same port straight after.
//
// The token is NOT handed out until the code is right — the page is
// reachable without one (nobody has authenticated yet), so the reply to a
// correct code is the only place it appears.
func lockGate(l net.Listener, dataDir, token string) ([]byte, error) {
	page, err := lockAssets.ReadFile("lockscreen.html")
	if err != nil {
		return nil, err
	}

	type result struct {
		pass []byte
		err  error
	}
	done := make(chan result, 1)
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Everything that is not the unlock endpoint is the screen. A
		// locked node has exactly one page and no other surface at all.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Write(page)
	})

	mux.HandleFunc("/state", func(w http.ResponseWriter, r *http.Request) {
		st, err := passcode.Info(dataDir)
		if err != nil {
			http.Error(w, "unreadable", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"digits": st.Digits, "attempts_left": st.AttemptsLeft,
			"max_attempts": passcode.MaxAttempts,
		})
	})

	mux.HandleFunc("/unlock", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "post only", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
			writeJSON(w, map[string]any{"ok": false, "reason": "malformed"})
			return
		}
		code := strings.TrimSpace(body.Code)

		// "Use the passphrase instead" arrives through this same endpoint,
		// and it is NOT a passcode attempt: verifying it here means a person
		// falling back to their real key never spends one of the ten tries
		// they might still need, and a passphrase never reaches the KDF that
		// exists to make four digits expensive.
		if !allDigits(code) {
			if err := node.VerifyPassphrase(dataDir, []byte(code)); err != nil {
				writeJSON(w, map[string]any{"ok": false, "reason": "wrong_passphrase"})
				return
			}
			writeJSON(w, map[string]any{"ok": true, "token": token})
			done <- result{pass: []byte(code)}
			return
		}

		pass, err := passcode.Unwrap(dataDir, code)
		if err != nil {
			st, _ := passcode.Info(dataDir)
			reason := "wrong"
			switch {
			case errors.Is(err, passcode.ErrLockedOut), errors.Is(err, passcode.ErrNoPasscode):
				reason = "locked_out"
			case errors.Is(err, passcode.ErrBadCode):
				reason = "malformed"
			}
			writeJSON(w, map[string]any{
				"ok": false, "reason": reason, "attempts_left": st.AttemptsLeft,
			})
			return
		}
		// Right code. The token travels exactly once, in this reply.
		writeJSON(w, map[string]any{"ok": true, "token": token})
		done <- result{pass: pass}
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(l)

	res := <-done
	// Let the reply reach the browser before the port changes hands, then
	// close so node.Open's API can bind the same address.
	time.Sleep(120 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	return res.pass, res.err
}

// allDigits distinguishes a code from a passphrase without deriving from
// either. Empty is not a code.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// bindPort opens the listener the gate serves on. It is bound here rather
// than inside the gate so that a busy port is reported before somebody has
// typed their code rather than after, and so the caller learns the real
// port when --port was 0 — the API must then be told to take that same
// one, or the browser would be left looking at the wrong address.
func bindPort(port int) (net.Listener, string, int, error) {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, "", 0, err
	}
	return l, l.Addr().String(), l.Addr().(*net.TCPAddr).Port, nil
}

// newToken mints the local API token when the gate needs one before the
// API server exists. The gate must know the token in advance because it is
// the thing that hands it over on a correct code.
func newToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// parsePort turns the --port flag into a number, with 0 meaning "any". It
// lives here because the gate needs it before ui.go's own parsing would
// have run, and two copies would eventually disagree.
func parsePort(s string) int {
	if s == "" {
		return 0
	}
	n := 0
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0
	}
	return n
}

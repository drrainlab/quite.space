// Package lockgate is the screen that stands IN FRONT of the node.
//
// That is the whole trick, and it is why this lives beside the node rather
// than inside it. The node has no locked state: node.Open either succeeds
// with a passphrase or does not happen, and the API only ever serves an open
// runtime. Rather than teach the API to be half-open — a second handler, a
// swap, a whole state machine inside a package that has none — the gate holds
// the surface first, asks for what it needs, hands back the passphrase, and
// gets out of the way. node.Open then runs exactly as it always has, and
// everything downstream is unchanged.
//
// It began as one file in cmd/terminal, serving four digits on a listener the
// API would take over. The desktop shell needs the same sequence with two
// differences, and both are why this is a package now: the shell owns a WINDOW
// rather than a browser tab, and it mounts a HANDLER rather than binding a
// port — inside Wails' AssetServer there is no listener at all. So Handler()
// is the real surface and Unlock() is the listener-shaped convenience the CLI
// already had.
//
// The gate asks a different question depending on what it finds, and the four
// answers are genuinely different situations rather than degrees of one:
//
//	create      nothing here yet — make an identity
//	passcode    a code is bound — four digits, ten tries
//	passphrase  a keystore, no code — the real key
//	in_use      another window already holds this directory
//
// Nothing here derives, decrypts or writes anything itself. It asks
// node.Inspect what a directory is, asks passcode to unwrap a code, asks
// node.VerifyPassphrase whether a passphrase opens, and hands the answer to
// somebody who will call node.Open.
package lockgate

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
	"sync"
	"time"

	"encoding/base64"
	"github.com/drrainlab/quiet_places/kernel/passcode"
	"github.com/drrainlab/quiet_places/node"
	"github.com/drrainlab/quiet_places/transports/lan"
)

//go:embed lockscreen.html
var assets embed.FS

// MinPassphrase mirrors kernel/storage's own floor.
//
// It is repeated rather than imported because storage exports the refusal and
// not the number, and a first-run screen has to say the rule BEFORE anything
// is derived — refusing at the field is kinder than refusing after a scrypt.
// TestTheFloorStillMatchesStorage pins the two together, so a change there
// fails here rather than silently letting this screen promise something
// node.Open will reject.
const MinPassphrase = 8

// Credentials are what the gate hands back once somebody gets in.
//
// Created is not cosmetic: on a first run there is no keystore to verify
// against, so the gate has checked the SHAPE of a passphrase and nothing more.
// The caller's node.Open is what makes it real.
type Credentials struct {
	Passphrase  []byte
	DisplayName string // meaningful only when Created
	Created     bool
}

// Gate serves the unlock screen and reports, exactly once, the credentials
// that opened it.
type Gate struct {
	dataDir string
	token   string
	page    []byte

	once   sync.Once
	opened chan Credentials

	// mu guards the one pairing flow a gate may run (MD-1 child half).
	mu   sync.Mutex
	pair *pairFlow
}

// New reads the screen and prepares the gate. The token is NOT handed out
// until somebody gets in — the page itself is reachable without one, because
// nobody has authenticated yet, so the reply to a correct answer is the only
// place it ever appears.
func New(dataDir, token string) (*Gate, error) {
	page, err := assets.ReadFile("lockscreen.html")
	if err != nil {
		return nil, err
	}
	return &Gate{
		dataDir: dataDir,
		token:   token,
		page:    page,
		opened:  make(chan Credentials, 1),
	}, nil
}

// Opened fires once. The channel is buffered and then closed, so a caller that
// arrives late still receives the credentials rather than blocking forever.
func (g *Gate) Opened() <-chan Credentials { return g.opened }

func (g *Gate) deliver(c Credentials) {
	g.once.Do(func() {
		g.opened <- c
		close(g.opened)
	})
}

// Handler is the gate's whole surface. A locked node has this and nothing
// else; the shell composes its own refusal for /api/* around it.
func (g *Gate) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/state", g.state)
	mux.HandleFunc("/suggest", g.suggest)
	mux.HandleFunc("/unlock", g.unlock)
	mux.HandleFunc("/create", g.create)
	mux.HandleFunc("/pair/start", g.pairStart)
	mux.HandleFunc("/pair/state", g.pairState)
	mux.HandleFunc("/pair/approve", g.pairApprove)
	// Everything that is not one of the above is the screen itself.
	mux.HandleFunc("/", g.screen)
	return mux
}

func (g *Gate) screen(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(g.page)
}

// state names the situation. The counts travel only in the mode that has
// them, so the screen cannot render a budget that does not exist.
func (g *Gate) state(w http.ResponseWriter, r *http.Request) {
	st := node.Inspect(g.dataDir)
	out := map[string]any{"min_passphrase": MinPassphrase}
	switch {
	case st.InUse:
		out["mode"] = "in_use"
	case !st.HasIdentity:
		out["mode"] = "create"
	default:
		pc, err := passcode.Info(g.dataDir)
		if err == nil && pc.Bound {
			out["mode"] = "passcode"
			out["digits"] = pc.Digits
			out["attempts_left"] = pc.AttemptsLeft
			out["max_attempts"] = passcode.MaxAttempts
		} else {
			out["mode"] = "passphrase"
		}
	}
	writeJSON(w, out)
}

// suggest offers a passphrase from the project's frozen wordlist — the same
// words the five-word quicklink uses, chosen so one can be read down a
// telephone and typed off a piece of paper years later.
//
// It only ever OFFERS. Nothing is bound until the person submits it, and a
// person who wants their own passphrase never has to press this.
func (g *Gate) suggest(w http.ResponseWriter, r *http.Request) {
	if st := node.Inspect(g.dataDir); st.HasIdentity {
		http.Error(w, "not a first run", http.StatusConflict)
		return
	}
	p, err := passcode.Generate()
	if err != nil {
		http.Error(w, "could not generate", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"passphrase": p})
}

func (g *Gate) unlock(w http.ResponseWriter, r *http.Request) {
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

	// "Use the passphrase instead" arrives through this same endpoint, and it
	// is NOT a passcode attempt: verifying it here means a person falling back
	// to their real key never spends one of the ten tries they might still
	// need, and a passphrase never reaches the KDF that exists to make four
	// digits expensive.
	if !allDigits(code) {
		if err := node.VerifyPassphrase(g.dataDir, []byte(code)); err != nil {
			writeJSON(w, map[string]any{"ok": false, "reason": "wrong_passphrase"})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "token": g.token})
		g.deliver(Credentials{Passphrase: []byte(code)})
		return
	}

	pass, err := passcode.Unwrap(g.dataDir, code)
	if err != nil {
		st, _ := passcode.Info(g.dataDir)
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
	writeJSON(w, map[string]any{"ok": true, "token": g.token})
	g.deliver(Credentials{Passphrase: pass})
}

// create is the first run, and it is a separate endpoint rather than a mode of
// unlock on purpose: unlock VERIFIES against something that exists, and here
// nothing does. Conflating them is how a screen ends up reporting "wrong
// passphrase" to somebody who has not chosen one yet.
func (g *Gate) create(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "post only", http.StatusMethodNotAllowed)
		return
	}
	if st := node.Inspect(g.dataDir); st.HasIdentity {
		// Racing a first run against an existing identity must never overwrite
		// one. node.Open would refuse anyway; saying so here is clearer.
		writeJSON(w, map[string]any{"ok": false, "reason": "exists"})
		return
	}
	var body struct {
		Name       string `json:"name"`
		Passphrase string `json:"passphrase"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, map[string]any{"ok": false, "reason": "malformed"})
		return
	}
	// Not trimmed: a passphrase is bytes, and silently eating somebody's
	// leading space would make it untypable from the paper they wrote it on.
	if len(body.Passphrase) < MinPassphrase {
		writeJSON(w, map[string]any{"ok": false, "reason": "too_short",
			"min_passphrase": MinPassphrase})
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "me"
	}
	writeJSON(w, map[string]any{"ok": true, "token": g.token})
	g.deliver(Credentials{
		Passphrase:  []byte(body.Passphrase),
		DisplayName: name,
		Created:     true,
	})
}

// Unlock serves the gate on l until somebody opens it, then shuts the server
// down so the real API can bind the same address. This is the listener-shaped
// path: the desktop shell uses Handler() instead and hands over no port at all.
func Unlock(l net.Listener, dataDir, token string) (Credentials, error) {
	g, err := New(dataDir, token)
	if err != nil {
		return Credentials{}, err
	}
	srv := &http.Server{Handler: g.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(l) }()

	c := <-g.Opened()
	// Let the reply reach the browser before the port changes hands.
	time.Sleep(120 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	return c, nil
}

// BindPort opens a loopback listener. It is bound by the caller rather than
// inside the gate so that a busy port is reported before somebody has typed
// their code rather than after, and so the caller learns the real port when 0
// was asked for — the API must then be told to take that same one, or the
// browser is left looking at the wrong address.
func BindPort(port int) (net.Listener, string, int, error) {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, "", 0, err
	}
	return l, l.Addr().String(), l.Addr().(*net.TCPAddr).Port, nil
}

// NewToken mints the local API token when the gate needs one before the API
// server exists. The gate must know it in advance because it is the thing that
// hands it over once somebody is in.
func NewToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
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

// ---- pairing onboarding (MD-1/MD-2): the child's half, which by its nature
// runs HERE — before any identity exists to unlock. The person pastes the
// offer their other device shows, this gate runs the ceremony, both screens
// show six digits, and a "yes" here writes the keystore the caller's
// node.Open then opens as a secondary.

type pairFlow struct {
	mu      sync.Mutex
	stage   string // running | digits | done | failed
	digits  string
	fail    string
	approve chan struct{}
}

func (g *Gate) pairStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "post only", http.StatusMethodNotAllowed)
		return
	}
	if st := node.Inspect(g.dataDir); st.HasIdentity {
		writeJSON(w, map[string]any{"ok": false, "reason": "exists"})
		return
	}
	var body struct {
		Passphrase string `json:"passphrase"`
		Offer      string `json:"offer"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(w, map[string]any{"ok": false, "reason": "malformed"})
		return
	}
	if len(body.Passphrase) < MinPassphrase {
		writeJSON(w, map[string]any{"ok": false, "reason": "too_short",
			"min_passphrase": MinPassphrase})
		return
	}
	offer, err := decodeOfferLoose(body.Offer)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "reason": "bad_offer"})
		return
	}
	g.mu.Lock()
	if g.pair != nil {
		g.mu.Unlock()
		writeJSON(w, map[string]any{"ok": false, "reason": "in_progress"})
		return
	}
	flow := &pairFlow{stage: "running", approve: make(chan struct{})}
	g.pair = flow
	g.mu.Unlock()

	pass := body.Passphrase
	go func() {
		err := node.JoinAsPairedDeviceVia(g.dataDir, []byte(pass), offer,
			lan.MulticastAddr, func(digits string) bool {
				flow.mu.Lock()
				flow.stage, flow.digits = "digits", digits
				flow.mu.Unlock()
				// The human's yes arrives via /pair/approve; a closed gate
				// window abandons the ceremony, which costs the offer nothing.
				<-flow.approve
				return true
			}, uint64(time.Now().Unix()))
		flow.mu.Lock()
		if err != nil {
			flow.stage, flow.fail = "failed", err.Error()
		} else {
			flow.stage = "done"
		}
		flow.mu.Unlock()
		if err == nil {
			// The keystore exists now; the caller's node.Open makes it real.
			g.deliver(Credentials{Passphrase: []byte(pass)})
		} else {
			g.mu.Lock()
			g.pair = nil // a failed ceremony frees the screen to try again
			g.mu.Unlock()
		}
	}()
	writeJSON(w, map[string]any{"ok": true})
}

func (g *Gate) pairState(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	flow := g.pair
	g.mu.Unlock()
	if flow == nil {
		writeJSON(w, map[string]any{"stage": "idle"})
		return
	}
	flow.mu.Lock()
	defer flow.mu.Unlock()
	writeJSON(w, map[string]any{"stage": flow.stage, "digits": flow.digits, "error": flow.fail})
}

func (g *Gate) pairApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "post only", http.StatusMethodNotAllowed)
		return
	}
	g.mu.Lock()
	flow := g.pair
	g.mu.Unlock()
	if flow == nil {
		writeJSON(w, map[string]any{"ok": false})
		return
	}
	flow.mu.Lock()
	ok := flow.stage == "digits"
	if ok {
		flow.stage = "running"
		close(flow.approve)
	}
	flow.mu.Unlock()
	writeJSON(w, map[string]any{"ok": ok})
}

// decodeOfferLoose accepts the offer however a human got it here: standard
// or URL base64, padded or not, with the whitespace a copy-paste collects.
func decodeOfferLoose(s string) ([]byte, error) {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, s))
	if s == "" {
		return nil, errors.New("empty offer")
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("not base64")
}

// The UI seam for devices and pairing (MD-1/MD-2): what a screen needs and
// nothing it must not have.
//
// The pairing flow a client drives:
//
//	POST /api/pairing            → the offer (for QR or sound) + "listening"
//	GET  /api/pairing            → poll: listening | digits | approving |
//	                               done | failed  (+ the six digits)
//	POST /api/pairing/approve    → the human said the numbers match
//	DELETE /api/pairing          → cancel; an unspent offer dies with it
//
// The child side of the ceremony belongs to the app shell, BEFORE a runtime
// exists (JoinAsPairedDevice against an empty dir) — an API served by an
// open node cannot onboard the very device that has nothing to open yet.
package node

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/pairing"
	"github.com/drrainlab/quiet_places/transports/lan"
)

// pairingUI is one pairing flow's state as a screen sees it.
type pairingUI struct {
	mu      sync.Mutex
	host    *PairingHost
	stage   string // listening | digits | approving | done | failed
	digits  string
	fail    string
	attempt *PairingAttempt
}

func (a *APIServer) handleDevices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"devices": a.rt.Devices()})
}

func (a *APIServer) handleRevokeDevice(w http.ResponseWriter, r *http.Request) {
	raw, err := hex.DecodeString(r.PathValue("id"))
	var dev id.DeviceID
	if err != nil || len(raw) != len(dev) {
		http.Error(w, "bad device id", http.StatusBadRequest)
		return
	}
	copy(dev[:], raw)
	if err := a.rt.RevokeDevice(dev); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]any{"revoked": dev.Hex()})
}

func (a *APIServer) handleBeginPairing(w http.ResponseWriter, r *http.Request) {
	a.pairingMu.Lock()
	defer a.pairingMu.Unlock()
	if a.pairing != nil {
		a.pairing.close() // one flow at a time; beginning again cancels the old
	}
	host, err := a.rt.BeginPairing(pairingBind(), uint64(time.Now().Unix()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	// The beacon fallback runs for the offer's whole life: DHCP does not
	// care about ceremonies.
	host.Announce(lan.MulticastAddr)
	p := &pairingUI{host: host, stage: "listening"}
	a.pairing = p
	go p.acceptLoop()
	writeJSON(w, map[string]any{
		"offer": base64.StdEncoding.EncodeToString(host.OfferBytes()),
		"stage": p.stage,
	})
}

// acceptLoop admits dial-ins until one survives the hellos: a stranger's
// babble costs the offer nothing (spent on confirmation, never on connect),
// so a failed attempt just means listening again inside the window.
func (p *pairingUI) acceptLoop() {
	for {
		attempt, err := p.host.Accept(uint64(time.Now().Unix()))
		p.mu.Lock()
		if p.stage != "listening" {
			p.mu.Unlock()
			return // cancelled or superseded while we waited
		}
		if err == nil {
			p.stage, p.digits, p.attempt = "digits", attempt.Digits, attempt
			p.mu.Unlock()
			return
		}
		if errors.Is(err, pairing.ErrOfferExpired) || errors.Is(err, pairing.ErrOfferSpent) {
			p.stage, p.fail = "failed", err.Error()
			p.mu.Unlock()
			return
		}
		p.mu.Unlock() // a stranger; keep listening
	}
}

func (a *APIServer) handlePairingStatus(w http.ResponseWriter, r *http.Request) {
	a.pairingMu.Lock()
	p := a.pairing
	a.pairingMu.Unlock()
	if p == nil {
		writeJSON(w, map[string]any{"stage": "idle"})
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	writeJSON(w, map[string]any{
		"stage":  p.stage,
		"digits": p.digits,
		"error":  p.fail,
		"offer":  base64.StdEncoding.EncodeToString(p.host.OfferBytes()),
	})
}

func (a *APIServer) handleApprovePairing(w http.ResponseWriter, r *http.Request) {
	a.pairingMu.Lock()
	p := a.pairing
	a.pairingMu.Unlock()
	if p == nil {
		http.Error(w, "no pairing in progress", http.StatusConflict)
		return
	}
	p.mu.Lock()
	if p.stage != "digits" {
		stage := p.stage
		p.mu.Unlock()
		http.Error(w, "nothing to approve at stage "+stage, http.StatusConflict)
		return
	}
	p.stage = "approving"
	attempt := p.attempt
	p.mu.Unlock()
	// Approve blocks on the child's own confirmation, so it runs off the
	// request: the phone's human and the desktop's human press in whichever
	// order they like.
	go func() {
		err := attempt.Approve(uint64(time.Now().Unix()))
		p.mu.Lock()
		if err != nil {
			p.stage, p.fail = "failed", err.Error()
		} else {
			p.stage = "done"
		}
		p.mu.Unlock()
	}()
	writeJSON(w, map[string]any{"stage": "approving"})
}

func (a *APIServer) handleCancelPairing(w http.ResponseWriter, r *http.Request) {
	a.pairingMu.Lock()
	defer a.pairingMu.Unlock()
	if a.pairing != nil {
		a.pairing.close()
		a.pairing = nil
	}
	writeJSON(w, map[string]any{"stage": "idle"})
}

func (p *pairingUI) close() {
	p.mu.Lock()
	if p.stage == "listening" || p.stage == "digits" {
		p.stage = "failed"
		p.fail = "cancelled"
	}
	p.mu.Unlock()
	p.host.Close()
}

// pairingBind is where the ceremony listener binds: the node's best guess
// at its LAN-reachable address, so the offer's fast path is dialable from
// the room. The guess can be wrong or go stale — that is exactly what the
// beacon exists for.
// pairingBind is where the ceremony listener binds: EVERY interface, an
// ephemeral port. Which address the offer advertises is a separate question
// (advertiseHost), answered inside BeginPairing — binding to a guessed
// address was the bug a live pairing found, with a VPN owning the guess.
var pairingBind = func() string { return "0.0.0.0:0" }

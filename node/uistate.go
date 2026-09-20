package node

// THIS DEVICE'S OWN INTERFACE STATE, KEPT BY THE NODE.
//
// The web client kept "where you were" in localStorage, and on a desktop that
// works. On Android it cannot: the core listens on an EPHEMERAL port, so every
// launch is a new origin and the browser's storage starts empty — the owner
// scrolled a conversation to its end, closed the app, and reopened on the
// first space of the list. He had said "remember it ON THE NODE" in the
// first place.
//
// One small JSON document, device-local, never replicated, sealed at rest
// with everything else the node keeps for itself (it names spaces and
// messages, so it is not for a plain file). The node does not interpret it:
// it is the client's notebook. Bounded, and it must be an object.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const (
	uiStateSealedName = "ui-state.v1"
	uiStateMaxBytes   = 64 << 10
)

// UIState returns the stored document, or "{}" when there is none yet.
func (r *Runtime) UIState() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, err := r.root.LoadSealed(uiStateSealedName)
	if err != nil || len(raw) == 0 || !json.Valid(raw) {
		return []byte("{}")
	}
	return raw
}

// SetUIState replaces the document.
func (r *Runtime) SetUIState(raw []byte) error {
	if len(raw) > uiStateMaxBytes {
		return errors.New("node: interface state is too large")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return errors.New("node: interface state must be a JSON object")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.root.SaveSealed(uiStateSealedName, raw)
}

func (a *APIServer) handleUIState(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(a.rt.UIState())
}

func (a *APIServer) handleSetUIState(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, uiStateMaxBytes+1))
	if err != nil {
		httpErr(w, http.StatusRequestEntityTooLarge, errors.New("interface state is too large"))
		return
	}
	if err := a.rt.SetUIState(raw); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

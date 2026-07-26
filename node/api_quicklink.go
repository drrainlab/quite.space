package node

import (
	"errors"
	"net/http"
	"strings"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/quicklink"
)

// Quick link routes (SP-1). Registered from api.go.
func (a *APIServer) routeQuickLinks(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/quicklinks", a.auth(a.handleMintQuickLink))
	mux.HandleFunc("GET /api/quicklinks", a.auth(a.handleListQuickLinks))
	mux.HandleFunc("DELETE /api/quicklinks/{hint}", a.auth(a.handleWithdrawQuickLink))
	mux.HandleFunc("POST /api/quicklinks/resolve", a.auth(a.handleResolveQuickLink))
	mux.HandleFunc("GET /api/quicklinks/words", a.auth(a.handleQuickLinkWords))
}

func (a *APIServer) handleMintQuickLink(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		// Space is optional: empty means this node's own line, created on
		// first use. That is the "add me" gesture.
		Space string `json:"space"`
		Note  string `json:"note"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	var tid id.TerminalID
	if body.Space != "" {
		if tid, err = id.ParseTerminalID(body.Space); err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
	}
	info, err := a.rt.MintQuickLink(tid, body.Note)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, info)
}

func (a *APIServer) handleListQuickLinks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"quicklinks": a.rt.QuickLinks()})
}

func (a *APIServer) handleWithdrawQuickLink(w http.ResponseWriter, r *http.Request) {
	if err := a.rt.WithdrawQuickLink(r.PathValue("hint")); err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	// Say what withdrawing does and does not do. The sealed payload stays on
	// the relay until its TTL runs out; what is gone is the pass it points
	// at. A caller that reports "revoked" without this is overstating.
	writeJSON(w, map[string]any{
		"withdrawn": true,
		"note": "The pass is revoked. The sealed payload stays on the relay until it " +
			"expires, so the words may still resolve — and then fail to join, which is " +
			"the honest outcome.",
	})
}

func (a *APIServer) handleResolveQuickLink(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Words string `json:"words"`
	}](r)
	if err != nil || strings.TrimSpace(body.Words) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("words required"))
		return
	}
	preview, err := a.rt.ResolveQuickLink(body.Words)
	if err != nil {
		// A malformed token is the caller's mistake; anything else is a
		// condition of the world (expired, used, wrong relay) and should not
		// read as a client error.
		code := http.StatusNotFound
		if errors.Is(err, quicklink.ErrNotAToken) {
			code = http.StatusBadRequest
		}
		httpErr(w, code, err)
		return
	}
	writeJSON(w, preview)
}

// handleQuickLinkWords serves completions so the join field can help someone
// typing a half-heard word. It returns candidates and never a choice — the
// same rule Parse follows, one layer up.
func (a *APIServer) handleQuickLinkWords(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	writeJSON(w, map[string]any{
		"words": quicklink.Suggest(prefix, 8),
		"count": quicklink.WordCount,
	})
}

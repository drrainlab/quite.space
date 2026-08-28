package node

// The basemap tile endpoint (SP-3.1, ADR-032). One GET, image bytes out.
// The page's CSP (img-src 'self') makes this the only door tiles can
// enter through — see tiles.go for the laws enforced behind it.

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
)

func (a *APIServer) routeTiles(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tiles/{z}/{x}/{y}", a.auth(a.handleTile))
}

func (a *APIServer) handleTile(w http.ResponseWriter, r *http.Request) {
	z, errZ := strconv.Atoi(r.PathValue("z"))
	x, errX := strconv.Atoi(r.PathValue("x"))
	// The y segment arrives as "1300.png" — the extension is for the
	// browser's benefit, not the node's.
	y, errY := strconv.Atoi(strings.TrimSuffix(r.PathValue("y"), ".png"))
	if errZ != nil || errX != nil || errY != nil {
		http.NotFound(w, r)
		return
	}
	data, err := a.rt.FetchTile(r.Context(), z, x, y)
	if err != nil {
		// "Policy or network said no, nothing cached" is a distinct
		// answer from "no such tile": the header lets a test (or curl)
		// see the difference an <img> cannot.
		if errors.Is(err, errTileOffline) {
			w.Header().Set("X-QP-Tile", "offline")
		}
		http.NotFound(w, r)
		return
	}
	// Content-Type from the bytes we hold, never from what an upstream
	// once claimed (the asset-serving law, api_blocks.go).
	w.Header().Set("Content-Type", http.DetectContentType(data))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// An hour, not immutable: tiles are not content-addressed and the
	// upstream re-renders them. The browser cache is a free layer above
	// the node's own.
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(data)
}

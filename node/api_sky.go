package node

// SKY (SK-1) — the API for a shared drawing. Three verbs, no canvas
// object: start a sky (one block message), draw a stroke (one small
// event), erase your own (one event naming them). Reading is the
// reducer's projection: the strokes in film order, plus how many hands.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

type skyResp struct {
	Title   string `json:"title,omitempty"`
	Strokes int    `json:"strokes"`
	Hands   int    `json:"hands"`
	Evicted int    `json:"evicted,omitempty"`
}

func (a *APIServer) handleStartSky(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Title string `json:"title"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	payload, err := (&schemas.SkyBlock{Title: strings.TrimSpace(body.Title)}).Encode()
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	eid, err := a.rt.EmitBlock(tid, schemas.BlockSky, payload)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{"id": eid.Hex()})
}

func (a *APIServer) handleSky(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	sky, err := parseEventID(r.PathValue("sky"))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	type strokeJSON struct {
		ID     string `json:"id"`
		Author string `json:"author"`
		Mine   bool   `json:"mine,omitempty"`
		Points []int  `json:"points"`
		Bright uint8  `json:"bright"`
		At     uint64 `json:"at"`
	}
	out := map[string]any{"grid": schemas.SkyGrid}
	if err := a.rt.withSpace(tid, func(st *spaceState) error {
		me := a.rt.PrincipalID
		strokes := st.space.State.SkyStrokes(sky)
		list := make([]strokeJSON, 0, len(strokes))
		for _, s := range strokes {
			pts := make([]int, len(s.Points))
			for i, v := range s.Points {
				pts[i] = int(v)
			}
			list = append(list, strokeJSON{ID: s.EventID.Hex(), Author: s.Author.Hex(),
				Mine: s.Author == me, Points: pts, Bright: s.Bright, At: s.CreatedAt})
		}
		n, hands, evicted := st.space.State.SkyStats(sky)
		out["strokes"] = list
		out["count"], out["hands"], out["evicted"] = n, hands, evicted
		out["me"] = me.Hex()
		return nil
	}); err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, out)
}

func (a *APIServer) handleSkyStroke(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	sky, err := parseEventID(r.PathValue("sky"))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Points []int `json:"points"`
		Bright uint8 `json:"bright"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	pts := make([]byte, 0, len(body.Points))
	for _, v := range body.Points {
		if v < 0 || v >= schemas.SkyGrid {
			httpErr(w, http.StatusBadRequest, errors.New("point off the grid"))
			return
		}
		pts = append(pts, byte(v))
	}
	payload, err := (&schemas.SkyStrokeEvent{Sky: sky, Points: pts, Bright: body.Bright}).Encode()
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	eid, err := a.rt.EmitBlock(tid, schemas.SkyStroke, payload)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{"id": eid.Hex()})
}

func (a *APIServer) handleSkyErase(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	sky, err := parseEventID(r.PathValue("sky"))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		IDs []string `json:"ids"`
	}](r)
	if err != nil || len(body.IDs) == 0 {
		httpErr(w, http.StatusBadRequest, errors.New("ids required"))
		return
	}
	var targets []id.EventID
	for _, s := range body.IDs {
		e, err := parseEventID(s)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
		targets = append(targets, e)
	}
	payload, err := (&schemas.SkyStrokeEvent{Sky: sky, Erase: targets}).Encode()
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	eid, err := a.rt.EmitBlock(tid, schemas.SkyStroke, payload)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{"id": eid.Hex()})
}

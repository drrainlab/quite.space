// Field API (SP-3, ADR-031). JSON speaks float degrees; the wire never
// does — protocol/geo converts at this boundary. The freshness ladder
// (live / stale / unknown + ages) and the overdue arithmetic are computed
// HERE, from signed expiries: the map projects knowledge and its age,
// never a fabricated present.
package node

import (
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/protocol/geo"
)

func geoFromBody(lat, lon float64) (geo.Point, error) {
	return geo.FromDegrees(lat, lon)
}

func (a *APIServer) handleSetPosition(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Lat      float64 `json:"lat"`
		Lon      float64 `json:"lon"`
		Accuracy uint64  `json:"accuracy_m"`
		TTL      uint64  `json:"ttl_seconds"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	pt, err := geoFromBody(body.Lat, body.Lon)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.rt.SetPosition(tid, pt, body.Accuracy, body.TTL); err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *APIServer) handlePlaceMarker(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Kind      string  `json:"kind"`
		Text      string  `json:"text"`
		Lat       float64 `json:"lat"`
		Lon       float64 `json:"lon"`
		ObjectID  string  `json:"object_id"`
		ExpiresIn uint64  `json:"expires_in_seconds"`
	}](r)
	if err != nil || body.Kind == "" {
		httpErr(w, http.StatusBadRequest, errors.New("kind required"))
		return
	}
	pt, err := geoFromBody(body.Lat, body.Lon)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	var objID *[16]byte
	if body.ObjectID != "" {
		b, err := hex.DecodeString(body.ObjectID)
		if err != nil || len(b) != 16 {
			httpErr(w, http.StatusBadRequest, errors.New("bad object id"))
			return
		}
		var o [16]byte
		copy(o[:], b)
		objID = &o
	}
	var expiresAt uint64
	if body.ExpiresIn > 0 {
		expiresAt = uint64(time.Now().Unix()) + body.ExpiresIn
	}
	eid, err := a.rt.PlaceMarker(tid, strings.TrimSpace(body.Kind),
		strings.TrimSpace(body.Text), pt, objID, expiresAt)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{"id": eid.Hex()})
}

func (a *APIServer) handleCheckin(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Note    string   `json:"note"`
		Lat     *float64 `json:"lat"`
		Lon     *float64 `json:"lon"`
		Battery *uint64  `json:"battery_pct"`
		SOS     bool     `json:"sos"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	var pt *geo.Point
	if body.Lat != nil && body.Lon != nil {
		p, err := geoFromBody(*body.Lat, *body.Lon)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
		pt = &p
	}
	var battery uint64
	hasBattery := body.Battery != nil
	if hasBattery {
		battery = *body.Battery
	}
	eid, err := a.rt.SendCheckin(tid, strings.TrimSpace(body.Note), pt, battery, hasBattery, body.SOS)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{"id": eid.Hex()})
}

// handleField is the map's one bundle: members with the honesty ladder,
// markers, latest check-ins with the viewer's overdue arithmetic input,
// and every geo-bearing object.
func (a *APIServer) handleField(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	now := uint64(time.Now().Unix())
	resp := map[string]any{}
	if err := a.rt.withSpace(tid, func(st *spaceState) error {
		me := a.rt.PrincipalID
		// People: the ladder per member card. live = inside the SIGNED
		// TTL; stale = expired but known; unknown = never claimed. Last
		// KNOWN place + the age of the knowing — never a faked present.
		people := []map[string]any{}
		for _, c := range st.space.MemberCards(now) {
			pj := map[string]any{
				"principal": c.Principal.Hex(),
				"name":      c.Name,
				"mine":      c.Principal == me,
			}
			pos := st.space.Trust.Position(c.Terminal, now)
			switch {
			case pos.Known && pos.Current:
				pj["position"] = map[string]any{
					"lat":        geo.Point{LatE7U: pos.LatE7U, LonE7U: pos.LonE7U}.LatDeg(),
					"lon":        geo.Point{LatE7U: pos.LatE7U, LonE7U: pos.LonE7U}.LonDeg(),
					"accuracy_m": pos.AccuracyM, "state": "live",
					"age_seconds": pos.AgeSeconds, "remaining_seconds": pos.RemainingSeconds,
				}
			case pos.Known:
				pj["position"] = map[string]any{
					"lat":        geo.Point{LatE7U: pos.LatE7U, LonE7U: pos.LonE7U}.LatDeg(),
					"lon":        geo.Point{LatE7U: pos.LatE7U, LonE7U: pos.LonE7U}.LonDeg(),
					"accuracy_m": pos.AccuracyM, "state": "stale",
					"age_seconds": pos.AgeSeconds,
				}
			default:
				pj["position"] = map[string]any{"state": "unknown"}
			}
			if ci, ok := st.space.State.LatestCheckin(c.Principal); ok {
				cj := map[string]any{
					"text": ci.Text, "at": ci.CreatedAt,
					"age_seconds": ageSince(ci.CreatedAt, now), "sos": ci.SOS,
				}
				if ci.HasBattery {
					cj["battery_pct"] = ci.BatteryPct
				}
				pj["checkin"] = cj
			}
			people = append(people, pj)
		}
		resp["people"] = people

		markers := []map[string]any{}
		for _, m := range st.space.State.Markers() {
			mj := map[string]any{
				"id":     hex.EncodeToString(m.MarkerID[:]),
				"kind":   m.Kind,
				"text":   m.Text,
				"lat":    geo.Point{LatE7U: m.LatE7U, LonE7U: m.LonE7U}.LatDeg(),
				"lon":    geo.Point{LatE7U: m.LatE7U, LonE7U: m.LonE7U}.LonDeg(),
				"author": m.Author.Hex(),
				"at":     m.CreatedAt,
			}
			if m.ObjectID != nil {
				mj["object_id"] = hex.EncodeToString(m.ObjectID[:])
			}
			if m.ExpiresAt != 0 {
				mj["expires_at"] = m.ExpiresAt
				// A marker is a historical claim; "active" is display
				// arithmetic, said here so every client agrees.
				mj["active"] = now < m.ExpiresAt
			} else {
				mj["active"] = true
			}
			markers = append(markers, mj)
		}
		resp["markers"] = markers

		// Geo-bearing objects: places, routes — anything standing on the
		// map. One level, list form.
		places := []map[string]any{}
		for _, o := range st.space.State.Objects() {
			if o.Record.Geo == nil && len(o.Record.Path) == 0 {
				continue
			}
			oj := objectListJSON(o, st.space.State.TasksForObject(o.ObjectID))
			addParentName(st, o, oj)
			places = append(places, oj)
		}
		resp["objects"] = places
		return nil
	}); err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, resp)
}

func ageSince(at, now uint64) uint64 {
	if now <= at {
		return 0
	}
	return now - at
}

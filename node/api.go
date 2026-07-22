// Local typed API (ADR-011): HTTP on 127.0.0.1 with a per-session token.
// The UI is a pure client of this API and contains no protocol logic. Every
// projection carries its honesty fields — there is no endpoint that returns
// a bare "online" or "delivered".
package node

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// APIServer wraps a Runtime for local HTTP access.
type APIServer struct {
	rt    *Runtime
	token string
	ui    fs.FS // optional embedded web UI
}

// NewAPIServer creates the server with a fresh session token.
func NewAPIServer(rt *Runtime, ui fs.FS) (*APIServer, error) {
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	return &APIServer{rt: rt, token: hex.EncodeToString(tok), ui: ui}, nil
}

// Token returns the session token (printed as part of the UI URL).
func (a *APIServer) Token() string { return a.token }

// Serve binds 127.0.0.1 (only) and serves until the listener closes.
// Returns the bound address.
func (a *APIServer) Serve(port int) (string, net.Listener, error) {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", itoa(port)))
	if err != nil {
		return "", nil, err
	}
	go http.Serve(l, a.Handler())
	return l.Addr().String(), l, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Handler builds the route table.
func (a *APIServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", a.auth(a.handleStatus))
	mux.HandleFunc("GET /api/spaces", a.auth(a.handleSpaces))
	mux.HandleFunc("POST /api/spaces", a.auth(a.handleCreateSpace))
	mux.HandleFunc("GET /api/spaces/{id}/messages", a.auth(a.handleMessages))
	mux.HandleFunc("POST /api/spaces/{id}/messages", a.auth(a.handleSay))
	mux.HandleFunc("GET /api/spaces/{id}/state", a.auth(a.handleState))
	mux.HandleFunc("POST /api/spaces/{id}/cards", a.auth(a.handleMakeCard))
	mux.HandleFunc("POST /api/spaces/{id}/cards/{card}/status", a.auth(a.handleCardStatus))
	mux.HandleFunc("POST /api/spaces/{id}/invites", a.auth(a.handleMintInvite))
	mux.HandleFunc("POST /api/invites/accept", a.auth(a.handleJoin))
	mux.HandleFunc("POST /api/lan/connect", a.auth(a.handleConnect))
	mux.HandleFunc("POST /api/mesh/connect", a.auth(a.handleMeshConnect))
	if a.ui != nil {
		mux.Handle("GET /", http.FileServerFS(a.ui))
	}
	return mux
}

func (a *APIServer) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-QP-Token")
		if tok == "" {
			tok = r.URL.Query().Get("token")
		}
		if tok != a.token {
			httpErr(w, http.StatusUnauthorized, errors.New("missing or wrong token"))
			return
		}
		next(w, r)
	}
}

func httpErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (a *APIServer) spaceID(r *http.Request) (id.TerminalID, error) {
	return id.ParseTerminalID(r.PathValue("id"))
}

// ---- Handlers ----

type statusResp struct {
	Fingerprint string `json:"fingerprint"`
	DeviceID    string `json:"device_id"`
	DeviceXPub  string `json:"device_xpub"`
	LAN         struct {
		Listening bool `json:"listening"`
		Port      int  `json:"port"`
		Peers     int  `json:"peers"`
	} `json:"lan"`
	Mesh struct {
		Connected bool   `json:"connected"`
		NodeNum   uint32 `json:"node_num"`
		TX        int    `json:"tx"`
		RX        int    `json:"rx"`
		Err       string `json:"err,omitempty"`
	} `json:"mesh"`
}

func (a *APIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	var resp statusResp
	resp.Fingerprint = a.rt.Principal.Fingerprint()
	resp.DeviceID = a.rt.Device.ID.Hex()
	resp.DeviceXPub = hex.EncodeToString(a.rt.Device.X25519Pub[:])
	l := a.rt.LAN()
	resp.LAN.Listening, resp.LAN.Port, resp.LAN.Peers = l.Listening, l.Port, l.Peers
	m := a.rt.Mesh()
	resp.Mesh.Connected, resp.Mesh.NodeNum = m.Connected, m.NodeNum
	resp.Mesh.TX, resp.Mesh.RX, resp.Mesh.Err = m.TX, m.RX, m.Err
	writeJSON(w, resp)
}

func (a *APIServer) handleMeshConnect(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Target string `json:"target"`
	}](r)
	if err != nil || body.Target == "" {
		httpErr(w, http.StatusBadRequest, errors.New("target required: tcp:HOST[:PORT] or serial:/dev/PATH"))
		return
	}
	if err := a.rt.StartMeshtastic(strings.TrimSpace(body.Target)); err != nil {
		httpErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

type spaceResp struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Owned         bool   `json:"owned"`
	Events        int    `json:"events"`
	Messages      int    `json:"messages"`
	Undecryptable int    `json:"undecryptable"`
	Peers         int    `json:"peers"`
}

func (a *APIServer) handleSpaces(w http.ResponseWriter, r *http.Request) {
	spaces := a.rt.Spaces()
	out := make([]spaceResp, 0, len(spaces))
	for _, s := range spaces {
		out = append(out, spaceResp{
			ID: s.ID.Hex(), Title: s.Title, Owned: s.Owned,
			Events: s.Events, Messages: s.Messages,
			Undecryptable: s.Undecryptable, Peers: s.Peers,
		})
	}
	writeJSON(w, out)
}

func readBody[T any](r *http.Request) (T, error) {
	var v T
	err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&v)
	return v, err
}

func (a *APIServer) handleCreateSpace(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Title string `json:"title"`
	}](r)
	if err != nil || strings.TrimSpace(body.Title) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("title required"))
		return
	}
	tid, err := a.rt.CreateSpace(strings.TrimSpace(body.Title))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]string{"id": tid.Hex()})
}

type messageResp struct {
	ID         string `json:"id"`
	Author     string `json:"author"`
	Text       string `json:"text"`
	ProducedBy string `json:"produced_by"`
	Revised    bool   `json:"revised"`
	Clock      uint64 `json:"clock"`
	Mine       bool   `json:"mine"`
}

func (a *APIServer) handleMessages(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	s, ok := a.rt.Space(tid)
	if !ok {
		httpErr(w, http.StatusNotFound, errors.New("unknown space"))
		return
	}
	msgs := s.State.Messages()
	out := make([]messageResp, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, messageResp{
			ID: m.ID.Hex(), Author: m.Author.String(), Text: m.Text,
			ProducedBy: m.ProducedBy.String(), Revised: m.Revised,
			Clock: m.Clock, Mine: m.Author == a.rt.Principal.ID,
		})
	}
	writeJSON(w, out)
}

func (a *APIServer) handleSay(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Text string `json:"text"`
	}](r)
	if err != nil || body.Text == "" {
		httpErr(w, http.StatusBadRequest, errors.New("text required"))
		return
	}
	eid, err := a.rt.Say(tid, body.Text)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{"id": eid.Hex()})
}

type cardResp struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type observationResp struct {
	Display   string `json:"display"`
	Simulated bool   `json:"simulated"`
	Freshness string `json:"freshness"`
	AgeSec    uint64 `json:"age_seconds"`
}

type presenceResp struct {
	Known   bool   `json:"known"`
	Current bool   `json:"current"`
	State   string `json:"state"`
	AgeSec  uint64 `json:"age_seconds"`
}

type stateResp struct {
	Cards         []cardResp       `json:"cards"`
	Observation   *observationResp `json:"observation,omitempty"`
	Undecryptable int              `json:"undecryptable"`
	Events        int              `json:"events"`
}

func (a *APIServer) handleState(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	s, ok := a.rt.Space(tid)
	if !ok {
		httpErr(w, http.StatusNotFound, errors.New("unknown space"))
		return
	}
	resp := stateResp{Cards: []cardResp{}, Undecryptable: s.Undecryptable, Events: s.Log.Len()}
	for _, c := range s.State.Cards() {
		resp.Cards = append(resp.Cards, cardResp{ID: c.ID.Hex(), Title: c.Title, Status: c.Status})
	}
	if o, ok := s.State.LatestObservation(); ok {
		now := uint64(time.Now().Unix())
		fresh := "unknown"
		age := uint64(0)
		if now > o.ObservedAt {
			age = now - o.ObservedAt
		}
		if o.Value.StaleAfter > 0 {
			if age > o.Value.StaleAfter {
				fresh = "stale"
			} else {
				fresh = "current"
			}
		}
		sign := ""
		if o.Value.Negative {
			sign = "-"
		}
		resp.Observation = &observationResp{
			Display:   sign + itoa(int(o.Value.CentiValue/100)) + "." + pad2(int(o.Value.CentiValue%100)) + " " + o.Value.Unit,
			Simulated: o.Value.Simulated,
			Freshness: fresh,
			AgeSec:    age,
		}
	}
	writeJSON(w, resp)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

func (a *APIServer) handleMakeCard(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Title  string `json:"title"`
		Origin string `json:"origin"`
	}](r)
	if err != nil || body.Title == "" {
		httpErr(w, http.StatusBadRequest, errors.New("title required"))
		return
	}
	var origin *id.EventID
	if body.Origin != "" {
		h, err := hex.DecodeString(body.Origin)
		if err != nil || len(h) != id.Size {
			httpErr(w, http.StatusBadRequest, errors.New("bad origin event id"))
			return
		}
		var e id.EventID
		copy(e[:], h)
		origin = &e
	}
	eid, err := a.rt.MakeCard(tid, body.Title, origin)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{"id": eid.Hex()})
}

func (a *APIServer) handleCardStatus(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	cardHex := r.PathValue("card")
	h, err := hex.DecodeString(cardHex)
	if err != nil || len(h) != id.Size {
		httpErr(w, http.StatusBadRequest, errors.New("bad card id"))
		return
	}
	var card id.EventID
	copy(card[:], h)
	body, err := readBody[struct {
		Title  string `json:"title"`
		Status string `json:"status"`
	}](r)
	if err != nil || body.Status == "" {
		httpErr(w, http.StatusBadRequest, errors.New("status required"))
		return
	}
	if err := a.rt.SetCardStatus(tid, card, body.Title, body.Status); err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *APIServer) handleMintInvite(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Device string `json:"device"`
		XPub   string `json:"xpub"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	dev, err := id.ParseDeviceID(body.Device)
	if err != nil {
		httpErr(w, http.StatusBadRequest, errors.New("bad device id"))
		return
	}
	xb, err := hex.DecodeString(body.XPub)
	if err != nil || len(xb) != 32 {
		httpErr(w, http.StatusBadRequest, errors.New("bad xpub"))
		return
	}
	var xpub [32]byte
	copy(xpub[:], xb)
	invite, err := a.rt.MintInvite(tid, dev, xpub)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{"invite": invite})
}

func (a *APIServer) handleJoin(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Invite string `json:"invite"`
	}](r)
	if err != nil || body.Invite == "" {
		httpErr(w, http.StatusBadRequest, errors.New("invite required"))
		return
	}
	tid, err := a.rt.JoinInvite(strings.TrimSpace(body.Invite))
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{"id": tid.Hex()})
}

func (a *APIServer) handleConnect(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Addr string `json:"addr"`
	}](r)
	if err != nil || body.Addr == "" {
		httpErr(w, http.StatusBadRequest, errors.New("addr required"))
		return
	}
	if err := a.rt.ConnectPeer(body.Addr); err != nil {
		httpErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

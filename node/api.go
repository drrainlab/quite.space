// Local typed API (ADR-011): HTTP on 127.0.0.1 with a per-session token.
// The UI is a pure client of this API and contains no protocol logic. Every
// projection carries its honesty fields — there is no endpoint that returns
// a bare "online" or "delivered".
package node

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
	qrcode "github.com/skip2/go-qrcode"
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
	mux.HandleFunc("GET /api/onboarding", a.auth(a.handleOnboarding))
	mux.HandleFunc("POST /api/identity/name", a.auth(a.handleSetName))
	mux.HandleFunc("GET /api/spaces", a.auth(a.handleSpaces))
	mux.HandleFunc("POST /api/spaces", a.auth(a.handleCreateSpace))
	mux.HandleFunc("GET /api/spaces/{id}/messages", a.auth(a.handleMessages))
	mux.HandleFunc("POST /api/spaces/{id}/messages", a.auth(a.handleSay))
	mux.HandleFunc("GET /api/spaces/{id}/state", a.auth(a.handleState))
	mux.HandleFunc("POST /api/spaces/{id}/cards", a.auth(a.handleMakeCard))
	mux.HandleFunc("POST /api/spaces/{id}/cards/{card}/status", a.auth(a.handleCardStatus))
	mux.HandleFunc("POST /api/spaces/{id}/invites", a.auth(a.handleMintInvite))
	mux.HandleFunc("GET /api/spaces/{id}/members", a.auth(a.handleMembers))
	mux.HandleFunc("GET /api/spaces/{id}/entries", a.auth(a.handleEntries))
	mux.HandleFunc("POST /api/spaces/{id}/blocks", a.auth(a.handlePostBlock))
	mux.HandleFunc("GET /api/spaces/{id}/assets/{asset}", a.auth(a.handleGetAsset))
	mux.HandleFunc("POST /api/spaces/{id}/assets/{asset}/fetch", a.auth(a.handleFetchAsset))
	mux.HandleFunc("POST /api/spaces/{id}/reactions", a.auth(a.handleReaction))
	mux.HandleFunc("POST /api/spaces/{id}/presence", a.auth(a.handlePresence))
	mux.HandleFunc("POST /api/invites/accept", a.auth(a.handleJoin))
	mux.HandleFunc("POST /api/spaces/{id}/passes", a.auth(a.handleMintPass))
	mux.HandleFunc("GET /api/spaces/{id}/passes", a.auth(a.handleListPasses))
	mux.HandleFunc("DELETE /api/spaces/{id}/passes/{pass}", a.auth(a.handleRevokePass))
	mux.HandleFunc("POST /api/join-requests", a.auth(a.handleJoinRequest))
	mux.HandleFunc("GET /api/join-requests/{req}", a.auth(a.handleJoinStatus))
	mux.HandleFunc("GET /api/spaces/{id}/appearance", a.auth(a.handleAppearance))
	mux.HandleFunc("GET /api/spaces/{id}/composition", a.auth(a.handleComposition))
	mux.HandleFunc("GET /api/spaces/{id}/bundles", a.auth(a.handleBundles))
	mux.HandleFunc("POST /api/lan/connect", a.auth(a.handleConnect))
	mux.HandleFunc("POST /api/mesh/connect", a.auth(a.handleMeshConnect))
	mux.HandleFunc("POST /api/relay/push", a.auth(a.handleRelayPush))
	mux.HandleFunc("POST /api/relay/pull", a.auth(a.handleRelayPull))
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

func (a *APIServer) handleOnboarding(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"needs_name":  a.rt.NeedsOnboarding(),
		"name":        a.rt.DisplayName(),
		"device_name": deviceName(),
		"fingerprint": a.rt.Principal.Fingerprint(),
	})
}

func (a *APIServer) handleSetName(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Name string `json:"name"`
	}](r)
	if err != nil || strings.TrimSpace(body.Name) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("name required"))
		return
	}
	if err := a.rt.SetName(body.Name); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{"name": a.rt.DisplayName()})
}

// deviceName returns a friendly, platform-neutral device label.
func deviceName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "this device"
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

type characterResp struct {
	Archetype string   `json:"archetype"`
	Mood      string   `json:"mood"`
	Material  string   `json:"material"`
	Motion    string   `json:"motion"`
	Geometry  string   `json:"geometry"`
	Central   string   `json:"central"`
	Memory    string   `json:"memory"`
	Relic     string   `json:"relic"`
	Rituals   []string `json:"rituals"`
	Presence  []string `json:"presence"`
}

func characterOf(c terminals.Character) characterResp {
	return characterResp{
		Archetype: c.Archetype, Mood: c.Mood, Material: c.Material,
		Motion: c.Motion, Geometry: c.Geometry, Central: c.Central,
		Memory: c.Memory, Relic: c.Relic,
		Rituals: c.Rituals, Presence: c.Presence,
	}
}

type spaceResp struct {
	ID            string        `json:"id"`
	Title         string        `json:"title"`
	Owned         bool          `json:"owned"`
	Events        int           `json:"events"`
	Messages      int           `json:"messages"`
	Undecryptable int           `json:"undecryptable"`
	Peers         int           `json:"peers"`
	Character     characterResp `json:"character"`
}

func (a *APIServer) handleSpaces(w http.ResponseWriter, r *http.Request) {
	spaces := a.rt.Spaces()
	out := make([]spaceResp, 0, len(spaces))
	for _, s := range spaces {
		resp := spaceResp{
			ID: s.ID.Hex(), Title: s.Title, Owned: s.Owned,
			Events: s.Events, Messages: s.Messages,
			Undecryptable: s.Undecryptable, Peers: s.Peers,
		}
		if sp, ok := a.rt.Space(s.ID); ok {
			_, c := sp.Character()
			resp.Character = characterOf(c)
		}
		out = append(out, resp)
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
		Title     string   `json:"title"`
		Archetype string   `json:"archetype"`
		Mood      string   `json:"mood"`
		Material  string   `json:"material"`
		Motion    string   `json:"motion"`
		Geometry  string   `json:"geometry"`
		Central   string   `json:"central"`
		Memory    string   `json:"memory"`
		Rituals   []string `json:"rituals"`
		Presence  []string `json:"presence"` // extra custom states
	}](r)
	if err != nil || strings.TrimSpace(body.Title) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("title required"))
		return
	}
	arch := body.Archetype
	if arch == "" {
		arch = "campfire"
	}
	c := terminals.DefaultCharacter(arch)
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&c.Mood, body.Mood)
	set(&c.Material, body.Material)
	set(&c.Motion, body.Motion)
	set(&c.Geometry, body.Geometry)
	set(&c.Central, body.Central)
	set(&c.Memory, body.Memory)
	if len(body.Rituals) > 0 {
		c.Rituals = body.Rituals
	}
	for _, p := range body.Presence {
		if !slices.Contains(c.Presence, p) {
			c.Presence = append(c.Presence, p)
		}
	}
	tid, err := a.rt.CreateSpaceWithCharacter(strings.TrimSpace(body.Title), c)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
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
	resp := map[string]string{"invite": invite}
	// QR of the invite string (vision §6.2 step 2): scan or send, any channel.
	if png, err := qrcode.Encode(invite, qrcode.Medium, 512); err == nil {
		resp["qr_png_base64"] = base64.StdEncoding.EncodeToString(png)
	}
	writeJSON(w, resp)
}

type memberResp struct {
	Terminal       string   `json:"terminal"`
	Principal      string   `json:"principal"`
	Name           string   `json:"name"`
	Kind           string   `json:"kind"`
	Agency         string   `json:"agency"`
	AIPresent      bool     `json:"ai_present"`
	Autonomy       string   `json:"autonomy"`
	Model          string   `json:"model"` // "not specified" unless declared
	IOMode         string   `json:"io_mode"`
	Capabilities   []string `json:"capabilities"`
	DeclaredLabels []string `json:"declared_labels"`
	SysLabels      []string `json:"sys_labels"`
	Commandable    bool     `json:"commandable"`
	Presence       struct {
		Known   bool   `json:"known"`
		Current bool   `json:"current"`
		State   string `json:"state,omitempty"`
		AgeSec  uint64 `json:"age_seconds,omitempty"`
	} `json:"presence"`
}

func (a *APIServer) handleMembers(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	cards, err := a.rt.Members(tid)
	if err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	out := make([]memberResp, 0, len(cards))
	for _, c := range cards {
		m := memberResp{
			Terminal: c.Terminal.Hex(), Principal: c.Principal.Hex(), Name: c.Name,
			Kind: c.Kind, Agency: c.Agency,
			AIPresent: c.AIPresent, Autonomy: terminals.AutonomyLabel(c.Autonomy),
			Model: "not specified", IOMode: c.IOMode,
			Capabilities: c.Capabilities, DeclaredLabels: c.DeclaredLabels,
			SysLabels: c.SysLabels, Commandable: c.CanReceiveCommands,
		}
		m.Presence.Known = c.Presence.Known
		m.Presence.Current = c.Presence.Current
		m.Presence.State = c.Presence.State
		m.Presence.AgeSec = c.Presence.AgeSeconds
		out = append(out, m)
	}
	writeJSON(w, out)
}

func (a *APIServer) handlePresence(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		State string `json:"state"`
		TTL   uint64 `json:"ttl_seconds"`
	}](r)
	if err != nil || body.State == "" {
		httpErr(w, http.StatusBadRequest, errors.New("state required"))
		return
	}
	if body.TTL == 0 {
		body.TTL = 300
	}
	if err := a.rt.SetPresence(tid, body.State, body.TTL); err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
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

// ---- Space Pass (ADR-012 / UI-2) ----

// handleMintPass mints a bearer-secret Join Pass for an owned space. The
// returned link bundles the rendezvous relay with the signed pass; the bearer
// secret lives only inside that link, never in the pass registry on this node.
func (a *APIServer) handleMintPass(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		MaxUses  uint64 `json:"max_uses"`
		TTLHours uint64 `json:"ttl_hours"`
		Relay    string `json:"relay"`
	}](r)
	if err != nil || body.Relay == "" {
		httpErr(w, http.StatusBadRequest, errors.New("relay required"))
		return
	}
	info, err := a.rt.MintPass(tid, body.MaxUses, body.TTLHours, body.Relay)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	resp := map[string]any{
		"pass_id":    info.PassID,
		"link":       info.Link,
		"expires_at": info.ExpiresAt,
		"max_uses":   info.MaxUses,
	}
	if png, err := qrcode.Encode(info.Link, qrcode.Medium, 512); err == nil {
		resp["qr_png_base64"] = base64.StdEncoding.EncodeToString(png)
	}
	writeJSON(w, resp)
}

func (a *APIServer) handleListPasses(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	want := tid.Hex()
	out := []map[string]any{}
	for _, p := range a.rt.ListPasses() {
		if p.Space != want {
			continue
		}
		out = append(out, map[string]any{
			"pass_id": p.PassID, "expires_at": p.ExpiresAt,
			"max_uses": p.MaxUses, "used": p.Used, "revoked": p.Revoked,
		})
	}
	writeJSON(w, map[string]any{"passes": out})
}

// handleRevokePass blocks a pass for new and pending requests. It never
// removes members already accepted (ADR-012 invariant 6: revoke ≠ remove).
func (a *APIServer) handleRevokePass(w http.ResponseWriter, r *http.Request) {
	if err := a.rt.RevokePass(r.PathValue("pass")); err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, map[string]string{"status": "revoked"})
}

// handleJoinRequest starts an async join: it sends a sealed request to the
// pass's rendezvous and returns a request id to poll. No space is opened yet —
// pending has no access until the owner's device confirms.
func (a *APIServer) handleJoinRequest(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Pass string `json:"pass"`
	}](r)
	if err != nil || strings.TrimSpace(body.Pass) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("pass required"))
		return
	}
	reqID, err := a.rt.JoinByPass(strings.TrimSpace(body.Pass))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{
		"request_id": reqID,
		"status":     string(JoinWaiting),
	})
}

func (a *APIServer) handleJoinStatus(w http.ResponseWriter, r *http.Request) {
	state, space := a.rt.JoinStatus(r.PathValue("req"))
	resp := map[string]any{"status": string(state)}
	if space != "" {
		resp["space"] = space
	}
	writeJSON(w, resp)
}

func (a *APIServer) handleRelayPush(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Addr  string `json:"addr"`
		Space string `json:"space"`
	}](r)
	if err != nil || body.Addr == "" || body.Space == "" {
		httpErr(w, http.StatusBadRequest, errors.New("addr and space required"))
		return
	}
	tid, err := id.ParseTerminalID(body.Space)
	if err != nil {
		httpErr(w, http.StatusBadRequest, errors.New("bad space id"))
		return
	}
	pushed, deadline, err := a.rt.PushToRelay(body.Addr, tid)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err)
		return
	}
	// Honest wording: the relay accepted the bundle; nobody received it.
	writeJSON(w, map[string]any{
		"events_pushed":     pushed,
		"relay_holds_until": deadline,
		"status":            "accepted_by_relay",
	})
}

func (a *APIServer) handleRelayPull(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Addr string `json:"addr"`
	}](r)
	if err != nil || body.Addr == "" {
		httpErr(w, http.StatusBadRequest, errors.New("addr required"))
		return
	}
	applied, err := a.rt.PullFromRelay(body.Addr)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]int{"events_applied": applied})
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

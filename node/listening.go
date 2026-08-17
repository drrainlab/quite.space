// Listening room (LR-2), node side: the built-in app template, the
// host-gated command path (epoch/sequence counters persisted locally), the
// session projection API, and the SyncClock time source. The audio element
// on any client is NEVER protocol state — the log carries only durable
// host commands; every position is computed.
package node

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/protocol/appdef"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/listening"
)

func init() {
	builtinApps["listening-room"] = &appdef.Definition{
		AppID: "listening-room", Name: "Listening room",
		Inputs: map[string]appdef.Input{
			"commands": {Schema: listening.SchemaCommand,
				Where: &appdef.Where{Field: "instance_id", Value: "$instance.id"}, Limit: 200},
		},
		State: map[string]appdef.Reducer{
			"commands": {Kind: "list", Input: "commands"},
		},
		Actions: map[string]appdef.Action{
			"command": {Emit: listening.SchemaCommand,
				Fields: []string{"action", "position_ms", "start_session"}},
		},
		Requested: []appdef.Capability{
			{Kind: "read", Schemas: []string{listening.SchemaCommand}},
			{Kind: "append", Schemas: []string{listening.SchemaCommand}},
		},
		Fallback: "A listening room. One track, one shared moment.",
	}
	appPolicyRead = appdef.NewCapSet([]appdef.Capability{{
		Kind: "read", Schemas: []string{appdef.SchemaPollVote, appdef.SchemaFormResponse, listening.SchemaCommand},
	}})
	appPolicyAppend = appdef.NewCapSet([]appdef.Capability{{
		Kind: "append", Schemas: []string{appdef.SchemaPollVote, appdef.SchemaFormResponse, listening.SchemaCommand},
	}})
}

// ---- host command counters (persisted; "Start session" → epoch++, seq=1) ----

type listeningCounters struct {
	Epoch    uint64 `json:"epoch"`
	Sequence uint64 `json:"sequence"`
}

func countersName(iid [16]byte) string {
	return "listening-" + hex.EncodeToString(iid[:])
}

func (r *Runtime) loadListeningCounters(iid [16]byte) listeningCounters {
	var c listeningCounters
	b, err := r.root.LoadSealed(countersName(iid))
	if err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

func (r *Runtime) saveListeningCounters(iid [16]byte, c listeningCounters) {
	b, _ := json.Marshal(c)
	_ = r.root.SaveSealed(countersName(iid), b)
}

// emitListeningCommand is the schema-specific branch of AppAction: only the
// PERMANENT host (instance creator) may emit; the node assigns
// (session_epoch, sequence) and the effective_at instant on the shared
// timeline. Called with r.mu held.
func (r *Runtime) emitListeningCommand(st *spaceState, iid [16]byte,
	instAuthor id.PrincipalID, props map[string]string, fields map[string]any) (id.EventID, error) {

	if instAuthor != r.PrincipalID {
		return id.EventID{}, errors.New("node: only the host controls the session (no transfer in v1)")
	}
	action, _ := fields["action"].(string)
	posF, _ := fields["position_ms"].(float64)
	if posF < 0 {
		return id.EventID{}, errors.New("node: position must be ≥ 0")
	}
	pos := uint64(posF)
	start, _ := fields["start_session"].(bool)

	// Position bound: an explicit seek beyond the declared duration is
	// refused; play/pause positions clamp to the track end (a pause pressed
	// after the track ran out is still a legitimate command).
	if durStr := props["duration_ms"]; durStr != "" {
		var dur uint64
		if _, err := fmt.Sscanf(durStr, "%d", &dur); err == nil && dur > 0 && pos > dur {
			if action == "seek" {
				return id.EventID{}, errors.New("node: seek beyond track duration")
			}
			pos = dur
		}
	}

	c := r.loadListeningCounters(iid)
	if start || c.Epoch == 0 {
		c.Epoch++
		c.Sequence = 1
	} else {
		c.Sequence++
	}

	// The command takes effect a short lead ahead of the shared timeline so
	// every follower can schedule it, clamped to the protocol bound.
	nowMS, _, _ := r.sharedNow()
	cmd := &listening.Command{
		Action:        action,
		PositionMS:    pos,
		EffectiveAtMS: nowMS + 400,
		SessionEpoch:  c.Epoch,
		Sequence:      c.Sequence,
	}
	payload, err := listening.Encode(iid, cmd)
	if err != nil {
		return id.EventID{}, err
	}
	eid, err := r.emitLocked(st, listening.SchemaCommand, payload)
	if err != nil {
		return id.EventID{}, err
	}
	r.saveListeningCounters(iid, c)
	return eid, nil
}

// ---- SyncClock (LR-2, correction 4): ONE common source ----
//
// Every participant calibrates against the same relay; the node may PROXY
// relay time but never substitutes its own clock as that source. Without a
// relay the node honestly reports itself as the source — two clients on
// different sources must show `approximate`, never fake `calibrated`.

type relayClock struct {
	mu            sync.Mutex
	offsetMS      int64 // relay_now - local_now
	uncertaintyMS uint64
	calibratedAt  time.Time
	sourceID      string
}

// CalibrateRelayClock samples the relay several times and keeps the minimum
// RTT sample (monotonic clock for RTT — wall time is what's being measured).
func (r *Runtime) CalibrateRelayClock(addr string) error {
	client, err := r.dialRelay(addr)
	if err != nil {
		return err
	}
	defer client.Close()

	bestRTT := time.Duration(-1)
	var bestOffset int64
	for i := 0; i < 5; i++ {
		relayNow, rtt, err := client.Time()
		if err != nil {
			return err
		}
		local := time.Now().UnixMilli()
		offset := int64(relayNow) + rtt.Milliseconds()/2 - local
		if bestRTT < 0 || rtt < bestRTT {
			bestRTT = rtt
			bestOffset = offset
		}
	}
	r.relayClk.mu.Lock()
	r.relayClk.offsetMS = bestOffset
	r.relayClk.uncertaintyMS = uint64(bestRTT.Milliseconds()/2) + 5
	r.relayClk.calibratedAt = time.Now()
	r.relayClk.sourceID = "relay:" + addr
	r.relayClk.mu.Unlock()
	return nil
}

// sharedNow returns the node's current estimate of the shared timeline in
// unix ms, its source id, and the uncertainty. Stale calibration honestly
// grows the uncertainty; with no relay the source is this node itself.
func (r *Runtime) sharedNow() (nowMS uint64, sourceID string, uncertaintyMS uint64) {
	local := time.Now().UnixMilli()
	r.relayClk.mu.Lock()
	defer r.relayClk.mu.Unlock()
	if r.relayClk.sourceID == "" {
		return uint64(local), "node:" + r.Fingerprint(), 0
	}
	age := time.Since(r.relayClk.calibratedAt)
	unc := r.relayClk.uncertaintyMS + uint64(age.Minutes())*10 // drift allowance
	return uint64(local + r.relayClk.offsetMS), r.relayClk.sourceID, unc
}

// ---- API ----

// handleTime serves the calibration endpoint. ?relay=ADDR (re)calibrates
// against that relay first; otherwise the current source answers.
func (a *APIServer) handleTime(w http.ResponseWriter, r *http.Request) {
	if addr := r.URL.Query().Get("relay"); addr != "" {
		if err := a.rt.CalibrateRelayClock(addr); err != nil {
			httpErr(w, http.StatusBadGateway, err)
			return
		}
	}
	now, source, unc := a.rt.sharedNow()
	writeJSON(w, map[string]any{
		"now_ms":         now,
		"source_id":      source,
		"uncertainty_ms": unc,
	})
}

// handleListeningSession projects one instance's session for rendering:
// folded host command, track props with asset status, and the shared clock.
func (a *APIServer) handleListeningSession(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	// The instance id needs no space, and it is the only 400 here.
	iid, err := hex16(r.PathValue("instance"))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	var out map[string]any
	if err := a.rt.withSpace(tid, func(st *spaceState) error {
		sess, ok := st.space.State.ListeningSession(iid)
		if !ok {
			return errors.New("unknown app instance")
		}
		rec, _ := st.space.State.AppInstanceByID(iid)

		me := a.rt.PrincipalID
		names := map[id.PrincipalID]string{me: a.rt.displayNameLocked()}
		for _, c := range st.space.MemberCards(0) {
			if c.Name != "" {
				names[c.Principal] = c.Name
			}
		}

		out = map[string]any{
			"host":      sess.Host.Hex(),
			"host_name": names[sess.Host],
			"i_am_host": sess.Host == me,
			"ignored":   map[string]int{"non_host": sess.IgnoredNonHost, "malformed": sess.IgnoredMalformed},
		}
		if sess.HasCommand {
			out["command"] = sess.Command
			out["command_clock"] = sess.Clock
		}
		if rec != nil {
			track := map[string]any{
				"title":       rec.Instance.Props["title"],
				"asset":       rec.Instance.Props["asset"],
				"duration_ms": rec.Instance.Props["duration_ms"],
			}
			if aidHex := rec.Instance.Props["asset"]; aidHex != "" {
				// assetStatusLocked, not AssetStatus: r.mu is already held.
				if as, err := a.rt.assetStatusLocked(AssetKey{Space: tid, Asset: aidHex}); err == nil {
					track["asset_state"] = string(as.State)
					track["asset_missing"] = as.Missing
					track["asset_total"] = as.Total
				} else {
					track["asset_state"] = "unknown"
				}
			}
			out["track"] = track
		}
		return nil
	}); err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	// The shared clock lives behind its own mutex and does not touch the
	// replica — read it outside the space lock, which keeps that scope short
	// and keeps the two locks unordered with respect to each other.
	now, source, unc := a.rt.sharedNow()
	out["now_ms"] = now
	out["source_id"] = source
	out["uncertainty_ms"] = unc
	writeJSON(w, out)
}

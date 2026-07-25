// QuietRank HTTP surface.
//
// The split here is deliberate and load-bearing: LEARNING verdicts (useful /
// not mine / you should have caught this) go to the ranking model, while
// POLICY commands (mute a space, digest only, stop watching a phrase) go to
// the policy. Silencing a room is not a statement that its content is
// worthless, and conflating the two would teach the model the wrong lesson.
package node

import (
	"errors"
	"net/http"

	"github.com/drrainlab/quiet_places/attention"
	"github.com/drrainlab/quiet_places/protocol/id"
)

type signalsResp struct {
	Signals []attention.Signal `json:"signals"`
	Unseen  int                `json:"unseen"`
	Mode    attention.Mode     `json:"mode"`
	// ProcessedLocally is always true today and is stated explicitly rather
	// than implied: the UI shows it, so the API must own the claim.
	ProcessedLocally bool `json:"processed_locally"`
}

func (a *APIServer) handleSignals(w http.ResponseWriter, r *http.Request) {
	sigs := a.rt.Signals()
	writeJSON(w, signalsResp{
		Signals: sigs, Unseen: a.rt.UnseenSignals(),
		Mode: a.rt.AttentionPolicy().Mode, ProcessedLocally: true,
	})
}

// handleSignalSeen marks one signal read, or all of them when the id is
// "all" — the badge must be able to go quiet without judging anything.
func (a *APIServer) handleSignalSeen(w http.ResponseWriter, r *http.Request) {
	sig := r.PathValue("id")
	if sig == "all" {
		sig = ""
	}
	a.rt.MarkSignalsSeen(sig)
	writeJSON(w, map[string]int{"unseen": a.rt.UnseenSignals()})
}

// handleSignalFeedback teaches the ranking model. Only two verdicts reach
// it; everything else is policy.
func (a *APIServer) handleSignalFeedback(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Verdict string `json:"verdict"` // useful | not_mine
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	var useful bool
	switch body.Verdict {
	case "useful":
		useful = true
	case "not_mine":
		useful = false
	default:
		httpErr(w, http.StatusBadRequest, errors.New("verdict must be useful or not_mine"))
		return
	}
	if err := a.rt.SignalFeedback(r.PathValue("id"), useful); err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, map[string]string{"status": "learned"})
}

// handleAttentionNotice is "you should have caught this", pointed at an
// ordinary message. It is a POSITIVE example: without it the layer could
// only ever learn to go quiet.
func (a *APIServer) handleAttentionNotice(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Space string `json:"space"`
		Event string `json:"event"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	tid, err := id.ParseTerminalID(body.Space)
	if err != nil {
		httpErr(w, http.StatusBadRequest, errors.New("bad space id"))
		return
	}
	eid, err := parseEventID(body.Event)
	if err != nil {
		httpErr(w, http.StatusBadRequest, errors.New("bad event id"))
		return
	}
	if err := a.rt.NoticeEvent(tid, eid); err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, map[string]string{"status": "learned"})
}

func (a *APIServer) handleGetAttentionPolicy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.rt.AttentionPolicy())
}

// handleSetAttentionPolicy replaces the policy. Policy changes never touch
// the ranking model (see the package comment).
func (a *APIServer) handleSetAttentionPolicy(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[attention.Policy](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	switch body.Mode {
	case attention.ModeOff, attention.ModeMinimal, attention.ModeCustom:
	case "":
		body.Mode = attention.ModeMinimal
	default:
		httpErr(w, http.StatusBadRequest, errors.New("unknown mode"))
		return
	}
	for hex, scope := range body.Spaces {
		if _, err := id.ParseTerminalID(hex); err != nil {
			httpErr(w, http.StatusBadRequest, errors.New("bad space id in scopes"))
			return
		}
		switch scope {
		case attention.ScopeOff, attention.ScopeDirectOnly,
			attention.ScopeHighlights, attention.ScopeDigest:
		default:
			httpErr(w, http.StatusBadRequest, errors.New("unknown scope"))
			return
		}
	}
	if err := a.rt.SetAttentionPolicy(body); err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, a.rt.AttentionPolicy())
}

// handleForgetAttention deletes the learned profile and every stored signal.
// "Delete what it learned about me" has to actually delete.
func (a *APIServer) handleForgetAttention(w http.ResponseWriter, r *http.Request) {
	if err := a.rt.ForgetAttention(); err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]string{"status": "forgotten"})
}

// handleViewing tells the node which space is open, so QuietRank stays quiet
// about the room the person is already reading.
func (a *APIServer) handleViewing(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Space string `json:"space"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	var tid id.TerminalID
	if body.Space != "" {
		tid, err = id.ParseTerminalID(body.Space)
		if err != nil {
			httpErr(w, http.StatusBadRequest, errors.New("bad space id"))
			return
		}
	}
	a.rt.SetViewing(tid)
	w.WriteHeader(http.StatusNoContent)
}

package node

// The UI seam for knocking on a person (ADR-027).
//
// It is deliberately the DOOR's shape one floor up: a list of who is
// waiting, and one verb that answers. The three answers are named on the
// wire exactly as the ADR names them, because a screen that offered
// "accept / reject" would be describing a different feature — one where
// refusing is a judgement rather than an answer.
//
// What this seam refuses to do: it never returns the pass a knock carried
// and never exposes the sealed envelope. A page renders WHO is asking,
// FROM WHERE, and WHAT THEY SAID — the answer is taken by the node.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/drrainlab/quiet_places/protocol/id"
)

func (a *APIServer) handleKnocks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"knocks": a.rt.Knocks()})
}

func (a *APIServer) handleAnswerKnock(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Answer string `json:"answer"` // let_in | not_now | do_not_ask
		Reason string `json:"reason"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	answer := KnockAnswer(strings.TrimSpace(body.Answer))
	switch answer {
	case KnockLetIn, KnockNotNow, KnockNever:
	default:
		httpErr(w, http.StatusBadRequest, errors.New("answer must be let_in, not_now or do_not_ask"))
		return
	}
	if err := a.rt.AnswerKnock(r.PathValue("id"), answer, strings.TrimSpace(body.Reason)); err != nil {
		httpErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"answered": string(answer)})
}

// handleKnockOn asks somebody met in THIS space for a conversation. The
// space is in the path because it is the acquaintance, not a detail: a
// knock without the room that justifies it is what this design refuses.
func (a *APIServer) handleKnockOn(w http.ResponseWriter, r *http.Request) {
	tid, err := id.ParseTerminalID(r.PathValue("id"))
	if err != nil {
		httpErr(w, http.StatusBadRequest, errors.New("bad space id"))
		return
	}
	body, err := readBody[struct {
		Principal string `json:"principal"`
		Line      string `json:"line"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	who, err := id.ParsePrincipalID(strings.TrimSpace(body.Principal))
	if err != nil {
		httpErr(w, http.StatusBadRequest, errors.New("bad principal"))
		return
	}
	space, err := a.rt.KnockOn(tid, who, strings.TrimSpace(body.Line))
	if err != nil {
		httpErr(w, http.StatusConflict, err)
		return
	}
	// The room exists on this side already; it opens for them only if they
	// answer. Named so a screen can say "asked" and go back to it.
	writeJSON(w, map[string]string{"space": space})
}

func (a *APIServer) handleRefusals(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, rf := range a.rt.Refusals() {
		out = append(out, map[string]any{
			"principal": rf.Principal.Hex(),
			"reason":    rf.Reason,
			"at":        rf.At,
		})
	}
	writeJSON(w, map[string]any{"refusals": out})
}

func (a *APIServer) handleUnrefuse(w http.ResponseWriter, r *http.Request) {
	who, err := id.ParsePrincipalID(r.PathValue("principal"))
	if err != nil {
		httpErr(w, http.StatusBadRequest, errors.New("bad principal"))
		return
	}
	if err := a.rt.UnrefusePerson(who); err != nil {
		httpErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"status": "lifted"})
}

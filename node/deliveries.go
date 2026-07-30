// What happened to a message after it was sent (SHARE-2).
//
// Runtime.Delivery has existed since RB-1 and been reachable from nothing:
// the whole honest vocabulary — what is proven, what is carrying it, and
// WHY nothing is moving when nothing is — sat behind a Go method no client
// could call. Sharing is what finally needs it, because one act with three
// destinations has three answers and "sent" is not one of them.
//
// One rule governs this file. AN UNTRACKED ID IS NOT A PENDING ONE. The
// ledger may have no intent (it settled and was compacted; the node opened
// without one), and the only honest projection of "we are not tracking
// this" is exactly that — never an invented "pending" that a person would
// read as a promise still outstanding.
package node

import (
	"errors"
	"net/http"
	"strings"
)

// maxDeliveryQuery bounds one question. A share is at most eight
// destinations and each is one event, so this is generous by design.
const maxDeliveryQuery = 32

type deliveryResp struct {
	Event string `json:"event"`
	// Tracked false means the ledger holds no intent for this event. The
	// client renders NOTHING for it — see the file header.
	Tracked bool   `json:"tracked"`
	Space   string `json:"space,omitempty"`
	State   string `json:"state,omitempty"`
	// Proof is the ADR-007 level actually established. A transport can
	// never assert past accepted_by_relay, and nothing here raises it.
	Proof     string `json:"proof,omitempty"`
	Transport string `json:"transport,omitempty"`
	Route     string `json:"route,omitempty"`
	// Waiting says WHY nothing is moving when nothing is: "connectivity"
	// when policy permits no route at all, "faster_link" when this message
	// cannot fit the only route permitted. Empty when something has it —
	// a message in a gateway's custody is not waiting on us.
	Waiting      string `json:"waiting,omitempty"`
	LeaseExpires int64  `json:"lease_expires,omitempty"`
}

func (a *APIServer) handleDeliveries(w http.ResponseWriter, r *http.Request) {
	raw := strings.Split(r.URL.Query().Get("ids"), ",")
	if len(raw) > maxDeliveryQuery {
		httpErr(w, http.StatusBadRequest, errors.New("node: too many events at once"))
		return
	}
	out := make([]deliveryResp, 0, len(raw))
	for _, hexID := range raw {
		hexID = strings.TrimSpace(hexID)
		if hexID == "" {
			continue
		}
		eid, err := parseEventHex(hexID)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
		v, ok := a.rt.Delivery(eid)
		if !ok {
			out = append(out, deliveryResp{Event: hexID, Tracked: false})
			continue
		}
		out = append(out, deliveryResp{
			Event: hexID, Tracked: true, Space: v.Space.Hex(),
			State: v.State, Proof: v.Proof, Transport: v.Transport,
			Route: v.Route, Waiting: v.Waiting, LeaseExpires: v.LeaseExpires,
		})
	}
	writeJSON(w, map[string]any{"deliveries": out})
}

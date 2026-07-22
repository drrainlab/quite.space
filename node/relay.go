// Store-and-forward via blind relay (M1.5): push a space's frames to a
// relay under its rotating hint; an offline peer pulls them later. The
// relay sees a hint and a ciphertext bundle — payloads are epoch-encrypted
// (ADR-005) and the trust engine records exactly accepted_by_relay for
// pushed events, never delivery (ADR-008).
package node

import (
	"errors"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/bundle"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// DefaultRelayTTL bounds how long pushed bundles wait for their recipient.
const DefaultRelayTTL = 48 * time.Hour

// PushToRelay uploads the full space bundle under the current-bucket hint.
// Returns how many events were pushed and the relay's accepted deadline.
func (r *Runtime) PushToRelay(addr string, tid id.TerminalID) (int, uint64, error) {
	r.mu.Lock()
	st, ok := r.spaces[tid]
	if !ok {
		r.mu.Unlock()
		return 0, 0, errors.New("node: unknown space")
	}
	var frames [][]byte
	var eventIDs []id.EventID
	if err := st.space.Log.Replay(func(a eventlog.Applied) error {
		frames = append(frames, a.Frame)
		eventIDs = append(eventIDs, a.ID)
		return nil
	}); err != nil {
		r.mu.Unlock()
		return 0, 0, err
	}
	body := bundle.Encode(tid, frames)
	r.mu.Unlock()

	client, err := relay.DialClient(addr)
	if err != nil {
		return 0, 0, err
	}
	defer client.Close()
	now := uint64(time.Now().Unix())
	hint := relay.Hint(tid, relay.Bucket(now))
	deadline, err := client.Put(hint, now+uint64(DefaultRelayTTL/time.Second), body)
	if err != nil {
		return 0, 0, err
	}

	// Record the honest receipt level for every pushed event: the relay
	// accepted them; nobody received anything yet.
	r.mu.Lock()
	for _, eid := range eventIDs {
		_ = st.space.Trust.RecordTransportReceipt(eid, tid, claims.DeliveryAcceptedByRelay)
	}
	r.mu.Unlock()
	return len(frames), deadline, nil
}

// PullFromRelay collects bundles for every known space (current and
// previous hint buckets) and absorbs them. Idempotent: duplicates are
// no-ops in the event log.
func (r *Runtime) PullFromRelay(addr string) (applied int, err error) {
	r.mu.Lock()
	tids := make([]id.TerminalID, 0, len(r.spaces))
	for tid := range r.spaces {
		tids = append(tids, tid)
	}
	r.mu.Unlock()
	if len(tids) == 0 {
		return 0, nil
	}

	client, err := relay.DialClient(addr)
	if err != nil {
		return 0, err
	}
	defer client.Close()

	now := uint64(time.Now().Unix())
	var hints [][]byte
	for _, tid := range tids {
		b := relay.Bucket(now)
		hints = append(hints, relay.Hint(tid, b))
		if b > 0 {
			hints = append(hints, relay.Hint(tid, b-1))
		}
	}
	items, err := client.Collect(hints)
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		terminal, frames, err := bundle.Decode(item)
		if err != nil {
			continue // not a bundle we understand; ignore quietly
		}
		r.mu.Lock()
		st, ok := r.spaces[terminal]
		if !ok {
			r.mu.Unlock()
			continue
		}
		for _, f := range frames {
			as, err := st.space.Log.Ingest(f)
			if err != nil {
				continue
			}
			for _, a := range as {
				st.space.AttachSyncApply(a)
				applied++
			}
		}
		r.persistEpochsLocked(terminal, st.space)
		r.mu.Unlock()
	}
	return applied, nil
}

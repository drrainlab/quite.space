// The join saga on disk (QL-0, ADR-012 invariant 7: "a per-request_id saga
// journal SURVIVES RESTARTS; reprocessing returns the stored result").
//
// This lives in the ENCRYPTED keystore rather than a plain file for one
// reason that outranks the others: admission mutates membership, rotates the
// epoch and consumes a use, and all three must land in ONE write. A separate
// file is a second write that can succeed alone, and the window between them
// is exactly ADR-012 invariant 8 — "one request consumes at most one use and
// causes at most one rotation" — broken by a power cut.
//
// The marginal disclosure is nil: the keystore already holds the space
// signing keys (with which you can mint passes at will) and every epoch key.
// A 16-byte rendezvous grants strictly less than either.
package storage

import (
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// EntryOutcome is the exact fate of one request. Not a generic "refused":
// after a restart the owner must be able to reproduce the TRUE reason, and
// a re-delivery must never report a different fate for the same request.
type EntryOutcome uint8

const (
	OutcomeGranted EntryOutcome = iota + 1
	OutcomeDeclinedByHost
	OutcomeExpiredWaiting
	OutcomeWithdrawn
	OutcomeCapacityExhausted
	OutcomeRevoked
)

// EntryState groups outcomes for the queue a person reads. The wire answer
// is always reproduced from the Outcome, never from this.
type EntryState uint8

const (
	EntryPending EntryState = iota + 1
	EntryAdmitted
	EntryDeclined
	EntryExpired
)

// HandledRequest is the canonical idempotency journal: request id → fate.
// Deliberately NOT the sealed acceptance bytes — those would put epoch
// material on disk a second time, and they are re-derivable, because
// AcceptIntoSpace returns the current epoch for an existing member without
// rotating.
type HandledRequest struct {
	Request [32]byte
	Outcome EntryOutcome
	At      uint64
}

// EntryRecord is somebody at the door, and later somebody who was let in or
// turned away. One record moves through its states; nothing is copied
// between structures, because once a pending row is deleted the name and
// the timestamps are gone and the queue has nothing to show.
//
// It carries NO bearer secret: the request was validated when it was
// collected and the secret is never needed again. If the original request
// material is gone after a restart, the decision is re-sealed when the guest
// re-delivers the same request id.
type EntryRecord struct {
	Request [32]byte
	Device  id.DeviceID
	Xpub    [32]byte
	// Name is a snapshot for presentation only — self-declared, unverified,
	// and never used for any decision.
	Name      string
	AskedAt   uint64
	DecidedAt uint64
	State     EntryState
	Outcome   EntryOutcome
	// Routes is the knocker's ReturnRouteHint (RT-0), held with the entry
	// because the host may approve hours after the knock — and admitting
	// somebody whose return address was already forgotten would grant a
	// membership nobody can deliver to.
	Routes []string
}

// PassRecord is the owner's durable acceptance state for one pass.
type PassRecord struct {
	Space id.TerminalID
	// Frame is the SIGNED pass frame, verbatim. Rendezvous, expiry,
	// max-uses and pass id all live inside it and re-verify against the
	// space id on load — one field, no drift, nothing to keep in sync.
	Frame []byte
	// Relay is the rendezvous this pass was minted against. Per-record,
	// because a single global address left passes on a second relay in a
	// mailbox nobody watched.
	Relay    string
	Used     uint64
	Revoked  bool
	Approval string // "" open · "host" (QL-2)
	Handled  []HandledRequest
	Entries  []EntryRecord
}

// JoinRecord is the guest's half of the same saga. This one DOES hold the
// bearer secret, because the guest must be able to re-send its request —
// which is how a decision made while it was offline still reaches it.
type JoinRecord struct {
	Space     id.TerminalID
	PassFrame []byte
	Secret    [32]byte
	Request   [32]byte
	Relay     string
	State     string
	StartedAt uint64
	// CollectUntil is deliberately later than the pass's expiry: expiry
	// forbids a NEW request, never the completion of one already recorded.
	CollectUntil uint64
}

// ---- codec ----

func appendPassRecords(buf []byte, recs []PassRecord) []byte {
	buf = codec.AppendArray(buf, len(recs))
	for _, r := range recs {
		buf = codec.AppendArray(buf, 8)
		buf = codec.AppendBytes(buf, r.Space[:])
		buf = codec.AppendBytes(buf, r.Frame)
		buf = codec.AppendText(buf, r.Relay)
		buf = codec.AppendUint(buf, r.Used)
		buf = codec.AppendBool(buf, r.Revoked)
		buf = codec.AppendText(buf, r.Approval)
		buf = codec.AppendArray(buf, len(r.Handled))
		for _, h := range r.Handled {
			buf = codec.AppendArray(buf, 3)
			buf = codec.AppendBytes(buf, h.Request[:])
			buf = codec.AppendUint(buf, uint64(h.Outcome))
			buf = codec.AppendUint(buf, h.At)
		}
		buf = codec.AppendArray(buf, len(r.Entries))
		for _, e := range r.Entries {
			buf = codec.AppendArray(buf, 9)
			buf = codec.AppendBytes(buf, e.Request[:])
			buf = codec.AppendBytes(buf, e.Device[:])
			buf = codec.AppendBytes(buf, e.Xpub[:])
			buf = codec.AppendText(buf, e.Name)
			buf = codec.AppendUint(buf, e.AskedAt)
			buf = codec.AppendUint(buf, e.DecidedAt)
			buf = codec.AppendUint(buf, uint64(e.State))
			buf = codec.AppendUint(buf, uint64(e.Outcome))
			buf = codec.AppendArray(buf, len(e.Routes))
			for _, rt := range e.Routes {
				buf = codec.AppendText(buf, rt)
			}
		}
	}
	return buf
}

func readPassRecords(d *codec.Decoder) ([]PassRecord, error) {
	n, err := d.ReadArray()
	if err != nil {
		return nil, err
	}
	out := make([]PassRecord, 0, n)
	for range n {
		acount, er := d.ReadArray()
		if er != nil {
			return nil, er
		}
		var r PassRecord
		b, er := d.ReadBytes()
		if er != nil {
			return nil, er
		}
		copy(r.Space[:], b)
		if r.Frame, er = readCopy(d); er != nil {
			return nil, er
		}
		if r.Relay, er = d.ReadText(); er != nil {
			return nil, er
		}
		if r.Used, er = d.ReadUint(); er != nil {
			return nil, er
		}
		if r.Revoked, er = d.ReadBool(); er != nil {
			return nil, er
		}
		if r.Approval, er = d.ReadText(); er != nil {
			return nil, er
		}
		hn, er := d.ReadArray()
		if er != nil {
			return nil, er
		}
		for range hn {
			if _, er = d.ReadArray(); er != nil {
				return nil, er
			}
			var h HandledRequest
			hb, e := d.ReadBytes()
			if e != nil {
				return nil, e
			}
			copy(h.Request[:], hb)
			o, e := d.ReadUint()
			if e != nil {
				return nil, e
			}
			h.Outcome = EntryOutcome(o)
			if h.At, e = d.ReadUint(); e != nil {
				return nil, e
			}
			r.Handled = append(r.Handled, h)
		}
		en, er := d.ReadArray()
		if er != nil {
			return nil, er
		}
		for range en {
			ec, e := d.ReadArray()
			if e != nil {
				return nil, e
			}
			var ent EntryRecord
			rb, e := d.ReadBytes()
			if e != nil {
				return nil, e
			}
			copy(ent.Request[:], rb)
			db, e := d.ReadBytes()
			if e != nil {
				return nil, e
			}
			copy(ent.Device[:], db)
			xb, e := d.ReadBytes()
			if e != nil {
				return nil, e
			}
			copy(ent.Xpub[:], xb)
			if ent.Name, e = d.ReadText(); e != nil {
				return nil, e
			}
			if ent.AskedAt, e = d.ReadUint(); e != nil {
				return nil, e
			}
			if ent.DecidedAt, e = d.ReadUint(); e != nil {
				return nil, e
			}
			sv, e := d.ReadUint()
			if e != nil {
				return nil, e
			}
			ent.State = EntryState(sv)
			ov, e := d.ReadUint()
			if e != nil {
				return nil, e
			}
			ent.Outcome = EntryOutcome(ov)
			if ec >= 9 {
				rc, e := d.ReadArray()
				if e != nil {
					return nil, e
				}
				for j := 0; j < rc; j++ {
					rt, e := d.ReadText()
					if e != nil {
						return nil, e
					}
					ent.Routes = append(ent.Routes, rt)
				}
			}
			// Forward compatibility: a newer build may write more fields.
			for i := 9; i < ec; i++ {
				if e := d.SkipItem(); e != nil {
					return nil, e
				}
			}
			r.Entries = append(r.Entries, ent)
		}
		for i := 8; i < acount; i++ {
			if e := d.SkipItem(); e != nil {
				return nil, e
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func appendJoinRecords(buf []byte, recs []JoinRecord) []byte {
	buf = codec.AppendArray(buf, len(recs))
	for _, r := range recs {
		buf = codec.AppendArray(buf, 8)
		buf = codec.AppendBytes(buf, r.Space[:])
		buf = codec.AppendBytes(buf, r.PassFrame)
		buf = codec.AppendBytes(buf, r.Secret[:])
		buf = codec.AppendBytes(buf, r.Request[:])
		buf = codec.AppendText(buf, r.Relay)
		buf = codec.AppendText(buf, r.State)
		buf = codec.AppendUint(buf, r.StartedAt)
		buf = codec.AppendUint(buf, r.CollectUntil)
	}
	return buf
}

func readJoinRecords(d *codec.Decoder) ([]JoinRecord, error) {
	n, err := d.ReadArray()
	if err != nil {
		return nil, err
	}
	out := make([]JoinRecord, 0, n)
	for range n {
		acount, er := d.ReadArray()
		if er != nil {
			return nil, er
		}
		var r JoinRecord
		b, er := d.ReadBytes()
		if er != nil {
			return nil, er
		}
		copy(r.Space[:], b)
		if r.PassFrame, er = readCopy(d); er != nil {
			return nil, er
		}
		sb, er := d.ReadBytes()
		if er != nil {
			return nil, er
		}
		copy(r.Secret[:], sb)
		rb, er := d.ReadBytes()
		if er != nil {
			return nil, er
		}
		copy(r.Request[:], rb)
		if r.Relay, er = d.ReadText(); er != nil {
			return nil, er
		}
		if r.State, er = d.ReadText(); er != nil {
			return nil, er
		}
		if r.StartedAt, er = d.ReadUint(); er != nil {
			return nil, er
		}
		if r.CollectUntil, er = d.ReadUint(); er != nil {
			return nil, er
		}
		for i := 8; i < acount; i++ {
			if e := d.SkipItem(); e != nil {
				return nil, e
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func readCopy(d *codec.Decoder) ([]byte, error) {
	b, err := d.ReadBytes()
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), b...), nil
}

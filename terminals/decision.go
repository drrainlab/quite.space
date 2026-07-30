// A host's answer to somebody at the door (QL-2).
//
// This is a SEPARATE sealed envelope, not a variant of Accepted, and the
// separation is load-bearing in two directions.
//
// Downward: OpenAccepted refuses a body with no epoch and no manifest,
// which is what guards epoch delivery. Loosening that check so it could
// also carry refusals would weaken the parser that stands between a
// stranger and a space's keys, to save writing forty lines.
//
// Upward: a distinct HPKE info string means no confused parser can ever
// read a decline as a grant. "They said no" and "you are in" must not be
// two shapes of one message.
//
// A decision carries no keys, no manifest and no capability. It is a
// sentence: somebody decided, and here is what they decided.
package terminals

import (
	"errors"

	"github.com/drrainlab/quiet_places/kernel/crypto"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// DecisionState is what happened to a request.
type DecisionState uint8

const (
	// DecisionReceived: your request arrived and a person has to look at
	// it. Distinct from "waiting" on the guest's side, which only means
	// nothing has come back yet — this one says a human is the next step.
	DecisionReceived DecisionState = 1
	// DecisionDeclined: somebody decided not to let you in. It must never
	// be renderable as an error or as a timeout.
	DecisionDeclined DecisionState = 2
	// DecisionUnavailable: nobody refused you — the door closed first (the
	// pass expired, or the link was withdrawn).
	DecisionUnavailable DecisionState = 3
)

// Decision is the host's sealed answer.
type Decision struct {
	RequestID [32]byte
	State     DecisionState
	// Reason is shown to the guest verbatim. It explains the state; it
	// never carries blame the host did not write.
	Reason string
}

func decisionInfo(space id.TerminalID, reqID [32]byte) []byte {
	out := append([]byte("qs.pass.decision.v1"), space[:]...)
	return append(out, reqID[:]...)
}

// BuildDecision seals an answer to the requesting device.
func BuildDecision(space id.TerminalID, deviceXpub [32]byte, d *Decision) ([]byte, error) {
	if d.State == 0 {
		return nil, errors.New("terminals: a decision must say what was decided")
	}
	body := codec.AppendMap(nil, 3)
	body = codec.AppendUint(body, 1)
	body = codec.AppendBytes(body, d.RequestID[:])
	body = codec.AppendUint(body, 2)
	body = codec.AppendUint(body, uint64(d.State))
	body = codec.AppendUint(body, 3)
	body = codec.AppendText(body, d.Reason)

	enc, ct, err := crypto.SealTo(deviceXpub, decisionInfo(space, d.RequestID), body)
	if err != nil {
		return nil, err
	}
	out := codec.AppendMap(nil, 2)
	out = codec.AppendUint(out, 1)
	out = codec.AppendBytes(out, enc)
	out = codec.AppendUint(out, 2)
	out = codec.AppendBytes(out, ct)
	return out, nil
}

// OpenDecision opens an answer addressed to this device.
func OpenDecision(space id.TerminalID, reqID [32]byte, xpriv [32]byte, sealed []byte) (*Decision, error) {
	enc, ct, err := splitSealed(sealed)
	if err != nil {
		return nil, err
	}
	plain, err := crypto.OpenFrom(xpriv, decisionInfo(space, reqID), enc, ct)
	if err != nil {
		return nil, errors.New("terminals: cannot open decision")
	}
	d := codec.NewDecoder(plain)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	out := &Decision{}
	for {
		k, ok, er := m.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case 1:
			var v []byte
			v, er = d.ReadBytes()
			if er == nil && len(v) == 32 {
				copy(out.RequestID[:], v)
			}
		case 2:
			var v uint64
			v, er = d.ReadUint()
			out.State = DecisionState(v)
		case 3:
			out.Reason, er = d.ReadText()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if out.State == 0 {
		return nil, errors.New("terminals: decision without a state")
	}
	if out.RequestID != reqID {
		return nil, errors.New("terminals: decision is for another request")
	}
	return out, nil
}

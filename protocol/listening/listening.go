// Package listening is the listening-room protocol (LR-2): one track, a
// PERMANENT host (the instance creator — no transfer in v1), followers, and
// durable play/pause/seek commands with a computed position.
//
// A command is an appdef state event (instance-partitioned). Ordering is
// NEVER wall-clock: the reducer takes the maximum of
// (session_epoch, sequence, event_clock, event_id). "Start session" bumps
// epoch and resets sequence to 1; resume after pause does NOT bump epoch.
// The state of anyone's audio element is never protocol state.
package listening

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/drrainlab/quiet_places/protocol/appdef"
	"github.com/drrainlab/quiet_places/protocol/contract"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

const SchemaCommand = "listening.command.v1"

// AppID of the built-in listening-room application.
const AppID = "qs.listening-room"

// MaxEffectiveAheadMS bounds how far in the future a command may schedule
// itself (checked against wall time at emit and at render — not in the
// structural validator, which must stay deterministic).
const MaxEffectiveAheadMS = 30_000

// Command is the JSON carried in the state event's data.
type Command struct {
	Action        string `json:"action"` // play | pause | seek
	PositionMS    uint64 `json:"position_ms"`
	EffectiveAtMS uint64 `json:"effective_at_ms"`
	SessionEpoch  uint64 `json:"session_epoch"`
	Sequence      uint64 `json:"sequence"`
}

// Validate structurally checks a command (deterministic — no clocks here).
func (c *Command) Validate() error {
	switch c.Action {
	case "play", "pause", "seek":
	default:
		return fmt.Errorf("listening: bad action %q", c.Action)
	}
	if c.SessionEpoch == 0 || c.Sequence == 0 {
		return errors.New("listening: session_epoch and sequence start at 1")
	}
	return nil
}

// Encode wraps a command into the state-event payload.
func Encode(instanceID [16]byte, c *Command) ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	ev := &appdef.StateEvent{
		Fallback:   Fallback(c),
		InstanceID: instanceID,
		Data:       data,
	}
	return ev.Encode(), nil
}

// Decode parses a state-event payload into its command.
func Decode(payload []byte) (*appdef.StateEvent, *Command, error) {
	ev, err := appdef.DecodeStateEvent(payload)
	if err != nil {
		return nil, nil, err
	}
	var c Command
	if err := json.Unmarshal(ev.Data, &c); err != nil {
		return nil, nil, errors.New("listening: command data malformed")
	}
	if err := c.Validate(); err != nil {
		return nil, nil, err
	}
	return ev, &c, nil
}

// Fallback is the mandatory text rendering of a command.
func Fallback(c *Command) string {
	pos := c.PositionMS / 1000
	return fmt.Sprintf("listening: %s @ %d:%02d", c.Action, pos/60, pos%60)
}

// Later reports whether command A supersedes command B in the total order
// (session_epoch, sequence, event_clock, event_id).
func Later(aEpoch, aSeq, aClock uint64, aID [32]byte,
	bEpoch, bSeq, bClock uint64, bID [32]byte) bool {
	if aEpoch != bEpoch {
		return aEpoch > bEpoch
	}
	if aSeq != bSeq {
		return aSeq > bSeq
	}
	if aClock != bClock {
		return aClock > bClock
	}
	return string(aID[:]) > string(bID[:])
}

// ---- contract (LR-0a registry) ----

type commandContract struct{}

func (commandContract) SchemaID() string { return SchemaCommand }
func (commandContract) Validate(p []byte) error {
	_, _, err := Decode(p)
	return err
}
func (commandContract) Fallback(p []byte) (string, error) {
	_, c, err := Decode(p)
	if err != nil {
		return "", err
	}
	return Fallback(c), nil
}
func (commandContract) AssetRefs(p []byte) ([]schemas.AssetRef, error) { return nil, nil }

func init() {
	contract.Register(commandContract{}, contract.Descriptor{SchemaID: SchemaCommand})
}

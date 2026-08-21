package node

// QI-1's gate: the reference greenhouse is attached by the owner, its
// readings cross a real relay SEALED TO THE INSTRUMENT PLANE, and every
// member's reducer holds the exact deterministic values — the clock is
// driven by hand, so there is nothing to sleep on and nothing to flake.

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"

	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

func TestTheGreenhouseReachesEveryMember(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()

	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	setPersonalRelay(t, owner, addr)
	owner.applyRelaySync("", 0)
	tid, err := owner.CreateSpace("cabin")
	if err != nil {
		t.Fatal(err)
	}

	guest := openRuntime(t, t.TempDir(), "guest")
	defer guest.Close()
	setPersonalRelay(t, guest, addr)
	guest.applyRelaySync("", 0)
	pass, err := owner.MintPass(tid, 1, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := guest.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, guest, req, JoinReady)

	// The greenhouse, deterministic: seed 1, ticks driven by hand.
	iid, err := owner.AttachSimulatedInstrument(tid, "Greenhouse", 1)
	if err != nil {
		t.Fatal(err)
	}
	owner.mu.Lock()
	ir := owner.instruments[iid]
	owner.mu.Unlock()
	if ir == nil {
		t.Fatal("the attached instrument is not in the runtime")
	}
	// Stop the live ticker — this test owns the clock.
	close(ir.stop)

	at := uint64(time.Now().Unix())
	if err := owner.emitSimTick(ir, 1, at); err != nil {
		t.Fatal(err)
	}

	// The reading's frame is sealed to the INSTRUMENT plane on the wire.
	var sawSealed bool
	_ = owner.withSpace(tid, func(st *spaceState) error {
		return st.space.Log.Replay(func(a eventlog.Applied) error {
			if a.Env.Schema == schemas.ObservationValue {
				if a.Env.PayloadEncoding != signal.PayloadInstrumentSealed {
					t.Fatalf("a reading rode encoding %d, not the instrument seal",
						a.Env.PayloadEncoding)
				}
				sawSealed = true
			}
			return nil
		})
	})
	if !sawSealed {
		t.Fatal("no observation frame found in the owner's log")
	}

	// Across the relay: owner pushes, guest pulls, the guest's reducer
	// holds the exact deterministic values.
	owner.relaySyncOnce(addr)
	if _, err := guest.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	tempDeci, humidDeci, door, light := simValues(1, 1)
	if err := guest.withSpace(tid, func(st *spaceState) error {
		vos := st.space.State.ValueObservations()
		get := func(ch string) (schemas.ValueObservation, bool) {
			for k, v := range vos {
				if k.Instrument == iid && k.Channel == ch {
					return v.Value, true
				}
			}
			return schemas.ValueObservation{}, false
		}
		temp, ok := get("temperature")
		if !ok {
			t.Fatal("the guest never received the temperature")
		}
		if int64(temp.Magnitude) != tempDeci || temp.Decimals != 1 || !temp.Simulated {
			t.Fatalf("temperature diverged from the deterministic driver: %+v want %d", temp, tempDeci)
		}
		hum, _ := get("humidity")
		if int64(hum.Magnitude) != humidDeci {
			t.Fatalf("humidity diverged: %+v want %d", hum, humidDeci)
		}
		d, _ := get("door")
		if d.BoolValue != door {
			t.Fatalf("door diverged: %v want %v", d.BoolValue, door)
		}
		l, _ := get("light")
		if l.Magnitude != light {
			t.Fatalf("light diverged: %d want %d", l.Magnitude, light)
		}
		if st.space.Undecryptable != 0 {
			t.Fatalf("a member could not read the plane: undecryptable=%d", st.space.Undecryptable)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestInstrumentsSurviveARuntimeRestart(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "keeper")
	tid, err := rt.CreateSpace("cabin")
	if err != nil {
		t.Fatal(err)
	}
	iid, err := rt.AttachSimulatedInstrument(tid, "Greenhouse", 7)
	if err != nil {
		t.Fatal(err)
	}
	rt.Close()

	rt2 := openRuntime(t, dir, "keeper")
	defer rt2.Close()
	rt2.mu.Lock()
	ir := rt2.instruments[iid]
	rt2.mu.Unlock()
	if ir == nil {
		t.Fatal("the instrument did not survive the restart")
	}
	close(ir.stop) // this test owns the clock too
	if !ir.rec.Simulated || ir.rec.SimSeed != 7 {
		t.Fatalf("the record lost its driver identity: %+v", ir.rec)
	}
	// And it still speaks: the instrument epoch came back with it.
	if err := rt2.emitSimTick(ir, 2, uint64(time.Now().Unix())); err != nil {
		t.Fatalf("a restarted greenhouse cannot publish: %v", err)
	}
}

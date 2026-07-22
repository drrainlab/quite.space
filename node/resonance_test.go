package node

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/resonance"
)

func semanticRe(key, fb string) resonance.Reaction {
	return resonance.Reaction{Kind: resonance.KindSemantic, Key: key, Fallback: fb}
}

// RP-2 emit gates: unknown target, semantic key outside the palette,
// unicode when forbidden, non-owner palette.
func TestResonanceEmitGates(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Studio")
	if err != nil {
		t.Fatal(err)
	}
	eid, err := rt.Say(tid, "the demo")
	if err != nil {
		t.Fatal(err)
	}

	// Unknown target refused.
	var ghost = eid
	ghost[0] ^= 0xFF
	if err := rt.ResonanceSet(tid, ghost, semanticRe("warmth", "♡")); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("ghost target must be refused, got %v", err)
	}

	// Semantic key outside the active palette refused (weight is core but
	// not in the default 6-slot palette).
	if err := rt.ResonanceSet(tid, eid, semanticRe("weight", "●")); err == nil ||
		!strings.Contains(err.Error(), "palette") {
		t.Fatalf("off-palette key must be refused, got %v", err)
	}

	// In-palette semantic works; replacement works; clear works.
	if err := rt.ResonanceSet(tid, eid, semanticRe("warmth", "♡")); err != nil {
		t.Fatal(err)
	}
	if err := rt.ResonanceSet(tid, eid, semanticRe("spark", "✦")); err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.Space(tid)
	agg := sp.State.ResonanceFor(eid)
	if agg.Total != 1 || agg.Groups[0].Reaction.Key != "spark" {
		t.Fatalf("replacement wrong: %+v", agg)
	}
	if err := rt.ResonanceClear(tid, eid); err != nil {
		t.Fatal(err)
	}
	if agg := sp.State.ResonanceFor(eid); agg.Total != 0 {
		t.Fatalf("clear failed: %+v", agg)
	}

	// Owner forbids unicode → unicode refused, semantic still fine.
	pal := resonance.DefaultPalette()
	pal.Policy.AllowUnicode = false
	if err := rt.SetResonancePalette(tid, &pal); err != nil {
		t.Fatal(err)
	}
	if err := rt.ResonanceSet(tid, eid,
		resonance.Reaction{Kind: resonance.KindUnicode, Value: "🌲"}); err == nil ||
		!strings.Contains(err.Error(), "semantic") {
		t.Fatalf("unicode must be refused by policy, got %v", err)
	}
	if err := rt.ResonanceSet(tid, eid, semanticRe("warmth", "♡")); err != nil {
		t.Fatal(err)
	}
}

// Two runtimes over LAN: reactions and the palette travel; a member's
// forged palette is ignored by the reducer on every replica.
func TestResonanceTwoNodeSync(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	tid, err := alice.CreateSpace("Studio")
	if err != nil {
		t.Fatal(err)
	}
	eid, err := alice.Say(tid, "first listen")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	if err := alice.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if err := bob.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if err := bob.ConnectPeer(fmt.Sprintf("127.0.0.1:%d", alice.LAN().Port)); err != nil {
		t.Fatal(err)
	}
	waitFor := func(desc string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timeout: %s", desc)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	spB, _ := bob.Space(tid)
	waitFor("message sync", func() bool { return len(spB.State.Messages()) >= 1 })

	// Bob reacts; Alice sees the aggregate.
	if err := bob.ResonanceSet(tid, eid, semanticRe("resonates", "〰️")); err != nil {
		t.Fatal(err)
	}
	spA, _ := alice.Space(tid)
	waitFor("reaction sync", func() bool { return spA.State.ResonanceFor(eid).Total == 1 })

	// Alice (owner) publishes a palette; it reaches Bob and changes HIS
	// emit gate.
	pal := resonance.DefaultPalette()
	pal.PaletteID = "studio.v1"
	pal.Policy.AllowUnicode = false
	if err := alice.SetResonancePalette(tid, &pal); err != nil {
		t.Fatal(err)
	}
	waitFor("palette sync", func() bool {
		p, own := spB.State.ActivePalette()
		return own && p.PaletteID == "studio.v1"
	})
	if err := bob.ResonanceSet(tid, eid,
		resonance.Reaction{Kind: resonance.KindUnicode, Value: "🔥"}); err == nil {
		t.Fatal("bob's unicode must be refused after the owner's policy arrived")
	}

	// Bob (not owner) cannot publish a palette through the API...
	forged := resonance.DefaultPalette()
	forged.PaletteID = "evil.v1"
	if err := bob.SetResonancePalette(tid, &forged); err == nil {
		t.Fatal("non-owner palette must be refused")
	}
	// ...and even a directly emitted forged palette event is ignored by the
	// reducer on Alice's replica (signature ≠ controller).
	payload, err := forged.Encode()
	if err != nil {
		t.Fatal(err)
	}
	bob.mu.Lock()
	stB := bob.spaces[tid]
	_, err = bob.Self.Emit(stB.space, resonance.SchemaPalette, payload,
		bob.Self.DefaultAuthorship(), uint64(time.Now().Unix()))
	bob.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	waitFor("forged palette counted on alice", func() bool {
		spA.State.Digest() // touch nothing; just poll
		return spA.State.Unsupported["resonance:unauthorized_palette"] >= 1
	})
	if p, _ := spA.State.ActivePalette(); p.PaletteID != "studio.v1" {
		t.Fatalf("forged palette must not fold: %q", p.PaletteID)
	}
}

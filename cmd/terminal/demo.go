package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/agent"
	"github.com/drrainlab/quiet_places/terminals/archive"
	"github.com/drrainlab/quiet_places/terminals/bot"
	"github.com/drrainlab/quiet_places/terminals/human"
	"github.com/drrainlab/quiet_places/terminals/sensor"
	"github.com/drrainlab/quiet_places/transports/bundle"
	"github.com/drrainlab/quiet_places/transports/loopback"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// runDemo is the Phase 0 proof, end to end, with no network and no server:
//
//  1. Node A: Alice (human) creates the "Forest Session" space; a
//     source-only sensor publishes a simulated temperature.
//  2. Node B joins as a replica: echo bot, AI-agent stub, sink-only logger.
//  3. Both nodes work offline, then sync over a local loopback link.
//  4. A third, blind relay node carries a bundle between nodes as opaque
//     bytes and holds it until the recipient collects it.
//  5. Every honesty projection is printed: authorship, freshness, presence.
func runDemo() error {
	now := uint64(1_753_142_400)
	say := func(f string, a ...any) { fmt.Printf(f+"\n", a...) }

	say("— Terminal Network M0 demo: no server, no accounts, two nodes —")

	// Node A.
	alice, err := human.New("alice")
	if err != nil {
		return err
	}
	spaceA, err := terminals.NewSpace("Forest Session", alice.Principal.ID)
	if err != nil {
		return err
	}
	spaceA.EnablePrivate(alice.Device)
	spaceA.AddMember(alice.Device.ID, alice.Device.X25519Pub)
	if _, err := alice.RotateEpoch(spaceA); err != nil {
		return err
	}
	say("\n[node A] alice created PRIVATE space %s (%q), epoch 1 minted", spaceA.ID, "Forest Session")
	say("[node A] alice fingerprint: %s", alice.Principal.Fingerprint())

	climate, err := sensor.NewTemperature("studio climate")
	if err != nil {
		return err
	}
	if _, err := sensor.Publish(climate, spaceA, 2360, false, true, now); err != nil {
		return err
	}
	say("[node A] sensor published 23.60°C (declared simulated=true, stale_after=600s)")
	if _, err := human.Say(alice, spaceA, "recording field ambience tonight", human.SayOptions{}, now+10); err != nil {
		return err
	}
	if err := human.SetPresence(alice, spaceA, "listening", now+10, 300); err != nil {
		return err
	}
	say("[node A] alice wrote a message and set presence 'listening' (TTL 300s)")

	// Node B joins via a signed capability invite (carries wrapped epoch
	// keys for exactly one device — nothing here goes through a server).
	echo, err := bot.NewEcho()
	if err != nil {
		return err
	}
	summarizer, err := agent.NewSummarizer()
	if err != nil {
		return err
	}
	logger, err := archive.NewLogger()
	if err != nil {
		return err
	}
	invite, err := spaceA.NewInvite(echo.Device.ID, echo.Device.X25519Pub)
	if err != nil {
		return err
	}
	spaceB, err := terminals.AcceptInvite(invite, echo.Device)
	if err != nil {
		return err
	}
	spaceA.AddMember(echo.Device.ID, echo.Device.X25519Pub)
	if _, err := alice.RotateEpoch(spaceA); err != nil {
		return err
	}
	say("\n[node B] joined via signed invite (%d bytes) — echo-bot, AI-agent stub, sink-only logger; epoch 2 minted", len(invite))

	// Direct sync over a local link (both directions).
	engA := kernelsync.NewEngine(spaceA.Log)
	engA.OnApplied = spaceA.AttachSyncApply
	engB := kernelsync.NewEngine(spaceB.Log)
	engB.OnApplied = func(a eventlog.Applied) {
		spaceB.AttachSyncApply(a)
		logger.Record(a)
		bot.Applied(echo, a)
	}
	pair := loopback.NewPair(loopback.Faults{Seed: 1})
	for range 4 {
		if err := engA.SendSummary(pair.A); err != nil {
			return err
		}
		if err := engB.SendSummary(pair.B); err != nil {
			return err
		}
		if _, _, err := engA.Pump(pair.A); err != nil {
			return err
		}
		if _, _, err := engB.Pump(pair.B); err != nil {
			return err
		}
	}
	say("[sync] loopback link: node B now holds %d events", spaceB.Log.Len())

	// Node B reacts: bot echoes, agent summarizes; both marked honestly.
	answered := map[id.EventID]bool{}
	if _, err := bot.React(echo, spaceB, answered, now+20); err != nil {
		return err
	}
	if _, err := agent.Summarize(summarizer, spaceB, now+30); err != nil {
		return err
	}
	say("[node B] bot echoed; agent summarized (marked ai_agent, human_approved=false)")

	// Carry node B's new events back via a blind relay + bundle file.
	dir, err := os.MkdirTemp("", "qp-demo-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	var frames [][]byte
	if err := spaceB.Log.Replay(func(a eventlog.Applied) error {
		frames = append(frames, a.Frame)
		return nil
	}); err != nil {
		return err
	}
	bundlePath := filepath.Join(dir, "b-to-a.terminal-bundle")
	if err := bundle.Write(bundlePath, spaceB.ID, frames); err != nil {
		return err
	}
	raw, err := os.ReadFile(bundlePath)
	if err != nil {
		return err
	}
	courier := relayserver.NewStore(64, 1<<20)
	courier.Put(relayserver.Item{DestinationHint: "rotating-hint-7f3a", ExpiresAt: now + 86400, Ciphertext: raw})
	say("\n[relay] blind courier holds %d bytes for hint %q", len(raw), "rotating-hint-7f3a")

	// Courier-eye view: a keyless replica of the same space can verify and
	// store every frame, and read none of them.
	courierEye := terminals.Replica(spaceB.ID)
	_, eyeFrames, err := bundle.Read(bundlePath)
	if err != nil {
		return err
	}
	for _, f := range eyeFrames {
		if _, err := courierEye.Absorb(f); err != nil {
			return err
		}
	}
	say("[relay] courier-eye check: %d frames verified, %d messages readable, %d undecryptable",
		courierEye.Log.Len(), len(courierEye.State.Messages()), courierEye.Undecryptable)
	say("[relay] honesty note: payloads are epoch-encrypted (ADR-005); envelope headers (author, schema, sequence) remain visible metadata")

	delivered := courier.Collect("rotating-hint-7f3a", now+3600)
	deliveredPath := filepath.Join(dir, "delivered.terminal-bundle")
	if err := os.WriteFile(deliveredPath, delivered[0], 0o600); err != nil {
		return err
	}
	_, gotFrames, err := bundle.Read(deliveredPath)
	if err != nil {
		return err
	}
	for _, f := range gotFrames {
		if _, err := spaceA.Absorb(f); err != nil && err != eventlog.ErrWrongTerminal {
			return err
		}
	}
	say("[node A] imported courier bundle — %d events total", spaceA.Log.Len())

	// Final state + honesty projections.
	say("\n— materialized state on node A —")
	for _, m := range spaceA.State.Messages() {
		say("  [%s] %s", m.ProducedBy, m.Text)
	}
	if o, ok := spaceA.State.LatestObservation(); ok {
		flag := "measured"
		if o.Value.Simulated {
			flag = "SIMULATED"
		}
		say("  sensor: %d.%02d°C (%s), observed at t=%d", o.Value.CentiValue/100, o.Value.CentiValue%100, flag, o.ObservedAt)
	}
	pres := spaceA.Trust.Presence(alice.TerminalID, now+1000)
	if pres.Known && !pres.Current {
		say("  presence: alice last known %q, %d seconds ago — NOT shown as online", pres.State, pres.AgeSeconds)
	}
	digestA, digestB := spaceA.State.Digest(), spaceB.State.Digest()
	say("\n[proof] node digests equal: %v (A=%x… B=%x…)", digestA == digestB, digestA[:4], digestB[:4])
	if digestA != digestB {
		return fmt.Errorf("demo failed: nodes diverged")
	}
	say("[proof] signed events, offline work, file transport, local link, blind courier — no central anything")
	return nil
}

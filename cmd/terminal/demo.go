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
	"github.com/drrainlab/quiet_places/transports/relay"
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
	say("\n[node A] alice created space %s (%q)", spaceA.ID, "Forest Session")
	say("[node A] alice fingerprint: %s", alice.Principal.Fingerprint())

	climate, err := sensor.NewTemperature("studio climate")
	if err != nil {
		return err
	}
	if _, err := sensor.Publish(climate, spaceA, 2360, false, true, now); err != nil {
		return err
	}
	say("[node A] sensor published 23.60°C (declared simulated=true, stale_after=600s)")
	if _, err := human.Say(alice, spaceA, "recording field ambience tonight", now+10); err != nil {
		return err
	}
	if err := human.SetPresence(alice, spaceA, "listening", now+10, 300); err != nil {
		return err
	}
	say("[node A] alice wrote a message and set presence 'listening' (TTL 300s)")

	// Node B: independent replica of the same space.
	spaceB := terminals.Replica(spaceA.ID)
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
	say("\n[node B] replica of the space with echo-bot, AI-agent stub, sink-only logger")

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
	courier := relay.NewStore(64, 1<<20)
	courier.Put(relay.Item{DestinationHint: "rotating-hint-7f3a", ExpiresAt: now + 86400, Ciphertext: raw})
	say("\n[relay] blind courier holds %d bytes for hint %q — it has no parser for them", len(raw), "rotating-hint-7f3a")
	say("[relay] honesty note: payload encryption lands in M1; today blindness is architectural, not yet cryptographic")

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

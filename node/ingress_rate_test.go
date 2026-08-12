package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/bundle"

	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relay"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// IC-1: an owner may tighten the per-contributor rate, and the promise the
// built-in caps already make holds for their limit too — nothing is lost.
//
// This is the whole reason the limit is a DEFERRAL and not a rejection: a
// space that quietly drops what it will not take this second is
// indistinguishable, from the contributor's side, from one that censors.
func TestAnOwnerLimitDefersRatherThanDrops(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	tid, err := owner.CreateSpaceWithOptions("Commons", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic, Join: terminals.JoinOpen,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	const limit = terminals.MinFramesPerAuthor
	if err := owner.RevisePolicy(tid, PolicyDelta{
		MaxFramesPerAuthor: ptr(limit),
	}); err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}

	loud := openRuntime(t, t.TempDir(), "loud")
	defer loud.Close()
	quiet := openRuntime(t, t.TempDir(), "quiet")
	defer quiet.Close()
	for _, c := range []*Runtime{loud, quiet} {
		if err := c.OpenPublicSpace(tid, addr); err != nil {
			t.Fatal(err)
		}
		if err := c.JoinPublicSpace(tid); err != nil {
			t.Fatal(err)
		}
	}

	const flood = limit * 3
	for i := 0; i < flood; i++ {
		if _, err := loud.Say(tid, fmt.Sprintf("flood %d", i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := quiet.Say(tid, "one calm word", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*Runtime{loud, quiet} {
		if err := c.pushPublicIngress(addr, tid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.collectPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}

	osp, _ := owner.spaceForTest(tid)
	got := countFlood(osp.State.Messages())
	if got > limit {
		t.Fatalf("first drain took %d frames from one author — the owner's "+
			"limit of %d did not bind", got, limit)
	}
	if got == 0 {
		t.Fatal("the owner's limit refused everything, which is a mute, not a limit")
	}
	// The limit is PER AUTHOR: a loud contributor must not spend anybody
	// else's share.
	if !hasMessage(osp.State.Messages(), "one calm word") {
		t.Fatal("the quiet contributor was caught by another author's limit")
	}
	// And the owner can see it working, in its own word.
	var throttled uint64
	_ = owner.withSpace(tid, func(st *spaceState) error {
		throttled = st.throttled
		return nil
	})
	if throttled == 0 {
		t.Fatal("nothing was reported as held back, so the owner has no way " +
			"to tell the limit from a quiet week")
	}
	if osp.PolicyStats.IgnoredTotal != 0 {
		t.Fatalf("a deferred frame was counted as ignored (%d) — it is coming "+
			"back, and counting it every cycle would flood the real refusals",
			osp.PolicyStats.IgnoredTotal)
	}

	// Nothing is lost: every flooded message lands on a later cycle.
	for cycle := 0; cycle < 24; cycle++ {
		if err := owner.publishPublicProjection(addr, tid); err != nil {
			t.Fatal(err)
		}
		if err := loud.fetchPublicProjection(addr, tid); err != nil {
			t.Fatal(err)
		}
		if err := loud.pushPublicIngress(addr, tid); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.collectPublicIngress(addr, tid); err != nil {
			t.Fatal(err)
		}
		osp, _ = owner.spaceForTest(tid)
		if countFlood(osp.State.Messages()) == flood {
			return
		}
	}
	osp, _ = owner.spaceForTest(tid)
	t.Fatalf("a rate-limited contributor lost content: %d/%d after convergence",
		countFlood(osp.State.Messages()), flood)
}

// The attack the charge point exists to stop, run end to end.
//
// The per-author key is the CLAIMED signer device, read before any signature
// is checked. So anyone who can see a contributor's frames — they travel a
// public space's ingress mailbox — can push them back. Under a limit charged
// on the claim, replaying somebody's own words would spend their allowance
// and mute them with their own content.
func TestReplayingAContributorsFramesDoesNotSpendTheirAllowance(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	tid, err := owner.CreateSpaceWithOptions("Commons", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic, Join: terminals.JoinOpen,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	const limit = terminals.MinFramesPerAuthor
	if err := owner.RevisePolicy(tid, PolicyDelta{MaxFramesPerAuthor: ptr(limit)}); err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}

	victim := openRuntime(t, t.TempDir(), "victim")
	defer victim.Close()
	attacker := openRuntime(t, t.TempDir(), "attacker")
	defer attacker.Close()
	for _, c := range []*Runtime{victim, attacker} {
		if err := c.OpenPublicSpace(tid, addr); err != nil {
			t.Fatal(err)
		}
		if err := c.JoinPublicSpace(tid); err != nil {
			t.Fatal(err)
		}
	}

	// The victim says one thing, and it lands.
	if _, err := victim.Say(tid, "the first word", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	var stolen [][]byte
	_ = victim.withSpace(tid, func(st *spaceState) error {
		stolen = append(stolen, st.space.UnackedLocalFrames()...)
		return nil
	})
	if len(stolen) == 0 {
		t.Fatal("nothing to replay")
	}
	if err := victim.pushPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.collectPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}

	// The attacker pushes the victim's own frames back, over and over, into
	// its own shard — which the owner drains just the same.
	var replay [][]byte
	for i := 0; i < limit*4; i++ {
		replay = append(replay, stolen[0])
	}
	pushRawIngress(t, attacker, addr, tid, replay)
	if _, err := owner.collectPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}

	// ...and the victim can still speak their full share in the same cycle.
	for i := 0; i < limit; i++ {
		if _, err := victim.Say(tid, fmt.Sprintf("word %d", i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := victim.pushPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.collectPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}
	osp, _ := owner.spaceForTest(tid)
	for i := 0; i < limit; i++ {
		if !hasMessage(osp.State.Messages(), fmt.Sprintf("word %d", i)) {
			t.Fatalf("the victim was silenced by a replay of their own words: "+
				"%q never arrived", fmt.Sprintf("word %d", i))
		}
	}
}

// pushRawIngress puts an arbitrary frame list into a space's ingress mailbox
// as `from` — the shape of a contribution, with none of the honesty.
func pushRawIngress(t *testing.T, from *Runtime, addr string, tid id.TerminalID, frames [][]byte) {
	t.Helper()
	self := from.Device.ID
	var hint []byte
	from.mu.Lock()
	if st, ok := from.spaces[tid]; ok {
		hint = from.ingressHintLocked(st, tid, self, relay.Bucket(uint64(time.Now().Unix())))
	}
	from.mu.Unlock()
	if hint == nil {
		t.Fatal("no ingress address")
	}
	body := bundle.EncodeWithReplyBox(tid, frames, nil, nil, self[:], nil)
	err := from.withRelayControl(addr, func(client *relay.Client) error {
		_, err := client.Put(hint, uint64(time.Now().Unix())+3600, body)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The limit is signed policy, so it travels and it is refused when it makes
// no sense — checked through the owner-facing route rather than the struct.
func TestRevisingTheLimitIsBoundedAndOwnerOnly(t *testing.T) {
	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	tid, err := owner.CreateSpaceWithOptions("Commons", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic, Join: terminals.JoinOpen,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.RevisePolicy(tid, PolicyDelta{MaxFramesPerAuthor: ptr(1)}); err == nil {
		t.Fatal("a limit below the floor was accepted")
	}
	if err := owner.RevisePolicy(tid, PolicyDelta{MaxFramesPerAuthor: ptr(9999)}); err == nil {
		t.Fatal("a limit above the defence cap was accepted")
	}
	if err := owner.RevisePolicy(tid, PolicyDelta{MaxFramesPerAuthor: ptr(16)}); err != nil {
		t.Fatal(err)
	}
	osp, _ := owner.spaceForTest(tid)
	if got := osp.Policy().MaxFramesPerAuthor; got != 16 {
		t.Fatalf("the limit did not reach the signed manifest: %d", got)
	}

	// Switching to broadcast clears it, or flipping back to community would
	// silently resurrect a number the owner has long forgotten.
	if err := owner.RevisePolicy(tid, PolicyDelta{Publish: ptr("curated")}); err != nil {
		t.Fatal(err)
	}
	osp, _ = owner.spaceForTest(tid)
	if got := osp.Policy().MaxFramesPerAuthor; got != 0 {
		t.Fatalf("the limit survived the switch to broadcast: %d", got)
	}
}

func ptr[T any](v T) *T { return &v }

package node

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// RR — the soaks. Fifty spaces, pulled at a person's cadence, for long enough
// that a slow leak has room to show.
//
// WHY A SOAK AND NOT ANOTHER UNIT TEST. The chunking bug was not a crash. The
// node kept running, the relay stayed reachable, and messages stopped arriving
// — and every single-shot test passed, because one pull across fifty spaces
// does work. What does not work is the FIFTIETH minute: a cursor that rotates
// one chunk short, a wait that is re-armed a little early every cycle, a
// backlog that grows by one round every ten. None of those are visible in a
// second, and all of them look exactly like a flaky connection to the person
// holding the phone.
//
// THEY ARE GATED, AND COMPILED ANYWAY. `QUIET_RELAY_SOAK=1` runs them; without
// it they skip in microseconds. The gate is an environment variable rather
// than a build tag on purpose — a tagged file stops being compiled, stops
// being vetted, and quietly rots until the day somebody needs it. Skipping is
// cheap; bit rot is not.
//
//	QUIET_RELAY_SOAK=1 go test ./node/ -run Soak -v -timeout 30m
//	QUIET_RELAY_SOAK=1 QUIET_RELAY_SOAK_SECONDS=90 go test ./node/ -run Soak -v
const (
	soakSpaces  = 50
	soakCadence = 2 * time.Second
)

func soakDuration(t *testing.T) time.Duration {
	t.Helper()
	if os.Getenv("QUIET_RELAY_SOAK") != "1" {
		t.Skip("set QUIET_RELAY_SOAK=1 to run the soaks (they take minutes)")
	}
	if s := os.Getenv("QUIET_RELAY_SOAK_SECONDS"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			t.Fatalf("QUIET_RELAY_SOAK_SECONDS=%q is not a number of seconds", s)
		}
		return time.Duration(n) * time.Second
	}
	return 10 * time.Minute
}

// A soak fixture: fifty spaces at one node, three of them — the first, the
// middle and the last — joined by another. Those three are the ones that
// answer the only question a chunker can fail: not "did anything arrive" but
// "did the far end of the list arrive too".
type soakFixture struct {
	addr    string
	alice   *Runtime
	bob     *Runtime
	tids    []id.TerminalID
	watched []id.TerminalID // first, middle and last IN THE ORDER THAT IS CHUNKED
}

func newSoakFixture(t *testing.T, limits relay.ServerLimits) *soakFixture {
	t.Helper()
	srv, port, err := relay.StartServer("127.0.0.1:0", limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	addr := "127.0.0.1:" + itoa(port)

	alice := openRuntime(t, t.TempDir(), "alice")
	t.Cleanup(func() { alice.Close() })
	bob := openRuntime(t, t.TempDir(), "bob")
	t.Cleanup(func() { bob.Close() })
	for _, rt := range []*Runtime{alice, bob} {
		if err := rt.SetSettings(Settings{Relay: addr}); err != nil {
			t.Fatal(err)
		}
	}

	tids := make([]id.TerminalID, 0, soakSpaces)
	for i := 0; i < soakSpaces; i++ {
		tid, err := alice.CreateSpace(fmt.Sprintf("room %d", i))
		if err != nil {
			t.Fatal(err)
		}
		tids = append(tids, tid)
	}

	// BOB JOINS ALL FIFTY, and that is the whole fixture rather than a detail.
	// The first version had him join only the three spaces the messages go to
	// and it tested nothing: the capability ceiling is on the side that
	// DRAINS, and a node with three spaces sends six hints. Fifty spaces at
	// the sender cost the receiver nothing. Both red-proofs passed against a
	// deliberately broken chunker before this was found — the soak was
	// measuring a node that never had a second chunk to serve.
	for i := range tids {
		info, err := alice.MintPass(tids[i], 1, 24, addr)
		if err != nil {
			t.Fatal(err)
		}
		reqID, err := bob.JoinByPass(info.Link)
		if err != nil {
			t.Fatal(err)
		}
		waitJoin(t, bob, reqID, JoinReady)
	}
	// And the fixture says so out loud: if a pull fits in one chunk, every
	// assertion below is about something else.
	order := bob.relayMailboxSpaces()
	if len(order) < soakSpaces {
		t.Fatalf("bob holds %d of %d spaces — the pull would not be chunked",
			len(order), soakSpaces)
	}

	// THE WATCHED SPACES COME FROM THAT ORDER, not from the order they were
	// created in. Capabilities are sorted by terminal id, so creation index 49
	// sits wherever its id falls — and a "last space" that happens to land in
	// the first chunk makes the far end of the list a coin toss. Both
	// red-proofs passed against a chunker pinned to the head of the list
	// before this was fixed.
	watched := []id.TerminalID{order[0], order[len(order)/2], order[len(order)-1]}

	return &soakFixture{addr: addr, alice: alice, bob: bob, tids: tids, watched: watched}
}

// How many of the rounds' messages bob actually holds.
//
// MESSAGES, NOT LOG LENGTH. The first version subtracted a baseline taken
// right after joining and reported more messages than were ever sent: the
// membership events of the LAST space joined were still replicating when the
// baseline was read, so the space that mattered most — the far end of the
// list — was the one measured wrong. A count that only ever counts what the
// test itself sent cannot drift like that.
func (f *soakFixture) delivered(tid id.TerminalID) int {
	sp, ok := f.bob.spaceForTest(tid)
	if !ok {
		return 0
	}
	n := 0
	_ = sp.Log.Replay(func(a eventlog.Applied) error {
		if a.Env != nil && a.Env.Schema == schemas.MessageText {
			n++
		}
		return nil
	})
	return n
}

// One round: a line into each watched space, pushed by the sender.
func (f *soakFixture) send(t *testing.T, round int) {
	t.Helper()
	for _, tid := range f.watched {
		if _, err := f.alice.Say(tid, fmt.Sprintf("round %d", round), SayOptions{}); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if _, _, err := f.alice.PushToRelay(f.addr, tid); err != nil {
			t.Fatalf("round %d: push: %v", round, err)
		}
	}
}

// Pull until everything sent has arrived or the deadline passes. Returns the
// worst backlog seen, in rounds.
func (f *soakFixture) drain(t *testing.T, sent int, deadline time.Time) int {
	t.Helper()
	worst := 0
	for {
		behind := 0
		for _, tid := range f.watched {
			if b := sent - f.delivered(tid); b > behind {
				behind = b
			}
		}
		if behind > worst {
			worst = behind
		}
		if behind <= 0 || !time.Now().Before(deadline) {
			return worst
		}
		if _, err := f.bob.PullFromRelay(f.addr); err != nil {
			var re relay.ErrRelay
			if errors.As(err, &re) && re.Throttled() {
				// Waiting is the correct behaviour, not a failure. The
				// clean soak asserts separately that it never got here.
				time.Sleep(time.Second)
				continue
			}
			t.Fatalf("draining failed: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// A person with fifty spaces, syncing every two seconds for ten minutes.
//
// THE THING THIS CATCHES that a single pull cannot: falling behind. Every
// round is one message into the first space, the middle one and the last, and
// every round the backlog is measured. A chunker that serves fifty spaces in
// one pass keeps the backlog at zero. One that serves forty-eight of them
// looks fine for a minute and is two hundred messages behind by the end —
// and the two are indistinguishable at t=1s.
func TestSoakFiftySpacesStayCurrentUnderASustainedPull(t *testing.T) {
	run := soakDuration(t)
	f := newSoakFixture(t, relay.DefaultLimits())

	end := time.Now().Add(run)
	report := time.Now().Add(30 * time.Second)
	rounds, worst, refusals := 0, 0, 0

	for time.Now().Before(end) {
		rounds++
		f.send(t, rounds)

		if _, err := f.bob.PullFromRelay(f.addr); err != nil {
			refusals++
			t.Errorf("round %d was refused, and at a person's cadence nothing "+
				"should be: %v", rounds, err)
		}

		behind := 0
		for _, tid := range f.watched {
			if b := rounds - f.delivered(tid); b > behind {
				behind = b
			}
		}
		if behind > worst {
			worst = behind
		}
		if time.Now().After(report) {
			t.Logf("%d rounds, worst backlog %d, refusals %d", rounds, worst, refusals)
			report = time.Now().Add(30 * time.Second)
		}
		time.Sleep(soakCadence)
	}

	// A last drain: the final round's push and pull race by design.
	f.drain(t, rounds, time.Now().Add(30*time.Second))

	for at, tid := range f.watched {
		if got := f.delivered(tid); got != rounds {
			t.Errorf("watched space %d of 3 received %d of %d rounds — the "+
				"first, the middle and the LAST of the capability list must "+
				"all keep up, or the tail is being dropped", at+1, got, rounds)
		}
	}
	if refusals > 0 {
		t.Errorf("%d refusals in %d rounds: a two-second cadence across %d "+
			"spaces is ordinary use, not abuse", refusals, rounds, soakSpaces)
	}
	// Two rounds of slack for the in-flight push; a trend shows up as tens.
	if worst > 2 {
		t.Errorf("backlog reached %d rounds — steady state is zero, and "+
			"anything that grows is a chunker that never quite catches up", worst)
	}
	t.Logf("clean: %d rounds over %v, worst backlog %d", rounds, run, worst)
}

// The same fifty spaces against a relay that WILL refuse, because the pull
// cadence is deliberately above its limit.
//
// WHAT MUST BE TRUE AFTERWARDS is not "no refusals" — refusals are the point
// — but that a refusal is a pause rather than an ending: the node waits the
// window out, comes back, resumes at the chunk that was refused, and loses
// nothing. Collect is destructive, so "loses nothing" is load-bearing: a
// refusal must happen BEFORE anything is handed over, or the messages in that
// chunk exist nowhere at all.
func TestSoakFiftySpacesRecoverFromRepeatedThrottling(t *testing.T) {
	run := soakDuration(t)
	limits := relay.DefaultLimits()
	// Well under one pull per two seconds, so the window runs out mid-run and
	// keeps running out.
	limits.CollectRatePerMin = 10
	f := newSoakFixture(t, limits)

	end := time.Now().Add(run)
	report := time.Now().Add(30 * time.Second)
	rounds, refusals, recoveries := 0, 0, 0
	wasThrottled := false

	for time.Now().Before(end) {
		rounds++
		f.send(t, rounds)

		_, err := f.bob.PullFromRelay(f.addr)
		switch {
		case err == nil:
			if wasThrottled {
				recoveries++
				wasThrottled = false
			}
		default:
			var re relay.ErrRelay
			if !errors.As(err, &re) || !re.Throttled() {
				t.Fatalf("round %d failed for something other than a rate "+
					"limit: %v", rounds, err)
			}
			refusals++
			wasThrottled = true
		}

		if time.Now().After(report) {
			t.Logf("%d rounds, %d refusals, %d recoveries", rounds, refusals, recoveries)
			report = time.Now().Add(30 * time.Second)
		}
		time.Sleep(soakCadence)
	}

	if refusals == 0 {
		t.Fatal("nothing was throttled, so nothing about recovery was tested — " +
			"the cadence or the limit is wrong")
	}
	if recoveries == 0 {
		t.Fatal("the node was refused and never came back: a rate limit became " +
			"a permanent disconnection, which is the failure this exists to catch")
	}

	// Everything sent must still arrive. Two minutes of slack: a wait can be
	// the remainder of a minute, and the tail may need more than one window.
	f.drain(t, rounds, time.Now().Add(2*time.Minute))
	for at, tid := range f.watched {
		if got := f.delivered(tid); got != rounds {
			t.Errorf("watched space %d of 3 received %d of %d rounds after "+
				"recovery — a throttle must cost time, never messages",
				at+1, got, rounds)
		}
	}
	t.Logf("recovery: %d rounds, %d refusals, %d recoveries, nothing lost",
		rounds, refusals, recoveries)
}

// The BACKGROUND sync, left to its own rhythm — which is the one a person
// actually uses.
//
// WRITTEN BECAUSE THE PHONE GATE KEPT FAILING IN A MOVING PLACE. Whichever
// check happened to run three minutes in reported "no notification arrived",
// the relay's item count stopped growing, and both nodes reported themselves
// healthy with nothing held. An earlier test drove PushToRelay and
// PullFromRelay by hand and passed — which proves the two calls work and says
// nothing about the loop that is supposed to make them, and the loop is where
// RR-2's tiering decides that a quiet space may wait.
//
// So this one says nothing to either node: it sets the relay, waits, speaks,
// and measures how long a message takes to arrive.
func TestSoakBackgroundSyncKeepsDeliveringAfterAQuietSpell(t *testing.T) {
	if os.Getenv("QUIET_RELAY_SOAK") != "1" {
		t.Skip("set QUIET_RELAY_SOAK=1 (this one waits out a quiet window)")
	}
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := "127.0.0.1:" + itoa(port)

	sender := openRuntime(t, t.TempDir(), "alice")
	defer sender.Close()
	phone := openRuntime(t, t.TempDir(), "bob")
	defer phone.Close()

	var rooms []id.TerminalID
	for _, title := range []string{"room A", "room B"} {
		tid, err := sender.CreateSpace(title)
		if err != nil {
			t.Fatal(err)
		}
		rooms = append(rooms, tid)
	}
	// Settings LAST, so both sides start their loops with the spaces in hand.
	for _, rt := range []*Runtime{sender, phone} {
		if err := rt.SetSettings(Settings{Relay: addr}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tid := range rooms {
		info, err := sender.MintPass(tid, 1, 24, addr)
		if err != nil {
			t.Fatal(err)
		}
		reqID, err := phone.JoinByPass(info.Link)
		if err != nil {
			t.Fatal(err)
		}
		waitJoin(t, phone, reqID, JoinReady)
	}

	// arrival waits for a message WITHOUT touching either node's sync.
	arrival := func(tid id.TerminalID, text string, limit time.Duration) time.Duration {
		start := time.Now()
		if _, err := sender.Say(tid, text, SayOptions{}); err != nil {
			t.Fatal(err)
		}
		for time.Since(start) < limit {
			if sp, ok := phone.spaceForTest(tid); ok {
				found := false
				_ = sp.Log.Replay(func(a eventlog.Applied) error {
					if a.Env != nil && a.Env.Schema == schemas.MessageText {
						if e, ok := sp.State.EntryByID(a.ID); ok &&
							e.Content.Text != nil && e.Content.Text.Text == text {
							found = true
						}
					}
					return nil
				})
				if found {
					return time.Since(start)
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		return -1
	}

	// Warm both, the way anybody's first minute looks.
	for i, tid := range rooms {
		if d := arrival(tid, fmt.Sprintf("warm %d", i), 90*time.Second); d < 0 {
			t.Fatalf("room %d never received its first message", i)
		} else {
			t.Logf("room %d warm in %v", i, d)
		}
	}

	// THE QUIET SPELL. Longer than RR-2's quietAfter, which is the window
	// this test exists to walk into.
	t.Log("going quiet for 90s")
	time.Sleep(90 * time.Second)

	for i, tid := range rooms {
		d := arrival(tid, fmt.Sprintf("after the quiet %d", i), 2*time.Minute)
		if d < 0 {
			t.Fatalf("room %d: a message after a quiet spell never arrived — "+
				"this is the phone's \"the conversation went silent\"", i)
		}
		t.Logf("room %d woke in %v", i, d)
		// A number, not only a pass: a room that takes half a minute to wake
		// is not broken and is not what anybody expects either.
		if d > 30*time.Second {
			t.Errorf("room %d took %v to wake — the loop is deciding to wait "+
				"far longer than a person will", i, d)
		}
	}
}

// A relay that dies and comes back, and two nodes that have to notice.
//
// WHAT A STALE SOCKET LOOKS LIKE FROM INSIDE: nothing. The connection is
// still an object, the pool still hands it out, sync is still "active" and
// the health word is still green — and every message goes into a socket with
// nobody on the other end. That is the failure this reproduces on purpose,
// because it is indistinguishable from working until somebody notices a
// conversation has been silent for an hour.
//
// The AR-1c gate found it on a phone; this is the same thing without one, so
// it can be fixed in seconds rather than in three-minute rounds.
func TestSoakARelayThatComesBackIsFoundAgain(t *testing.T) {
	if os.Getenv("QUIET_RELAY_SOAK") != "1" {
		t.Skip("set QUIET_RELAY_SOAK=1 (this one restarts a relay mid-test)")
	}
	// A FIXED PORT, because the point is that it comes back at the SAME
	// address — a new address would be an ordinary reconnection somewhere
	// else, which nobody doubts.
	const addr = "127.0.0.1:37411"
	srv, _, err := relay.StartServer(addr, relay.DefaultLimits())
	if err != nil {
		t.Skipf("cannot bind %s: %v", addr, err)
	}

	sender := openRuntime(t, t.TempDir(), "alice")
	defer sender.Close()
	phone := openRuntime(t, t.TempDir(), "bob")
	defer phone.Close()

	tid, err := sender.CreateSpace("a room")
	if err != nil {
		t.Fatal(err)
	}
	for _, rt := range []*Runtime{sender, phone} {
		if err := rt.SetSettings(Settings{Relay: addr}); err != nil {
			t.Fatal(err)
		}
	}
	info, err := sender.MintPass(tid, 1, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := phone.JoinByPass(info.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, phone, reqID, JoinReady)

	arrival := func(text string, limit time.Duration) time.Duration {
		start := time.Now()
		if _, err := sender.Say(tid, text, SayOptions{}); err != nil {
			t.Fatal(err)
		}
		for time.Since(start) < limit {
			if sp, ok := phone.spaceForTest(tid); ok {
				found := false
				_ = sp.Log.Replay(func(a eventlog.Applied) error {
					if e, ok := sp.State.EntryByID(a.ID); ok &&
						e.Content.Text != nil && e.Content.Text.Text == text {
						found = true
					}
					return nil
				})
				if found {
					return time.Since(start)
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		return -1
	}

	if d := arrival("before", 90*time.Second); d < 0 {
		srv.Close()
		t.Fatal("nothing arrived before the relay was touched")
	} else {
		t.Logf("before: %v", d)
	}

	// The relay dies with every connection open and comes back at the same
	// address a moment later. Both nodes now hold sockets to a process that
	// does not exist.
	srv.Close()
	time.Sleep(3 * time.Second)
	again, _, err := relay.StartServer(addr, relay.DefaultLimits())
	if err != nil {
		t.Fatalf("the relay could not come back: %v", err)
	}
	defer again.Close()

	d := arrival("after the relay came back", 2*time.Minute)
	if d < 0 {
		t.Fatal("a message sent after the relay came back never arrived — " +
			"the connection is dead and nothing on either side noticed")
	}
	t.Logf("after: %v", d)
	if d > 30*time.Second {
		t.Errorf("it took %v to notice a dead connection; a person calls that "+
			"silence, not recovery", d)
	}
}

// The same relay, back from the dead, over a PINNED connection.
//
// WHY A SECOND VERSION OF THE SAME TEST. The first one dialled 127.0.0.1,
// and loopback skips pinning entirely (see dialRelay): it proved that a
// dead socket is noticed, and said nothing about the path a phone actually
// uses, which is a LAN address with a confirmed TOFU pin. On a phone that
// path did not recover in three minutes while this one recovered in
// thirteen seconds — and the difference between the two was the whole
// question.
//
// The relay keeps its KEY across the restart and mints a fresh certificate,
// exactly as `terminal-relay --data` does. That is the honest reproduction:
// clients pin the key, so this is the same relay coming back, not a new one
// wearing its name.
func TestSoakAPinnedRelayThatComesBackIsFoundAgain(t *testing.T) {
	if os.Getenv("QUIET_RELAY_SOAK") != "1" {
		t.Skip("set QUIET_RELAY_SOAK=1 (this one restarts a relay mid-test)")
	}
	host := nonLoopbackAddr(t)
	const port = 37421
	addr := fmt.Sprintf("%s:%d", host, port)

	cert, pin := fixedRelayIdentity(t)
	srv, _, err := relay.StartServerWithIdentity(fmt.Sprintf("0.0.0.0:%d", port),
		relay.DefaultLimits(), cert)
	if err != nil {
		t.Skipf("cannot bind %d: %v", port, err)
	}

	sender := openRuntime(t, t.TempDir(), "alice")
	defer sender.Close()
	phone := openRuntime(t, t.TempDir(), "bob")
	defer phone.Close()

	tid, err := sender.CreateSpace("a room")
	if err != nil {
		t.Fatal(err)
	}
	for _, rt := range []*Runtime{sender, phone} {
		if err := rt.TrustRelay(addr, pin); err != nil {
			t.Fatalf("pinning the relay: %v", err)
		}
		if err := rt.SetSettings(Settings{Relay: addr}); err != nil {
			t.Fatal(err)
		}
	}
	info, err := sender.MintPass(tid, 1, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := phone.JoinByPass(info.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, phone, reqID, JoinReady)

	arrival := func(text string, limit time.Duration) time.Duration {
		start := time.Now()
		if _, err := sender.Say(tid, text, SayOptions{}); err != nil {
			t.Fatal(err)
		}
		for time.Since(start) < limit {
			if sp, ok := phone.spaceForTest(tid); ok {
				found := false
				_ = sp.Log.Replay(func(a eventlog.Applied) error {
					if e, ok := sp.State.EntryByID(a.ID); ok &&
						e.Content.Text != nil && e.Content.Text.Text == text {
						found = true
					}
					return nil
				})
				if found {
					return time.Since(start)
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		return -1
	}

	if d := arrival("before", 90*time.Second); d < 0 {
		srv.Close()
		t.Fatal("nothing arrived over the pinned connection at all")
	} else {
		t.Logf("before: %v", d)
	}

	srv.Close()
	time.Sleep(3 * time.Second)
	again, _, err := relay.StartServerWithIdentity(fmt.Sprintf("0.0.0.0:%d", port),
		relay.DefaultLimits(), cert)
	if err != nil {
		t.Fatalf("the relay could not come back: %v", err)
	}
	defer again.Close()

	d := arrival("after the relay came back", 3*time.Minute)
	if d < 0 {
		t.Fatal("a message sent after the SAME relay came back never arrived — " +
			"a pinned connection that dies is never rebuilt, and from outside " +
			"the conversation simply goes quiet")
	}
	t.Logf("after: %v", d)
	if d > 30*time.Second {
		t.Errorf("it took %v to rebuild a pinned connection; a person calls "+
			"that silence, not recovery", d)
	}
}

// nonLoopbackAddr finds this machine's LAN address, because loopback is
// exactly the case that skips the code under test.
func nonLoopbackAddr(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("no interfaces: %v", err)
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || ipn.IP.To4() == nil {
			continue
		}
		return ipn.IP.String()
	}
	t.Skip("no non-loopback IPv4 address on this machine")
	return ""
}

// fixedRelayIdentity mints one key and keeps it, the way `--data` does.
func fixedRelayIdentity(t *testing.T) (*tls.Certificate, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(crand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(parsed.RawSubjectPublicKeyInfo)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv},
		base64.StdEncoding.EncodeToString(sum[:])
}

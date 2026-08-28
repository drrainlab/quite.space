package node

// The relay pool judges relays. A radio segment is not one.
//
// This is pinned by a test rather than left to the suite's luck because
// the suite's luck is exactly what hid it: on fast iron the breaker had
// not gotten around to the address, the honest refusal came out, and
// everything was green. It surfaced only under docker --cpus=2, where
// the breaker had time to be wrong — the same time it has on a phone.

import (
	"strings"
	"testing"
	"time"
)

// markOffline puts an address into the pool's backoff, the state that
// makes health() say "offline".
func markOffline(t *testing.T, rt *Runtime, addr string) {
	t.Helper()
	pe := rt.pool().peer(addr)
	pe.mu.Lock()
	pe.backoffUntil = time.Now().Add(time.Hour)
	pe.mu.Unlock()
	if got := rt.pool().health(addr); got != "offline" {
		t.Fatalf("the fixture did not take: health(%s) = %s", addr, got)
	}
}

func TestARadioRendezvousSurvivesTheRelayBreaker(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	addr := radioRendezvousPrefix + "abc123"
	s := rt.GetSettings()
	s.Relay = addr
	if err := rt.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	// The pool cannot dial a radio segment, so sooner or later it says
	// offline about it. That must not erase the rendezvous.
	markOffline(t, rt, addr)

	if got := rt.ResolvePersonalRelay(); got != addr {
		t.Fatalf("the relay breaker swallowed a radio rendezvous: %q", got)
	}

	// And the refusal a person actually reads stays the SPECIFIC one.
	// The wrong one told them to set a relay in Settings — the relay they
	// had already set, which is the sentence this codebase removed once
	// before and which came back through the health filter.
	_, err := rt.CreateQuickLink(QuickLinkOptions{Approval: "host"})
	if err == nil {
		t.Fatal("host approval was accepted on a rendezvous that holds nothing")
	}
	for _, want := range []string{"decide", "holds nothing"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal should mention %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "set a relay in Settings") {
		t.Fatalf("told to set the relay they had already set: %v", err)
	}
}

// A REAL relay in backoff must still be filtered — the fix is narrow, and
// a test that only proves the new case would let the old one rot.
func TestARelayInBackoffIsStillSkipped(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	const addr = "198.51.100.7:7411"
	s := rt.GetSettings()
	s.Relay = addr
	if err := rt.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	markOffline(t, rt, addr)
	if got := rt.ResolvePersonalRelay(); got != "" {
		t.Fatalf("a relay in backoff was picked anyway: %q", got)
	}
	// PersonalRelayAddress still NAMES it: an address in a card is a place
	// to look later, from another machine and another moment.
	if got := rt.PersonalRelayAddress(); got != addr {
		t.Fatalf("naming the relay must not consult the breaker: %q", got)
	}
}

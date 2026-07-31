// RR-5: the signed relay set inside SpacePolicy labels.
package terminals

import "testing"

func TestRelaySetLabelsRoundTrip(t *testing.T) {
	p := SpacePolicy{
		Visibility: VisibilityPublic, Join: JoinOpen, Publish: PublishAll,
		Relays: []string{"official:eu-1", "custom:tls://relay.example.org:7411"},
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	got := ParsePolicy(p.Labels())
	if len(got.Relays) != 2 || got.Relays[0] != "official:eu-1" ||
		got.Relays[1] != "custom:tls://relay.example.org:7411" {
		t.Fatalf("round trip lost order or entries: %v", got.Relays)
	}
}

func TestRelaySetBounds(t *testing.T) {
	base := SpacePolicy{Visibility: VisibilityPublic, Join: JoinOpen, Publish: PublishAll}

	three := base
	three.Relays = []string{"official:a", "official:b", "official:c"}
	if err := three.Validate(); err == nil {
		t.Fatal("three relays validated")
	}
	bad := base
	bad.Relays = []string{"relay.example.org:7411"} // no scheme — not a RelayRef
	if err := bad.Validate(); err == nil {
		t.Fatal("a bare host:port validated as a RelayRef")
	}
	private := SpacePolicy{Relays: []string{"official:a"}}
	if err := private.Validate(); err == nil {
		t.Fatal("a private space carried a relay set")
	}
}

func TestMalformedRelayLabelFailsClosed(t *testing.T) {
	labels := []string{
		"qp.visibility=public", "qp.join=open",
		"qp.relay=http://not-a-ref",
	}
	if got := ParsePolicy(labels); got.IsPublic() {
		t.Fatal("a malformed relay ref did not fail the policy closed")
	}
	// Three relay labels: ambiguous, fail closed too.
	labels = []string{
		"qp.visibility=public", "qp.join=open",
		"qp.relay=official:a", "qp.relay=official:b", "qp.relay=official:c",
	}
	if got := ParsePolicy(labels); got.IsPublic() {
		t.Fatal("an oversized relay set did not fail closed")
	}
}

// An OLD build's behavior is simulated by what ParsePolicy guarantees for
// every unknown qp.* key: silently ignored. The relay label is only
// special to builds that know it — this pins that a policy WITHOUT the
// case would still parse (the switch has no default-bad arm).
func TestUnknownPolicyLabelsStayIgnored(t *testing.T) {
	labels := []string{
		"qp.visibility=public", "qp.join=open",
		"qp.some-future-key=whatever",
	}
	got := ParsePolicy(labels)
	if !got.IsPublic() {
		t.Fatal("an unknown qp key failed a valid policy closed")
	}
}

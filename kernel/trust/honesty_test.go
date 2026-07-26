// Honesty snapshot tests (plan §26, M0.5 acceptance): every test here pins a
// case where an ordinary app would show a stronger status than is proven.
// If one of these fails, the kernel has learned to lie.
package trust

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

func TestRelayAckIsNotDelivered(t *testing.T) {
	e := NewEngine()
	event := id.EventID{1}
	dest := id.TerminalID{2}

	if err := e.RecordTransportReceipt(event, dest, claims.DeliveryAcceptedByRelay); err != nil {
		t.Fatal(err)
	}
	st := e.Delivery(event, dest)
	if st.Level != claims.DeliveryAcceptedByRelay {
		t.Fatalf("level = %v", st.Level)
	}
	if st.Level >= claims.DeliveryReceivedByTerminal {
		t.Fatal("relay ACK projected as delivery")
	}
	// A transport claiming destination-level delivery is rejected outright.
	if err := e.RecordTransportReceipt(event, dest, claims.DeliveryReceivedByTerminal); err == nil {
		t.Fatal("transport overclaim accepted")
	}
	if e.Delivery(event, dest).Level != claims.DeliveryAcceptedByRelay {
		t.Fatal("overclaim mutated proven level")
	}
}

func TestNoUpgradeWithoutProofEvent(t *testing.T) {
	e := NewEngine()
	event := id.EventID{3}
	dest := id.TerminalID{4}

	// Nothing recorded: unknown, fail closed.
	if st := e.Delivery(event, dest); st.Level != claims.DeliveryUnknown || st.Proof != ProofNone {
		t.Fatalf("unproven pair projected as %v", st)
	}

	// Destination-level status appears only via a signed receipt Signal.
	env := &signal.Envelope{
		Terminal: dest,
		Schema:   "receipt.delivery.v1",
	}
	r := receiptsPayload(t, event, claims.DeliveryReceivedByTerminal)
	env.Payload = r
	proofID := id.EventID{9}
	if err := e.IngestReceiptSignal(env, proofID); err != nil {
		t.Fatal(err)
	}
	st := e.Delivery(event, dest)
	if st.Level != claims.DeliveryReceivedByTerminal || st.Proof != ProofSignedReceipt || st.Event != proofID {
		t.Fatalf("receipt projection wrong: %+v", st)
	}
}

func TestReadReceiptIsOptIn(t *testing.T) {
	e := NewEngine()
	event := id.EventID{5}
	dest := id.TerminalID{6}
	env := &signal.Envelope{Terminal: dest, Schema: "receipt.delivery.v1"}
	env.Payload = receiptsPayload(t, event, claims.DeliveryReceivedByTerminal)
	if err := e.IngestReceiptSignal(env, id.EventID{10}); err != nil {
		t.Fatal(err)
	}
	// received_by_terminal never implies presented/acknowledged.
	if e.Delivery(event, dest).Level >= claims.DeliveryPresentedToHuman {
		t.Fatal("delivery inflated to read status")
	}
}

func TestStalePresenceIsNotOnline(t *testing.T) {
	e := NewEngine()
	src := id.TerminalID{7}
	e.UpdatePresence(claims.Presence{
		State: "listening", EmittedAt: 1000, ExpiresAt: 1300, Source: src,
	})
	// Within TTL: current.
	p := e.Presence(src, 1200)
	if !p.Current || p.State != "listening" {
		t.Fatalf("current presence wrong: %+v", p)
	}
	// After TTL: only last-known + age is expressible.
	p = e.Presence(src, 2020)
	if p.Current {
		t.Fatal("expired presence projected as current")
	}
	if !p.Known || p.State != "listening" || p.AgeSeconds != 1020 {
		t.Fatalf("last-known projection wrong: %+v", p)
	}
	// Unknown terminal: nothing, not offline-as-fact.
	if p := e.Presence(id.TerminalID{99}, 2020); p.Known {
		t.Fatal("unknown terminal has presence")
	}
}

// A UI that counts presence down must count down the expiry the member
// SIGNED, not a TTL it assumed locally. Anything else drifts the moment a
// client and a member disagree about how long presence lasts.
func TestRemainingTimeComesFromTheSignedExpiry(t *testing.T) {
	e := NewEngine()
	src := id.TerminalID{7}
	e.UpdatePresence(claims.Presence{
		State: "listening", EmittedAt: 1000, ExpiresAt: 1300, Source: src,
	})
	if p := e.Presence(src, 1200); p.RemainingSeconds != 100 {
		t.Fatalf("remaining wrong: %+v", p)
	}
	// An expired announce has no remaining time to report — only an age.
	if p := e.Presence(src, 1400); p.RemainingSeconds != 0 {
		t.Fatalf("expired presence still reports time left: %+v", p)
	}
}

func TestSelfDeclaredIsNeverVerified(t *testing.T) {
	self := claims.Claim{Key: "calibrated", Value: "true",
		Origin: claims.OriginSelfDeclared, IssuedAt: 100}
	verified := claims.Claim{Key: "calibrated", Value: "true",
		Origin: claims.OriginThirdPartyVerified, IssuedAt: 100}
	if IsVerified(self, 200) {
		t.Fatal("self-declared claim shown as verified")
	}
	if !IsVerified(verified, 200) {
		t.Fatal("verified claim rejected")
	}
	// Expired verification no longer counts.
	verified.ExpiresAt = 150
	if IsVerified(verified, 200) {
		t.Fatal("expired verification still shown as verified")
	}
	// Projection always carries the origin.
	if p := ProjectClaim(self, 200); p.Origin != claims.OriginSelfDeclared {
		t.Fatal("projection lost claim origin")
	}
}

func TestAIOutputIsNotHuman(t *testing.T) {
	// Authorship survives decode untouched; unknown values degrade to
	// unknown, never to human (checked at the signal layer).
	if signal.AuthorshipAIAgent.String() == signal.AuthorshipHuman.String() {
		t.Fatal("authorship enum collapsed")
	}
	var a signal.Authorship = 200 // future value
	if a.String() != "unknown" {
		t.Fatalf("future authorship projected as %q", a.String())
	}
}

func TestSimulatedAndStaleMeasurements(t *testing.T) {
	// Simulated flag must survive round-trip: prediction ≠ measurement.
	o := &schemas.Observation{CentiValue: 2360, Unit: "celsius",
		ObservedAt: 1000, StaleAfter: 600, Simulated: true}
	enc, err := o.Encode()
	if err != nil {
		t.Fatal(err)
	}
	back, err := schemas.DecodeObservation(enc)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Simulated {
		t.Fatal("simulated flag lost — simulation would display as measurement")
	}
	// Freshness: a two-hour-old reading is not "temperature now".
	if f := ObservationFreshness(1000, 600, 1500); f != FreshnessCurrent {
		t.Fatalf("fresh reading marked %v", f)
	}
	if f := ObservationFreshness(1000, 600, 8200); f != FreshnessStale {
		t.Fatal("stale reading not marked stale")
	}
	if f := ObservationFreshness(0, 0, 8200); f != FreshnessUnknown {
		t.Fatal("missing freshness data not marked unknown")
	}
}

func receiptsPayload(t *testing.T, subject id.EventID, level claims.DeliveryLevel) []byte {
	t.Helper()
	// Local mirror of receipts.Delivery to avoid an import cycle in tests.
	d := deliveryPayload{subject: subject, level: level}
	return d.encode()
}

type deliveryPayload struct {
	subject id.EventID
	level   claims.DeliveryLevel
}

func (d deliveryPayload) encode() []byte {
	buf := []byte{0xa2} // map(2)
	buf = append(buf, 0x01, 0x58, 0x20)
	buf = append(buf, d.subject[:]...)
	buf = append(buf, 0x02)
	buf = append(buf, byte(d.level)) // levels < 24 encode as one byte
	return buf
}

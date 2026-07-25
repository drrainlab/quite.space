package node

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/bridge"
)

// custodyFixture is one node with one tracked event and a pinned gateway.
type custodyFixture struct {
	rt   *Runtime
	tid  id.TerminalID
	eid  id.EventID
	priv ed25519.PrivateKey
	now  time.Time
}

func newCustodyFixture(t *testing.T) *custodyFixture {
	t.Helper()
	rt := openRuntime(t, t.TempDir(), "bob")
	t.Cleanup(rt.Close)
	tid, err := rt.CreateSpace("Custody")
	if err != nil {
		t.Fatal(err)
	}
	eid, err := rt.Say(tid, "who has this now", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.PinCustodian("radio", pub); err != nil {
		t.Fatal(err)
	}
	return &custodyFixture{rt: rt, tid: tid, eid: eid, priv: priv, now: time.Now()}
}

// openAttempt mints and durably records the token for the current epoch,
// exactly as the pump does before it sends anything.
func (f *custodyFixture) openAttempt(t *testing.T) AttemptID {
	t.Helper()
	f.rt.mu.Lock()
	defer f.rt.mu.Unlock()
	tok, ok := f.rt.openAttempt(f.tid, f.now)
	if !ok {
		t.Fatal("no attempt could be opened for a due intent")
	}
	return tok
}

// receipt builds a signed receipt from the pinned gateway.
func (f *custodyFixture) receipt(kind bridge.ReceiptKind, attempt []byte,
	lease string, expires time.Time) *bridge.CustodyReceipt {

	r := &bridge.CustodyReceipt{
		FrameIDs:    []id.EventID{f.eid},
		StoreID:     "store",
		AcceptedAt:  uint64(f.now.Unix()),
		ExpiresAt:   uint64(expires.Unix()),
		Instance:    "gw0",
		Kind:        kind,
		Attempt:     attempt,
		Lease:       lease,
		IngressLink: "mesh:test",
		LoopDomain:  "radio",
	}
	raw := r.Sign(f.priv)
	decoded, err := bridge.DecodeReceipt(raw)
	if err != nil {
		panic(err)
	}
	return decoded
}

// apply runs the machine as the engine callback would.
func (f *custodyFixture) apply(rec *bridge.CustodyReceipt) ReceiptOutcome {
	f.rt.mu.Lock()
	defer f.rt.mu.Unlock()
	return f.rt.applyReceiptToLedger(f.eid, rec, f.now)
}

func (f *custodyFixture) intent(t *testing.T) DeliveryIntent {
	t.Helper()
	in, ok := f.rt.ledger.Get(f.eid)
	if !ok {
		t.Fatal("intent missing")
	}
	return in
}

// (1) The acknowledgement is lost, custody expires, a new attempt begins —
// and then the ORIGINAL acknowledgement finally arrives. It must not
// complete the attempt that replaced it.
func TestLateAcceptFromExpiredAttemptChangesNothing(t *testing.T) {
	f := newCustodyFixture(t)
	attemptA := f.openAttempt(t)
	ackA := f.receipt(bridge.ReceiptAccepted, attemptA[:], "L1", f.now.Add(time.Hour))

	// Custody expires without the node ever seeing the acknowledgement, so
	// the intent is due again and the next send opens a new epoch.
	f.rt.mu.Lock()
	_, _, err := f.rt.ledger.Update(f.eid, f.now, func(in *DeliveryIntent) bool {
		in.State = IntentRetryable
		in.Attempt = AttemptID{}
		return true
	})
	f.rt.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	attemptB := f.openAttempt(t)
	if attemptB == attemptA {
		t.Fatal("a new responsibility epoch reused the old token")
	}

	if got := f.apply(ackA); got != ReceiptAudited {
		t.Fatalf("a late acceptance from a dead attempt was applied: %v", got)
	}
	in := f.intent(t)
	if in.State == IntentCustody {
		t.Fatal("attempt B was put in custody by attempt A's acknowledgement")
	}
	if in.Attempt != attemptB {
		t.Fatalf("the current attempt changed: %s", in.Attempt.Hex())
	}
	if audits := f.rt.ReceiptAudits(f.eid); len(audits) == 0 {
		t.Fatal("the stale receipt was dropped silently instead of recorded")
	}
}

// (2) Accept then withdraw, then a DUPLICATE of the original accept.
func TestDuplicateAcceptAfterWithdrawalChangesNothing(t *testing.T) {
	f := newCustodyFixture(t)
	attemptA := f.openAttempt(t)
	ackA := f.receipt(bridge.ReceiptAccepted, attemptA[:], "L1", f.now.Add(time.Hour))
	if got := f.apply(ackA); got != ReceiptTookCustody {
		t.Fatalf("acceptance: %v", got)
	}
	lapse := f.receipt(bridge.ReceiptLapsed, attemptA[:], "L1", f.now.Add(time.Hour))
	if got := f.apply(lapse); got != ReceiptReturned {
		t.Fatalf("withdrawal: %v", got)
	}
	attemptB := f.openAttempt(t)
	if attemptB == attemptA {
		t.Fatal("responsibility came back but the epoch did not advance")
	}

	if got := f.apply(ackA); got != ReceiptAudited {
		t.Fatalf("a duplicate of the dead attempt's acceptance was applied: %v", got)
	}
	if in := f.intent(t); in.State == IntentCustody {
		t.Fatal("attempt B ended up in custody under attempt A's promise")
	}
}

// (3) A late withdrawal from a dead attempt must not cancel live custody.
func TestLateWithdrawalDoesNotCancelActiveCustody(t *testing.T) {
	f := newCustodyFixture(t)
	attemptA := f.openAttempt(t)
	if got := f.apply(f.receipt(bridge.ReceiptAccepted, attemptA[:], "L1",
		f.now.Add(time.Hour))); got != ReceiptTookCustody {
		t.Fatal("setup: attempt A not in custody")
	}
	staleLapse := f.receipt(bridge.ReceiptLapsed, attemptA[:], "L1", f.now.Add(time.Hour))

	// Custody ends and a new attempt is accepted by a different gateway.
	if got := f.apply(staleLapse); got != ReceiptReturned {
		t.Fatal("setup: withdrawal not applied")
	}
	attemptB := f.openAttempt(t)
	if got := f.apply(f.receipt(bridge.ReceiptAccepted, attemptB[:], "L2",
		f.now.Add(time.Hour))); got != ReceiptTookCustody {
		t.Fatal("setup: attempt B not in custody")
	}

	// The old withdrawal arrives again, delayed on the air.
	if got := f.apply(staleLapse); got != ReceiptAudited {
		t.Fatalf("a withdrawal from a dead attempt cancelled live custody: %v", got)
	}
	in := f.intent(t)
	if in.State != IntentCustody || in.Lease != "L2" {
		t.Fatalf("live custody was disturbed: state=%v lease=%q", in.State, in.Lease)
	}
}

// (4) A duplicate of the CURRENT acceptance is idempotent.
func TestDuplicateAcceptOfCurrentLeaseIsIdempotent(t *testing.T) {
	f := newCustodyFixture(t)
	attempt := f.openAttempt(t)
	ack := f.receipt(bridge.ReceiptAccepted, attempt[:], "L1", f.now.Add(time.Hour))
	if got := f.apply(ack); got != ReceiptTookCustody {
		t.Fatalf("first acceptance: %v", got)
	}
	before := f.intent(t)
	if got := f.apply(ack); got != ReceiptRepeated {
		t.Fatalf("a repeat of the current acceptance was not idempotent: %v", got)
	}
	after := f.intent(t)
	if after.State != before.State || after.Lease != before.Lease ||
		after.LeaseExpires != before.LeaseExpires || after.AttemptNo != before.AttemptNo {
		t.Fatalf("a repeat changed the intent:\n before %+v\n after  %+v", before, after)
	}
}

// A second gateway answering a hand-off another lease already holds must
// not silently take it over. In the beta exactly one downlink gateway is
// active per segment, so this is a misconfiguration — and abandoning a
// lease we are relying on, on the word of a receipt we did not ask for,
// would be the wrong way to find that out.
func TestSecondLeaseDoesNotSilentlyReplaceTheFirst(t *testing.T) {
	f := newCustodyFixture(t)
	attempt := f.openAttempt(t)
	if got := f.apply(f.receipt(bridge.ReceiptAccepted, attempt[:], "L1",
		f.now.Add(time.Hour))); got != ReceiptTookCustody {
		t.Fatal("setup")
	}
	if got := f.apply(f.receipt(bridge.ReceiptAccepted, attempt[:], "L2",
		f.now.Add(2*time.Hour))); got != ReceiptConflicted {
		t.Fatalf("a second lease replaced the first: %v", got)
	}
	if in := f.intent(t); in.Lease != "L1" {
		t.Fatalf("the first promise was abandoned: lease %q", in.Lease)
	}
	if audits := f.rt.ReceiptAudits(f.eid); len(audits) == 0 {
		t.Fatal("the conflict was not recorded")
	}
}

// (8) A receipt with no attempt token names no hand-off. It may be decoded
// and logged; it must not suspend or release responsibility. Wire
// compatibility must not be paid for with correctness.
func TestReceiptWithoutAttemptCannotMoveResponsibility(t *testing.T) {
	f := newCustodyFixture(t)
	attempt := f.openAttempt(t)

	unbound := f.receipt(bridge.ReceiptAccepted, nil, "L1", f.now.Add(time.Hour))
	if got := f.apply(unbound); got != ReceiptUnbound {
		t.Fatalf("a receipt naming no hand-off was applied: %v", got)
	}
	if in := f.intent(t); in.State == IntentCustody {
		t.Fatal("responsibility was suspended by a receipt that named no attempt")
	}

	// And it cannot release either: put the intent in custody first.
	if got := f.apply(f.receipt(bridge.ReceiptAccepted, attempt[:], "L1",
		f.now.Add(time.Hour))); got != ReceiptTookCustody {
		t.Fatal("setup: not in custody")
	}
	unboundLapse := f.receipt(bridge.ReceiptLapsed, nil, "L1", f.now.Add(time.Hour))
	if got := f.apply(unboundLapse); got != ReceiptUnbound {
		t.Fatalf("an unbound withdrawal was applied: %v", got)
	}
	if in := f.intent(t); in.State != IntentCustody {
		t.Fatal("an unbound withdrawal released responsibility")
	}
	if audits := f.rt.ReceiptAudits(f.eid); len(audits) < 2 {
		t.Fatalf("unbound receipts were not recorded: %d audits", len(audits))
	}
}

// (6) The node crashes after the attempt token is durable but before
// anything is sent under it. On restart it must CONTINUE that attempt
// rather than mint a second one: an acknowledgement already in flight names
// the first, and a node that had forgotten it would ignore its own answer.
func TestAttemptSurvivesCrashBeforeFirstSend(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "bob")
	tid, err := rt.CreateSpace("Custody")
	if err != nil {
		t.Fatal(err)
	}
	eid, err := rt.Say(tid, "minted but not yet sent", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rt.mu.Lock()
	tok, ok := rt.openAttempt(tid, now)
	rt.mu.Unlock()
	if !ok {
		t.Fatal("no attempt opened")
	}
	// Power cut here: nothing was sent, and Close is never called.
	rt.ledger.Close()

	rt2 := openRuntime(t, dir, "bob")
	defer rt2.Close()
	in, found := rt2.ledger.Get(eid)
	if !found {
		t.Fatal("responsibility lost across the restart")
	}
	if in.Attempt != tok {
		t.Fatalf("the attempt was not resumed: %s then %s",
			tok.Hex(), in.Attempt.Hex())
	}
	rt2.mu.Lock()
	again, ok := rt2.openAttempt(tid, now)
	rt2.mu.Unlock()
	if !ok {
		t.Fatal("no attempt after restart")
	}
	if again != tok {
		t.Fatalf("the restart minted a SECOND token for one epoch: %s then %s",
			tok.Hex(), again.Hex())
	}
}

// (7) Repeated sends within one attempt carry one token. Losing a
// transport acknowledgement is not evidence that anything changed, so it
// must not start a new responsibility epoch.
func TestRetriesWithinOneAttemptKeepOneToken(t *testing.T) {
	f := newCustodyFixture(t)
	first := f.openAttempt(t)

	// Several passes with no acknowledgement at all: the pump would call
	// openAttempt each time and stamp whatever it returns.
	for range 5 {
		f.rt.mu.Lock()
		f.rt.markHandedToTransport([]id.EventID{f.eid}, "radio", f.now)
		again, ok := f.rt.openAttempt(f.tid, f.now)
		f.rt.mu.Unlock()
		if !ok {
			t.Fatal("the attempt stopped being open while still unacknowledged")
		}
		if again != first {
			t.Fatalf("an unacknowledged retry minted a new epoch: %s then %s",
				first.Hex(), again.Hex())
		}
	}
	in := f.intent(t)
	if in.AttemptNo != 1 {
		t.Fatalf("retries counted as %d attempts, want 1", in.AttemptNo)
	}
	if in.Proof != claims.DeliveryHandedToTransport {
		t.Fatalf("bytes into an adapter proved more than handover: %v", in.Proof)
	}
}

// The layered rule from RB-0B, now enforced by the ledger: custody
// suspends retry, and the horizon the gateway promised is what brings it
// back if the withdrawal never arrives.
func TestCustodyExpiryReturnsResponsibilityWithoutAWithdrawal(t *testing.T) {
	f := newCustodyFixture(t)
	attempt := f.openAttempt(t)
	horizon := f.now.Add(30 * time.Minute)
	if got := f.apply(f.receipt(bridge.ReceiptAccepted, attempt[:], "L1",
		horizon)); got != ReceiptTookCustody {
		t.Fatal("setup")
	}
	if due := f.rt.ledger.Due(f.now.Add(time.Minute), 10); len(due) != 0 {
		t.Fatal("an intent in custody is still being retried")
	}
	// The withdrawal is lost on the air; the horizon is the backstop.
	due := f.rt.ledger.Due(horizon.Add(time.Second), 10)
	if len(due) != 1 || due[0].EventID != f.eid {
		t.Fatal("custody expired and responsibility never came back: it would " +
			"hang forever on a gateway that went quiet")
	}
}

// A structured trace of one transfer, and nothing else.
//
// This exists because three plausible explanations for eightfold frame
// amplification on a real link were each measured and each was wrong: the
// frame was not too long, consecutive transfers were not colliding, and the
// band was not busy. Excluding them is what says the next step is not another
// parameter sweep from outside but a record of what each side actually knew
// and when.
//
// The question it must answer, for ONE bad transfer:
//
//	which DATA fragments did the receiver actually get
//	which SACK bitmaps did it form, and when
//	which SACKs and COMMITs did the sender hear, and what did it resend
//
// with the standard of proof stated up front: for every DATA frame beyond the
// minimum, it must be possible to name the local state that made the sender
// send it again.
//
// NOTHING HERE CHANGES BEHAVIOUR. A nil Tracer costs one comparison per event
// and the events carry no allocation of their own.
package radiotransfer

import (
	"fmt"
	"strings"
	"time"
)

// Trace event names. Strings rather than an enum because these are read by a
// person looking at a log, and a number that has to be looked up is a number
// that gets misread at four in the morning.
const (
	TraceTransferCreated = "transfer_created"
	TraceWindowStarted   = "window_started"
	TraceDataTX          = "data_tx"
	TraceDataTXFailed    = "data_tx_failed"
	TraceDataRX          = "data_rx"
	TraceDataRXDuplicate = "data_rx_duplicate"
	TraceSACKArmed       = "sack_timer_armed"
	TraceSACKSuppressed  = "sack_suppressed"
	TraceSACKTX          = "sack_tx"
	TraceSACKRX          = "sack_rx"
	TraceCommitTX        = "commit_tx"
	TraceCommitRX        = "commit_rx"
	TraceCancelRX        = "cancel_rx"
	TraceAckTimeout      = "ack_timeout"
	TraceRetransmit      = "retransmit_selected"
	TraceCompleted       = "transfer_completed"
	TraceGaveUp          = "transfer_given_up"
	TraceBudgetExhausted = "repair_budget_exhausted"
)

// TraceEvent is one thing that happened, with the state that explains it.
type TraceEvent struct {
	At       time.Time
	Transfer TransferID
	Event    string

	// Fragment is the index this event concerns, or -1 when it concerns the
	// transfer as a whole. Zero is a real fragment index, so absence cannot
	// be spelled with a zero value.
	Fragment int
	Round    int
	Base     int
	Count    int

	// Missing and Have describe a SACK from the point of view of whoever is
	// looking at it, and Bitmap is rendered as text because the whole purpose
	// is to be read.
	Missing int
	Have    int
	Bitmap  string

	// Pending lists exactly which fragments a repair round chose to send
	// again. This is the field the standard of proof rests on.
	Pending []int

	Reason string
}

func (e TraceEvent) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %-20s %s", e.At.Format("15:04:05.000"), e.Event,
		e.Transfer.Short())
	if e.Fragment >= 0 {
		fmt.Fprintf(&b, " frag=%d", e.Fragment)
	}
	if e.Count > 0 {
		fmt.Fprintf(&b, " of=%d", e.Count)
	}
	if e.Round > 0 {
		fmt.Fprintf(&b, " round=%d", e.Round)
	}
	if e.Bitmap != "" {
		fmt.Fprintf(&b, " base=%d have=%d missing=%d bitmap=%s",
			e.Base, e.Have, e.Missing, e.Bitmap)
	}
	if len(e.Pending) > 0 {
		fmt.Fprintf(&b, " resending=%v", e.Pending)
	}
	if e.Reason != "" {
		fmt.Fprintf(&b, " reason=%s", e.Reason)
	}
	return b.String()
}

// Tracer receives events. It is called on the goroutine that produced the
// event, sometimes while a session lock is held, so an implementation must
// not block and must not call back into the session.
type Tracer func(TraceEvent)

// trace emits an event when anyone is listening.
func (s *Session) trace(ev TraceEvent) {
	if s.tracer == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	s.tracer(ev)
}

// bitmapText renders a SACK bitmap as one character per fragment — '#' for
// held, '.' for missing — and counts both.
func bitmapText(bitmap []byte, width int) (text string, have, missing int) {
	var b strings.Builder
	for i := range width {
		if HasBit(bitmap, i) {
			b.WriteByte('#')
			have++
		} else {
			b.WriteByte('.')
			missing++
		}
	}
	return b.String(), have, missing
}

// Supervised radios (RB-2). A radio goes away for ordinary reasons — a USB
// plug knocked loose, a node rebooting after someone changed its region, a
// WiFi node whose access point blinked — and before this a dropped link was
// permanent. The node went deaf, the bridge stopped carrying, and nothing
// anywhere said so: the reader goroutine simply exited.
//
// Supervised is a transports.Endpoint that outlives the device behind it. It
// redials with backoff and swaps the radio underneath, so everything above
// keeps one endpoint for the life of the process. That is what makes the
// stacking failure impossible rather than merely cleaned up: a caller that
// never has to re-attach cannot end up attached twice.
//
// While the radio is away, Send fails. That is deliberate and it is the
// honest answer: the frame did not go anywhere. A sender's queue keeps it,
// custody stays where it was, and the connectivity policy above sees a
// transport that is not working and can route around it.
package meshtastic

import (
	"errors"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/transports"
)

// Backoff is a retry schedule. Separate from the supervisor so the
// arithmetic can be tested without waiting for it.
type Backoff struct {
	Min, Max time.Duration
	// Stable is how long a link must hold before it counts as recovery.
	// Shorter, and a radio stuck in a reboot loop resets the schedule on
	// every cycle — which is the same as having no backoff at all.
	Stable  time.Duration
	attempt int
}

// Next returns the wait before the next dial and advances the schedule.
func (b *Backoff) Next() time.Duration {
	d := b.Min
	for range b.attempt {
		d *= 2
		if d >= b.Max {
			d = b.Max
			break
		}
	}
	b.attempt++
	return d
}

// Succeeded reports a link that has now ended, having lasted upFor. Only a
// link that actually held is treated as recovery.
func (b *Backoff) Succeeded(upFor time.Duration) {
	if upFor >= b.Stable {
		b.attempt = 0
	}
}

// DefaultBackoff is the schedule for a radio that is expected back.
func DefaultBackoff() Backoff {
	return Backoff{Min: time.Second, Max: 30 * time.Second, Stable: time.Minute}
}

// SuperviseStatus is what to show a person instead of unexplained silence.
type SuperviseStatus struct {
	Connected bool
	// Reconnecting distinguishes "no radio is configured" from "the radio is
	// gone and we are trying to get it back". Both are not-connected, and
	// they call for different reactions.
	Reconnecting bool
	NodeNum      uint32
	TX, RX       int
	// Attempts counts dials since the link first went down; Reconnects the
	// ones that worked. Attempts climbing with Reconnects beside it is a
	// flapping link; Attempts climbing alone is a radio that is not there.
	Attempts    int
	Reconnects  int
	NextRetryIn time.Duration
	Err         string
}

// Supervised is an Endpoint that keeps redialling one radio target.
type Supervised struct {
	target  string
	opts    Options
	backoff Backoff

	mu          sync.Mutex
	radio       *Radio
	cfg         NodeConfig
	closed      bool
	reconnects  int
	attempts    int
	nextRetryAt time.Time
	lastErr     string
	// tx and rx accumulate across radios: the counters describe the LINK a
	// person is watching, not the particular device object behind it, and
	// resetting them on every reconnect would hide exactly the flapping the
	// status is there to reveal.
	tx, rx int

	stop chan struct{}
	wg   sync.WaitGroup
}

// Supervise dials target and keeps it attached. Target forms:
//
//	tcp:HOST[:PORT]     WiFi node or meshtasticd (default port 4403)
//	serial:/dev/PATH    USB-attached node
//
// The FIRST dial is synchronous and its error is returned. A supervisor that
// swallowed it would turn "you typed the wrong device path" into a process
// quietly retrying a radio that does not exist — indistinguishable, from
// outside, from one that is merely out of range. Everything after this point
// is a link that once worked, and those are worth retrying.
func Supervise(target string, opts Options, backoff Backoff) (*Supervised, error) {
	radio, err := Dial(target, opts)
	if err != nil {
		return nil, err
	}
	s := &Supervised{
		target: target, opts: opts, backoff: backoff,
		radio: radio, cfg: radio.Config(), stop: make(chan struct{}),
	}
	s.wg.Add(1)
	go s.supervise()
	return s, nil
}

// Dial opens one radio without supervision.
func Dial(target string, opts Options) (*Radio, error) {
	switch {
	case len(target) > 4 && target[:4] == "tcp:":
		return dialTCP(target[4:], opts)
	case len(target) > 7 && target[:7] == "serial:":
		return openSerial(target[7:], opts)
	}
	return nil, errors.New("meshtastic: target must be tcp:HOST[:PORT] or serial:/dev/PATH")
}

func (s *Supervised) supervise() {
	defer s.wg.Done()
	upSince := time.Now()
	for {
		radio, ok := s.waitDown()
		if !ok {
			return
		}
		// Fold the dead radio's counters in before letting go of it.
		s.retire(radio)
		s.backoff.Succeeded(time.Since(upSince))

		next, ok := s.redial()
		if !ok {
			return
		}
		upSince = time.Now()
		s.mu.Lock()
		s.radio = next
		s.cfg = next.Config()
		s.reconnects++
		s.lastErr = ""
		s.mu.Unlock()
	}
}

// waitDown blocks until the current radio dies, returning it. Reports false
// if the supervisor was closed instead.
func (s *Supervised) waitDown() (*Radio, bool) {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return nil, false
		case <-t.C:
		}
		s.mu.Lock()
		radio := s.radio
		s.mu.Unlock()
		if radio == nil {
			continue
		}
		if closed, _ := radio.Closed(); closed {
			return radio, true
		}
	}
}

// retire folds a dead radio's counters into the link's and drops it.
func (s *Supervised) retire(radio *Radio) {
	_, why := radio.Closed()
	tx, rx := radio.Stats()
	radio.Close()
	s.mu.Lock()
	s.tx += tx
	s.rx += rx
	s.radio = nil
	if why != nil {
		s.lastErr = why.Error()
	}
	s.mu.Unlock()
}

func (s *Supervised) redial() (*Radio, bool) {
	for {
		wait := s.backoff.Next()
		s.mu.Lock()
		s.nextRetryAt = time.Now().Add(wait)
		s.mu.Unlock()

		select {
		case <-s.stop:
			return nil, false
		case <-time.After(wait):
		}

		s.mu.Lock()
		s.attempts++
		s.mu.Unlock()

		radio, err := Dial(s.target, s.opts)
		if err == nil {
			return radio, true
		}
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
	}
}

// ---- transports.Endpoint ----

// Send hands one packet to the radio, or fails if there is no radio to hand
// it to. Failing is the honest answer: nothing went out, and the caller's
// queue is the right place for it to wait.
func (s *Supervised) Send(pkt []byte) error {
	s.mu.Lock()
	radio, closed := s.radio, s.closed
	s.mu.Unlock()
	if closed {
		return errors.New("meshtastic: radio link closed")
	}
	if radio == nil {
		return errors.New("meshtastic: radio disconnected, reconnecting")
	}
	return radio.Send(pkt)
}

// Poll drains whatever the current radio received.
func (s *Supervised) Poll() [][]byte {
	s.mu.Lock()
	radio := s.radio
	s.mu.Unlock()
	if radio == nil {
		return nil
	}
	return radio.Poll()
}

// Capabilities are a property of the carrier, not of the particular device,
// so they hold across a reconnect.
func (s *Supervised) Capabilities() transports.Capabilities {
	return transports.Capabilities{
		MaxPayload: s.opts.withDefaults().MaxPayload,
		Realtime:   false,
		Ack:        transports.AckNone,
	}
}

// Closed reports the LINK, not the device: a radio being replaced is not a
// closed link, it is a link with nothing behind it at this instant. Only
// Close() ends it.
func (s *Supervised) Closed() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return true, errors.New("meshtastic: radio link closed")
	}
	return false, nil
}

// Close ends supervision and releases the device.
func (s *Supervised) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	radio := s.radio
	s.radio = nil
	close(s.stop)
	s.mu.Unlock()
	if radio != nil {
		radio.Close()
	}
	s.wg.Wait()
	return nil
}

// Config reports what the attached radio says it is configured for. While a
// radio is connected this is live — a node re-sends the affected message
// when someone changes its settings, so the answer keeps up with a radio
// being reconfigured at the bench. While it is away, this is the last thing
// the radio said.
func (s *Supervised) Config() NodeConfig {
	s.mu.Lock()
	radio, last := s.radio, s.cfg
	s.mu.Unlock()
	if radio == nil {
		return last
	}
	return radio.Config()
}

// Status reports the link for a person.
func (s *Supervised) Status() SuperviseStatus {
	s.mu.Lock()
	radio := s.radio
	st := SuperviseStatus{
		Reconnecting: radio == nil && !s.closed,
		Attempts:     s.attempts,
		Reconnects:   s.reconnects,
		Err:          s.lastErr,
		TX:           s.tx,
		RX:           s.rx,
	}
	if st.Reconnecting && !s.nextRetryAt.IsZero() {
		if d := time.Until(s.nextRetryAt); d > 0 {
			st.NextRetryIn = d
		}
	}
	s.mu.Unlock()
	if radio != nil {
		closed, _ := radio.Closed()
		st.Connected = !closed
		st.NodeNum = radio.NodeNum()
		tx, rx := radio.Stats()
		st.TX += tx
		st.RX += rx
	}
	return st
}

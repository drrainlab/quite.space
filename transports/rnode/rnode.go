// RNode as a SECOND carrier, and the check that the seam was real.
//
// The whole architecture rests on one claim: that a radio is a driver behind
// RadioDatagram, not a rewrite. This file is where that claim is either kept
// or exposed. Everything above it — fragmentation, selective repeat, dedup,
// addressing, the peer link, the invitation saga — is untouched by its
// existence.
//
// WHAT RNODE IS, AND WHY IT IS DIFFERENT IN KIND FROM MESHTASTIC. RNode is a
// modem. It has no channel table, no rebroadcast policy, no mesh of its own,
// no configuration transaction that can silently revert a field, and no
// opinion about what the bytes mean. You tell it a frequency, a bandwidth, a
// spreading factor, a coding rate and a power, and it hands you the frames it
// hears. Meshtastic is a messenger whose firmware we were using as a modem,
// and most of what this project spent days on — a config write that turned the
// transmitter off, a frequency slot inherited from a public channel, a
// rebroadcast mode that cost eightfold latency, queue refusals invisible until
// instrumented — was accidental complexity from that mismatch, not from LoRa.
//
// WHAT IT DOES NOT FIX: physics. LoRa is half-duplex, a frame at LONG_FAST
// equivalent settings is about two seconds of air, and a transmitting radio is
// deaf. Every collision this project has measured would happen here too.
package rnode

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/transports"
	"github.com/drrainlab/quiet_places/transports/radiotransfer"
	"go.bug.st/serial"
)

// KISS framing and the RNode command set.
//
// Taken from the reference implementation rather than guessed: a wrong command
// byte here is a radio that silently does nothing, which is the single hardest
// failure to tell from being out of range.
const (
	fend  = 0xC0
	fesc  = 0xDB
	tfend = 0xDC
	tfesc = 0xDD

	cmdData       = 0x00
	cmdFrequency  = 0x01
	cmdBandwidth  = 0x02
	cmdTXPower    = 0x03
	cmdSF         = 0x04
	cmdCR         = 0x05
	cmdRadioState = 0x06
	cmdDetect     = 0x08
	cmdSTALock    = 0x0B
	cmdLTALock    = 0x0C
	cmdError      = 0x90

	detectReq  = 0x73
	detectResp = 0x46

	radioOff = 0x00
	radioOn  = 0x01
	radioAsk = 0xFF

	errQueueFull    = 0x04
	errTXFailed     = 0x02
	errModemTimeout = 0x06
)

// Settings are the PHY, stated in full.
//
// There is no "preset" here on purpose. A preset is a name for a set of
// numbers, and the numbers are what the air actually sees; naming them would
// invite two radios to agree on a word while disagreeing on a bandwidth.
type Settings struct {
	FrequencyHz uint32
	BandwidthHz uint32
	SpreadingF  uint8 // 7..12
	CodingRate  uint8 // 5..8
	TXPowerDBm  uint8
}

// LongFastRU matches what the Meshtastic runs actually used, so a comparison
// compares CARRIERS rather than settings: SF11 at 250 kHz, 20 dBm.
//
// The frequency and the power are REGULATORY, not preference. Meshtastic's own
// region table defines RU as 868.7-869.2 MHz at up to 20 dBm, and that is the
// band this project has been transmitting in all along. A driver that hands
// the radio a number outside it would be putting a person on the air illegally
// — and unlike Meshtastic, RNode will do exactly what it is told.
func LongFastRU() Settings {
	return Settings{FrequencyHz: 868_950_000, BandwidthHz: 250_000,
		SpreadingF: 11, CodingRate: 5, TXPowerDBm: 20}
}

// MediumFast is SF9 at 250 kHz — roughly four times the rate of LONG_FAST,
// and the speed candidate Stage A named but never measured with a repair
// layer above it.
func MediumFast(freqHz uint32) Settings {
	return Settings{FrequencyHz: freqHz, BandwidthHz: 250_000,
		SpreadingF: 9, CodingRate: 5, TXPowerDBm: 22}
}

// MaxFrame is what the MODEM will accept in one frame. It is not the same
// question as what a frame SHOULD be — see mtuFor.
const MaxFrame = 500

// TargetFrameAirtime is what a frame WOULD be capped at if frame size were
// capped by airtime. It is not used to cap anything — see the refuted
// hypothesis below — and is kept because MTUFor is a useful thing to inspect
// before taking two radios into a field.
//
// THE HYPOTHESIS, AND ITS REFUTATION BY MEASUREMENT. Measured at SF11/250 kHz
// on two Heltec v3: 36 B took 0.75 s of wall clock, 200 B took 2.01 s, 400 B
// took 3.82 s — so a full 500-byte frame is ~4.6 s of air, nearly twice the
// transfer layer's 2.5 s FrameGap. That looked conclusive: the sender hands
// the modem frames faster than it can radiate them, the queue absorbs the
// difference (hence nothing is ever refused), the air is occupied
// continuously, and the receiver never gets a quiet slot for its SACK.
//
// So frame size was capped at 176 bytes to fit the gap, and Stage B was run
// again. IT WAS DRAMATICALLY WORSE:
//
//	size     MTU 500 frames/time     MTU 176 frames/time
//	 300      1-2 / 3.6 s             4-5 / 6.6 s
//	 700      2-3 / 8.8 s            10-15 / 24.9 s
//	1500      5-10 / 20.7 s          53-93 / 2m01 s
//
// The cost scales with the NUMBER OF FRAMES, not with bytes or with airtime
// per frame. Every extra fragment is another chance to lose one, another
// entry in a window that must be repaired, and the loss compounds far faster
// than a longer frame costs. Stage A agrees and should have been listened to
// first: 400-byte frames delivered 100%, so a big frame is not a fragile one
// on this link.
//
// Conclusion, recorded so it is not re-derived: on this carrier, FEWER AND
// LARGER frames win. The frame size stays at what the modem accepts.
const TargetFrameAirtime = 1500 * time.Millisecond

// Airtime models the time on air of a LoRa frame of n bytes, by the standard
// symbol arithmetic rather than by a fitted constant, so it stays right when
// somebody changes the spreading factor.
//
// It is a MODEL: measured wall-clock runs roughly 0.3-0.7 s above it, because
// serial transfer, firmware handling and host scheduling are real and are not
// time on air. The margin is deliberate — this decides a frame size, and
// being slightly conservative costs a few bytes while being optimistic costs
// the whole delivery mechanism.
func (s Settings) Airtime(n int) time.Duration {
	sf := float64(s.SpreadingF)
	bw := float64(s.BandwidthHz)
	if sf < 6 || bw <= 0 {
		return 0
	}
	tSym := math.Pow(2, sf) / bw // seconds

	// Low data rate optimisation is on when a symbol is long, which changes
	// the denominator below.
	de := 0.0
	if tSym > 0.016 {
		de = 1
	}
	preamble := (8 + 4.25) * tSym

	num := 8*float64(n) - 4*sf + 28 + 16
	den := 4 * (sf - 2*de)
	sym := math.Ceil(num/den) * float64(s.CodingRate-4+4)
	if sym < 0 {
		sym = 0
	}
	return time.Duration((preamble + (8+sym)*tSym) * float64(time.Second))
}

// MTUFor is the largest frame whose airtime stays within the target. Exported
// because the right frame size for a PHY is a fact worth inspecting before
// putting two radios in a field.
func MTUFor(s Settings) int {
	best := MinFrame
	for n := MinFrame; n <= MaxFrame; n++ {
		if s.Airtime(n) > TargetFrameAirtime {
			break
		}
		best = n
	}
	return best
}

// MinFrame is the floor, and it exists because the airtime target and a
// working frame can genuinely conflict.
//
// The transfer layer spends about 66 bytes per frame on its header and MAC.
// At SF12 a frame inside 1.5 s of air is 70 bytes — FOUR bytes of payload,
// which is not a slow frame but a useless one: a 6 KB message would need
// more fragments than the layer permits, so nothing would move at all.
// Where the two cannot both be had, the floor wins and the frame simply
// takes longer than the target. That is a stated trade, not an oversight —
// and it is why a very high spreading factor is a poor fit for bulk
// transfer rather than merely a patient one.
const MinFrame = 176

// openSettle is how long to wait after opening the port before configuring.
const openSettle = 2 * time.Second

// Radio is one RNode, presented as a radiotransfer.RadioDatagram.
type Radio struct {
	port serial.Port
	set  Settings
	mtu  int

	mu     sync.Mutex
	closed bool
	err    error

	inbox chan []byte
	stop  chan struct{}
	wg    sync.WaitGroup

	// refused counts frames the modem said it could not queue. Kept because
	// "the modem refused" and "the air lost it" are different problems with
	// different fixes, and only one of them is the carrier's.
	refused int

	// Diagnostics, so a zero result can be READ rather than guessed at.
	// rxBytes distinguishes "the serial link is silent" from "frames arrive
	// and something above drops them"; rxFrames counts them per command, so
	// an error the firmware is reporting cannot hide as an absence.
	rxBytes  int
	rxFrames map[byte]int
	lastErr  byte

	// estTxEnd is the queue model: when everything handed to the modem so
	// far is expected to have left the antenna. Advanced on every accepted
	// Send by the frame's modelled airtime plus a guard; never behind now.
	estTxEnd time.Time

	// The firmware reports whether the radio actually came up, and it can
	// refuse: a board may accept every PHY parameter, echo them all back
	// correctly, and still answer the state command with OFF. Measured on a
	// real board, where that produced 0 of 30 frames delivered with nothing
	// refused — indistinguishable from being out of range unless the driver
	// reads this.
	radioOn  bool
	sawState bool
}

// ErrRadioWillNotStart is returned when the modem accepted the configuration
// and then declined to power up its radio.
var ErrRadioWillNotStart = errors.New(
	"rnode: the modem accepted the settings but reports its radio as OFF")

// Counters reports what the serial side actually saw.
func (r *Radio) Counters() (bytes int, frames map[byte]int, lastErr byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f := make(map[byte]int, len(r.rxFrames))
	for k, v := range r.rxFrames {
		f[k] = v
	}
	return r.rxBytes, f, r.lastErr
}

// Open connects to an RNode over serial and brings the radio up with the given
// PHY. It does NOT provision or flash: an RNode arrives already knowing what
// it is, which is most of the point.
func Open(device string, s Settings) (*Radio, error) {
	p, err := serial.Open(device, &serial.Mode{BaudRate: 115200})
	if err != nil {
		return nil, fmt.Errorf("rnode: %w", err)
	}
	if err := p.SetReadTimeout(100 * time.Millisecond); err != nil {
		p.Close()
		return nil, err
	}
	// The modem's limit, NOT MTUFor: capping by airtime was measured worse.
	r := &Radio{port: p, set: s, mtu: MaxFrame, inbox: make(chan []byte, 64),
		stop: make(chan struct{}), rxFrames: map[byte]int{}}

	r.wg.Add(1)
	go r.readLoop()

	// Opening the serial port RESETS the MCU on these boards, so configuring
	// immediately talks to a device that is still booting. The reference
	// driver waits here for the same reason.
	time.Sleep(openSettle)

	if err := r.configure(); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

// configure writes the PHY and turns the radio on, in the order the firmware
// expects: every parameter first, then the state. Setting the state first
// starts a radio on whatever it happened to hold.
func (r *Radio) configure() error {
	type step struct {
		cmd  byte
		data []byte
		what string
	}
	f, b := r.set.FrequencyHz, r.set.BandwidthHz
	for _, st := range []step{
		{cmdFrequency, be32(f), "frequency"},
		{cmdBandwidth, be32(b), "bandwidth"},
		{cmdTXPower, []byte{r.set.TXPowerDBm}, "tx power"},
		{cmdSF, []byte{r.set.SpreadingF}, "spreading factor"},
		{cmdCR, []byte{r.set.CodingRate}, "coding rate"},
		// Zero means "no airtime limit". The firmware keeps these across
		// reboots, so a stale non-zero value would throttle or block
		// transmission with no error anywhere — set them explicitly rather
		// than inherit whatever the board was last told.
		{cmdSTALock, []byte{0, 0}, "short-term airtime limit"},
		{cmdLTALock, []byte{0, 0}, "long-term airtime limit"},
		{cmdRadioState, []byte{radioOn}, "radio state"},
	} {
		if err := r.writeKISS(st.cmd, st.data); err != nil {
			return fmt.Errorf("rnode: setting %s: %w", st.what, err)
		}
		// The firmware applies these one at a time and a burst can outrun it.
		time.Sleep(60 * time.Millisecond)
	}

	// Confirm rather than assume. Asking costs one frame and turns the most
	// confusing possible failure — a radio that is silently off — into a
	// sentence at Open.
	if err := r.writeKISS(cmdRadioState, []byte{radioAsk}); err != nil {
		return err
	}
	time.Sleep(400 * time.Millisecond)

	r.mu.Lock()
	saw, on := r.sawState, r.radioOn
	r.mu.Unlock()
	if saw && !on {
		return ErrRadioWillNotStart
	}
	return nil
}

// MTU reports what one frame carries, per the RadioDatagram contract:
// EXCLUDING carrier framing, INCLUDING our own header and MAC.
func (r *Radio) MTU() int { return r.mtu }

// Send hands one frame to the modem.
//
// dst is IGNORED and that is stated rather than hidden: RNode is a plain
// broadcast modem with no addressing of its own. Everything that needs to
// reach one peer is addressed INSIDE our frame, which is where addressing
// belonged all along — Meshtastic's node numbers were a second, redundant
// scheme we had to bridge.
func (r *Radio) Send(ctx context.Context, _ radiotransfer.RadioAddress, frame []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(frame) > r.mtu {
		return fmt.Errorf("rnode: a frame of %d bytes exceeds the %d-byte MTU "+
			"(%s of air at SF%d/%d kHz)", len(frame), r.mtu,
			r.set.Airtime(r.mtu).Round(time.Millisecond/10), r.set.SpreadingF,
			r.set.BandwidthHz/1000)
	}
	r.mu.Lock()
	closed, e := r.closed, r.err
	r.mu.Unlock()
	if closed {
		return errors.New("rnode: the radio is closed")
	}
	if e != nil {
		return e
	}
	if err := r.writeKISS(cmdData, frame); err != nil {
		return err
	}
	// Advance the queue model ONLY on acceptance: a frame the port refused
	// occupies no air. max(now, prev) because an idle queue does not owe
	// time backwards.
	r.mu.Lock()
	start := time.Now()
	if r.estTxEnd.After(start) {
		start = r.estTxEnd
	}
	r.estTxEnd = start.Add(r.FrameAirtime(len(frame)))
	r.mu.Unlock()
	return nil
}

// FrameAirtime prices one frame, per radiotransfer.AirtimeModel: the PHY
// model plus the measured margin.
//
// The margin is real and measured, not padding: wall clock on two Heltec v3
// ran 0.3-0.7 s above the pure symbol arithmetic (serial transfer, firmware
// handling, host scheduling), and this number must be conservative — an
// optimistic airtime model rebuilds the exact queue it exists to prevent.
func (r *Radio) FrameAirtime(n int) time.Duration {
	return r.set.Airtime(n) + txGuard
}

// txGuard is the measured gap between modelled air and observed wall clock.
const txGuard = 700 * time.Millisecond

// EstimatedTxEnd reports when the modem's queue is expected to drain, per
// radiotransfer.AirtimeModel.
func (r *Radio) EstimatedTxEnd() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.estTxEnd
}

// Receive waits for the next frame the modem heard.
//
// The src is nil: RNode reports no sender, because a LoRa frame carries none.
// Anything above that needs to know WHO must read it out of the frame and
// check a signature — which is what this project's card and peer link already
// do, and is the honest place for it.
func (r *Radio) Receive(ctx context.Context) (radiotransfer.RadioAddress, []byte, error) {
	select {
	case b := <-r.inbox:
		return nil, b, nil
	case <-r.stop:
		return nil, nil, errors.New("rnode: the radio is closed")
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

// Credit is UNKNOWN, and saying so is the honest answer.
//
// RNode reports queue state asynchronously and only on request; until this
// driver tracks it, claiming a number would be inventing one. transports
// already has a vocabulary for exactly this — Known:false means "I cannot
// say", which is different from "nothing right now" and is treated as
// permission to send. (An earlier version of this method said all of that
// and then returned Known:true with unlimited credit — the comment and the
// code disagreeing about which vocabulary was in use.)
//
// What this driver CAN say honestly is time, and it says it through
// radiotransfer.AirtimeModel instead.
func (r *Radio) Credit() transports.Credit {
	return transports.Credit{Known: false}
}

// Closed reports whether this radio is finished, and with what error — the
// liveness a link adopter needs, in the same shape the supervised Meshtastic
// link answers it.
func (r *Radio) Closed() (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed, r.err
}

// Refused reports frames the modem would not queue.
func (r *Radio) Refused() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refused
}

// Close stops the reader and turns the radio off, so a board left plugged in
// is not transmitting for a process that has exited.
func (r *Radio) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()

	_ = r.writeKISS(cmdRadioState, []byte{radioOff})
	close(r.stop)
	err := r.port.Close()
	r.wg.Wait()
	return err
}

// readLoop is the ONE reader of the port, and it un-escapes KISS as it goes.
func (r *Radio) readLoop() {
	defer r.wg.Done()
	buf := make([]byte, 1024)
	var frame []byte
	inFrame, haveCmd, escaped := false, false, false
	var cmd byte

	for {
		select {
		case <-r.stop:
			return
		default:
		}
		n, err := r.port.Read(buf)
		if err != nil {
			r.mu.Lock()
			if !r.closed {
				r.err = err
			}
			r.mu.Unlock()
			return
		}
		r.mu.Lock()
		r.rxBytes += n
		r.mu.Unlock()

		for _, c := range buf[:n] {
			switch {
			case c == fend:
				if inFrame && haveCmd {
					r.deliver(cmd, frame)
				}
				inFrame, haveCmd, escaped, frame, cmd = true, false, false, nil, 0
				// The next byte is the command; a zero-length frame between
				// two FENDs is a keepalive and is simply dropped above.
				continue
			case !inFrame:
				continue
			case escaped:
				switch c {
				case tfend:
					c = fend
				case tfesc:
					c = fesc
				}
				escaped = false
			case c == fesc:
				escaped = true
				continue
			}
			// The first byte after FEND is the command. It must be tracked by
			// its own flag and NEVER by testing cmd against zero: cmdData IS
			// zero, so a "cmd == 0 means not read yet" test eats the payload
			// of every data frame until it happens to meet a non-zero byte.
			// That defect delivered 0 of 30 frames while refusing nothing —
			// the exact signature of a dead radio, produced entirely in
			// software.
			if !haveCmd {
				cmd, haveCmd = c, true
				continue
			}
			frame = append(frame, c)
		}
	}
}

func (r *Radio) deliver(cmd byte, frame []byte) {
	r.mu.Lock()
	r.rxFrames[cmd]++
	if cmd == cmdError && len(frame) > 0 {
		r.lastErr = frame[0]
	}
	if cmd == cmdRadioState && len(frame) > 0 {
		r.radioOn = frame[0] == radioOn
		r.sawState = true
	}
	r.mu.Unlock()

	switch cmd {
	case cmdData:
		b := append([]byte(nil), frame...)
		select {
		case r.inbox <- b:
		default: // nobody is reading fast enough; the layer above repairs
		}
	case cmdError:
		if len(frame) > 0 {
			r.mu.Lock()
			switch frame[0] {
			case errQueueFull:
				r.refused++
			case errTXFailed, errModemTimeout:
				r.refused++
			}
			r.mu.Unlock()
		}
	}
}

// writeKISS frames and escapes one command.
func (r *Radio) writeKISS(cmd byte, data []byte) error {
	out := make([]byte, 0, len(data)+8)
	out = append(out, fend, cmd)
	for _, c := range data {
		switch c {
		case fend:
			out = append(out, fesc, tfend)
		case fesc:
			out = append(out, fesc, tfesc)
		default:
			out = append(out, c)
		}
	}
	out = append(out, fend)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("rnode: the radio is closed")
	}
	_, err := r.port.Write(out)
	return err
}

func be32(v uint32) []byte {
	return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

// The claims this file exists to check, checked at compile time.
var (
	_ radiotransfer.RadioDatagram = (*Radio)(nil)
	_ radiotransfer.AirtimeModel  = (*Radio)(nil)
)

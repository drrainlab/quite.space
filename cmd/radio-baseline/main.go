// Command radio-baseline measures what a Meshtastic carrier actually
// delivers, with nothing of Quiet Spaces on top of it.
//
// This exists because of a gate, and the gate exists because of nine days.
// Two radios two metres apart, hearing each other directly at 0 hops and 6 dB
// SNR, never assembled a single Quiet event. Three correct fixes shipped —
// backpressure, want_ack, one-packet messages — and none of them was
// sufficient. Every measurement in that period came from a full node: sync
// engine, fragmentation, reassembly, relay policy and carrier all at once, so
// a number like "15% arrived" could not say WHICH of those lost it.
//
// So this sends ONE SELF-CONTAINED PACKET at a time, numbered, and counts how
// many reach the application on the other radio. Nothing is fragmented,
// nothing is reassembled, nothing is retried by us. What comes out is a
// property of the carrier and of nothing else:
//
//	if single-frame delivery is high and events still do not arrive,
//	    the fault is ABOVE this — fragmentation, reassembly, sync
//	if single-frame delivery is low,
//	    building a reliability layer on top would be building on sand
//
// That is the whole question the Stage A gate asks, and it is a yes or a no.
//
//	radio-baseline --a serial:/dev/cu.usbserial-0001 --channel-a 2 \
//	               --b serial:/dev/cu.usbmodem24EC4A307B541 --channel-b 1 \
//	               --count 100 --interval 3s --reliable
//
// Exit codes: 0 the run met the gate · 1 it did not · 2 it could not be run.
// The 1/2 split matters: "the carrier loses packets" and "the harness never
// measured anything" are different answers, and only one of them is evidence.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

// magic marks a packet as one of ours. Two radios on a shared channel hear
// everybody; without a marker, somebody else's traffic on our portnum would
// be counted as a delivery.
var magic = [4]byte{'q', 'p', 'b', 'l'}

// gateDelivery is the Stage A exit criterion, written down where the run can
// check itself rather than left to whoever reads the output. It may be
// revised after the first honest run — but the gate has to end in yes or no,
// and a threshold nobody wrote down ends in neither.
const gateDelivery = 0.98

type result struct {
	seq  uint32
	sent time.Time
	got  time.Time // zero means it never arrived
}

func main() { os.Exit(run()) }

func run() int {
	var (
		aTarget  = flag.String("a", "", "the sending radio (serial:/dev/… or tcp:host)")
		bTarget  = flag.String("b", "", "the receiving radio")
		chA      = flag.Uint("channel-a", 0, "sender's channel index")
		chB      = flag.Uint("channel-b", 0, "receiver's channel index")
		count    = flag.Int("count", 100, "packets to send")
		interval = flag.Duration("interval", 3*time.Second, "gap between packets")
		size     = flag.Int("size", 32, "payload bytes (must stay one frame)")
		reliable = flag.Bool("reliable", false, "ask the firmware to retransmit (want_ack)")
		settle   = flag.Duration("settle", 30*time.Second, "how long to keep listening after the last packet")
		label    = flag.String("label", "", "a name for this run, printed with the result")
	)
	flag.Parse()
	if *aTarget == "" || *bTarget == "" {
		flag.Usage()
		return 2
	}
	if *size < 12 || *size > 200 {
		fmt.Fprintln(os.Stderr, "--size must be 12..200: below that the marker and "+
			"sequence number do not fit, above it the packet stops being one frame "+
			"and the run stops measuring what it claims to")
		return 2
	}

	a, err := open(*aTarget, meshtastic.Options{
		Channel: uint32(*chA), Reliable: *reliable,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "sender:", err)
		return 2
	}
	defer a.Close()
	b, err := open(*bTarget, meshtastic.Options{Channel: uint32(*chB)})
	if err != nil {
		fmt.Fprintln(os.Stderr, "receiver:", err)
		return 2
	}
	defer b.Close()

	if code := describe("sender", a, uint32(*chA)); code != 0 {
		return code
	}
	if code := describe("receiver", b, uint32(*chB)); code != 0 {
		return code
	}

	fmt.Printf("\n%s%d packets, %d bytes each, %s apart, want_ack=%v\n",
		labelPrefix(*label), *count, *size, *interval, *reliable)
	fmt.Println("one packet per message: nothing here is fragmented, so a loss " +
		"is the carrier's and\nnot ours.")

	// Drain whatever the receiver was already holding. Anything from before
	// the run started is not evidence about the run.
	b.Poll()

	// Listen CONTINUOUSLY, in its own goroutine.
	//
	// Polling once per send loop measured latency with a three-second ruler:
	// every arrival was timestamped at the next send, so the reported p50 was
	// the send interval and nothing else. A number that is really a
	// restatement of the caller's own flag is worse than no number, because
	// it looks like a measurement.
	rx := newListener(b)
	defer rx.stop()

	sent := make([]result, 0, *count)
	start := time.Now()

	for i := range *count {
		seq := uint32(i)
		if err := a.Send(payload(seq, *size)); err != nil {
			fmt.Fprintf(os.Stderr, "\nsend %d failed: %v\n", seq, err)
			// A refusal is DATA, not an abort: the whole point is to find out
			// whether the carrier refuses, and how often.
		}
		sent = append(sent, result{seq: seq, sent: time.Now()})

		if (i+1)%10 == 0 || i+1 == *count {
			fmt.Printf("  sent %3d/%d · arrived %3d · %s\n",
				i+1, *count, rx.count(), carrierLine(a))
		}
		if i+1 < *count {
			time.Sleep(*interval)
		}
	}

	// The last packets are still in the air when the sending stops. Waiting
	// is not padding: an impatient run reports losses that were merely late,
	// which is the same lie in the opposite direction.
	fmt.Printf("\nlistening for %s more…\n", *settle)
	time.Sleep(*settle)

	seen := rx.snapshot()
	for i := range sent {
		if at, ok := seen[sent[i].seq]; ok {
			sent[i].got = at
		}
	}
	return report(*label, sent, a, b, time.Since(start), *reliable)
}

// open retries a handshake that produced only silence.
//
// Boards with native USB answer a short session perhaps one time in three
// (issue #146) — the cause is unknown and it is not the carrier's fault, but
// a measurement run that dies on it measures nothing at all. A device that
// said NOTHING is worth asking again; one that is busy, or behind a path that
// does not exist, is not, and retrying those would turn a typo into a wait.
func open(target string, opts meshtastic.Options) (*meshtastic.Radio, error) {
	var err error
	for attempt := range 8 {
		var r *meshtastic.Radio
		r, err = meshtastic.Open(target, opts)
		if err == nil {
			if attempt > 0 {
				fmt.Printf("  (%s answered on attempt %d)\n", target, attempt+1)
			}
			return r, nil
		}
		if !meshtastic.SilentHandshake(err) {
			return nil, err
		}
		time.Sleep(700 * time.Millisecond)
	}
	return nil, err
}

func labelPrefix(label string) string {
	if label == "" {
		return ""
	}
	return label + ": "
}

// payload is a marked, numbered, fixed-size packet. The filler is
// deterministic rather than random so two runs put the same bytes on the air
// and a difference in the result is a difference in the radio.
func payload(seq uint32, size int) []byte {
	p := make([]byte, size)
	copy(p, magic[:])
	binary.BigEndian.PutUint32(p[4:], seq)
	for i := 8; i < size; i++ {
		p[i] = byte(i)
	}
	return p
}

// listener records arrivals as they happen, at a cadence fine enough that the
// timestamp is about the radio rather than about this loop.
type listener struct {
	mu   sync.Mutex
	seen map[uint32]time.Time
	done chan struct{}
}

func newListener(b *meshtastic.Radio) *listener {
	l := &listener{seen: map[uint32]time.Time{}, done: make(chan struct{})}
	go func() {
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-l.done:
				return
			case <-t.C:
				now := time.Now()
				for _, p := range b.Poll() {
					if len(p) < 8 || string(p[:4]) != string(magic[:]) {
						continue // somebody else's traffic on our portnum
					}
					seq := binary.BigEndian.Uint32(p[4:8])
					l.mu.Lock()
					if _, already := l.seen[seq]; !already {
						l.seen[seq] = now
					}
					l.mu.Unlock()
				}
			}
		}
	}()
	return l
}

func (l *listener) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.seen)
}

func (l *listener) snapshot() map[uint32]time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[uint32]time.Time, len(l.seen))
	for k, v := range l.seen {
		out[k] = v
	}
	return out
}

func (l *listener) stop() { close(l.done) }

// describe prints what each radio says about itself, and refuses to measure
// against a configuration that cannot produce a meaningful number.
//
// A run against a muted transmitter reports 0% and looks exactly like a
// carrier that loses everything — which is precisely the confusion that cost
// nine days. It is cheaper to refuse here than to explain the result later.
func describe(role string, r *meshtastic.Radio, channel uint32) int {
	cfg := r.Config()
	fmt.Printf("\n%s %s\n", role, targetLine(cfg))
	if cfg.LoRa == nil {
		fmt.Fprintf(os.Stderr, "the %s did not report its LoRa settings, so this "+
			"run could not say what it measured\n", role)
		return 2
	}
	if !cfg.LoRa.TxEnabled && role == "sender" {
		fmt.Fprintln(os.Stderr, "\nthe sender's TRANSMITTER IS OFF. It would report "+
			"0% delivery, which is\nindistinguishable from a carrier that loses "+
			"everything. Turn it on first:\n  terminal radio region --tx on --port …")
		return 2
	}
	if ch, ok := cfg.Channel(int(channel)); ok {
		fmt.Printf("  channel %d %q · key %s\n", channel, ch.Name, ch.KeyClass)
	} else {
		fmt.Fprintf(os.Stderr, "the %s has no channel at index %d — it would "+
			"transmit somewhere nobody is listening\n", role, channel)
		return 2
	}
	return 0
}

func targetLine(cfg meshtastic.NodeConfig) string {
	s := fmt.Sprintf("%08x", cfg.NodeNum)
	if cfg.LoRa != nil {
		s += fmt.Sprintf(" · %s · %s · hop %d · tx %v",
			cfg.LoRa.RegionName(), cfg.LoRa.PresetName(), cfg.LoRa.HopLimit,
			cfg.LoRa.TxEnabled)
	}
	if cfg.Device != nil {
		s += " · rebroadcast " + cfg.Device.RebroadcastName()
	}
	return s
}

func carrierLine(a *meshtastic.Radio) string {
	free, maxlen, refused, known := a.QueueState()
	acked, gaveUp, outstanding := a.Delivery()
	q := "queue unknown"
	if known {
		q = fmt.Sprintf("queue %d/%d free", free, maxlen)
	}
	return fmt.Sprintf("%s · refused %d · acked %d · gave up %d · outstanding %d",
		q, refused, acked, gaveUp, outstanding)
}

func report(label string, sent []result, a, b *meshtastic.Radio,
	elapsed time.Duration, reliable bool) int {
	arrived := 0
	var lat []time.Duration
	var lost []uint32
	for _, s := range sent {
		if s.got.IsZero() {
			lost = append(lost, s.seq)
			continue
		}
		arrived++
		lat = append(lat, s.got.Sub(s.sent))
	}
	ratio := float64(arrived) / float64(len(sent))
	free, maxlen, refused, queueKnown := a.QueueState()
	acked, gaveUp, outstanding := a.Delivery()

	fmt.Printf("\n%s\n", divider)
	if label != "" {
		fmt.Printf("run          %s\n", label)
	}
	fmt.Printf("delivery     %d/%d = %.1f%%\n", arrived, len(sent), ratio*100)
	if len(lat) > 0 {
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		fmt.Printf("latency      p50 %s · p95 %s · max %s\n",
			round(lat[len(lat)/2]), round(lat[p95(len(lat))]), round(lat[len(lat)-1]))
	}
	fmt.Printf("refused      %d   (packets the FIRMWARE would not queue)\n", refused)
	if queueKnown {
		fmt.Printf("queue        %d of %d free at the end\n", free, maxlen)
	} else {
		fmt.Println("queue        never reported")
	}
	if reliable {
		fmt.Printf("firmware     acked %d · gave up %d · outstanding %d\n",
			acked, gaveUp, outstanding)
		fmt.Println("             an ack here means a NEIGHBOUR REBROADCAST the packet.")
		fmt.Println("             It is not proof the peer received it, and is never")
		fmt.Println("             reported upward as delivery.")
	}
	fmt.Printf("elapsed      %s\n", elapsed.Round(time.Second))
	if len(lost) > 0 {
		fmt.Printf("lost         %s\n", runsOf(lost))
		fmt.Println("             consecutive losses point at bursts — interference or a")
		fmt.Println("             busy band; scattered ones point at per-packet loss.")
		fmt.Println("             The two want different fixes, so the pattern is printed")
		fmt.Println("             rather than only the total.")
	}
	fmt.Println(divider)

	switch {
	case ratio >= gateDelivery && refused == 0:
		fmt.Printf("\nPASSES the Stage A gate (≥ %.0f%% single-frame delivery, "+
			"nothing refused).\n", gateDelivery*100)
		fmt.Println("If whole events still do not arrive, the fault is ABOVE the " +
			"carrier —\nfragmentation, reassembly or sync — and that is where to look.")
		return 0
	case refused > 0:
		fmt.Printf("\nDOES NOT PASS: the firmware refused %d packet(s). The sender "+
			"is offering\nmore than the radio will take, and no reliability layer "+
			"above can fix\nthat — it has to be paced.\n", refused)
		return 1
	default:
		fmt.Printf("\nDOES NOT PASS: %.1f%% delivered, gate is %.0f%%.\n",
			ratio*100, gateDelivery*100)
		fmt.Println("The carrier is losing single, self-contained packets. Building " +
			"fragmentation\nand selective repeat on top of this would be building " +
			"on sand: fix the\ncarrier first — the band, the frequency slot, the " +
			"preset, or the segment.")
		return 1
	}
}

const divider = "──────────────────────────────────────────────────────────────"

func p95(n int) int {
	i := (n * 95) / 100
	if i >= n {
		i = n - 1
	}
	return i
}

func round(d time.Duration) time.Duration { return d.Round(10 * time.Millisecond) }

// runsOf collapses a list of lost sequence numbers into ranges, so a burst
// reads as one thing instead of forty.
func runsOf(lost []uint32) string {
	out, i := "", 0
	for i < len(lost) {
		j := i
		for j+1 < len(lost) && lost[j+1] == lost[j]+1 {
			j++
		}
		if out != "" {
			out += ", "
		}
		if j > i {
			out += fmt.Sprintf("%d-%d", lost[i], lost[j])
		} else {
			out += fmt.Sprintf("%d", lost[i])
		}
		i = j + 1
	}
	return out
}

// Stage B over RNode: WHOLE TRANSFERS, not single frames.
//
// Stage A asked whether the carrier moves a packet. This asks the only
// question a person actually cares about: did the MESSAGE arrive, complete
// and byte-for-byte, over a link that loses things. Fragmentation, the
// selective-repeat window, SACKs and reassembly are all under test here, and
// the headline is Stats.CompleteTransferRate — which the transfer layer
// already computes, because packet delivery is a property of the carrier
// while transfer completion is a property of the system.
//
// It builds its endpoints exactly the way node/mesh.go does — radiotransfer.
// Wrap over a RadioDatagram — so what is measured is production's shape with
// a different carrier underneath, not a harness's own idea of one.
package main

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/transports/radiotransfer"
	"github.com/drrainlab/quiet_places/transports/rnode"
)

func main() {
	var (
		aDev = flag.String("a", "", "the sending radio")
		bDev = flag.String("b", "", "the receiving radio")
		reps = flag.Int("reps", 2, "transfers per size")
		gap  = flag.Duration("gap", 0, "override FrameGap (0 = production default)")
		freq = flag.Uint("freq", 868950000, "frequency in Hz")
		bw   = flag.Uint("bw", 250000, "bandwidth in Hz")
		sf   = flag.Uint("sf", 11, "spreading factor")
		cr   = flag.Uint("cr", 5, "coding rate")
		txp  = flag.Uint("txp", 20, "tx power dBm")
		only = flag.Int("size", 0, "test only this payload size")
		win  = flag.Int("window", 0, "override Window (0 = production default)")
		ack  = flag.Duration("ack", 0, "override AckTimeout (0 = production default)")
		btwn = flag.Duration("between", 0, "quiet time between transfers")
		trc  = flag.String("trace", "", "write a structured transfer trace here")
	)
	flag.Parse()
	if *aDev == "" || *bDev == "" {
		fmt.Println("both -a and -b are required")
		os.Exit(2)
	}

	phy := rnode.Settings{FrequencyHz: uint32(*freq), BandwidthHz: uint32(*bw),
		SpreadingF: uint8(*sf), CodingRate: uint8(*cr), TXPowerDBm: uint8(*txp)}

	a, err := rnode.Open(*aDev, phy)
	if err != nil {
		fmt.Println("sender:", err)
		os.Exit(1)
	}
	defer a.Close()
	b, err := rnode.Open(*bDev, phy)
	if err != nil {
		fmt.Println("receiver:", err)
		os.Exit(1)
	}
	defer b.Close()

	// One segment seed, so both sides derive the same transfer authentication
	// key. On a real segment this comes from the descriptor; here it is fixed
	// so a run is reproducible.
	seed := bytes.Repeat([]byte("quiet-stage-b-seed"), 4)
	key, err := radiotransfer.DeriveTransferKey(seed, radiotransfer.KDFVersion)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	var opts radiotransfer.EndpointOptions
	if *gap > 0 {
		opts.Limits.FrameGap = *gap
	}
	if *win > 0 {
		opts.Limits.Window = *win
	}
	if *ack > 0 {
		opts.Limits.AckTimeout = *ack
	}

	// One trace, both sides, one clock. Two files would have to be correlated
	// afterwards, and the whole question is which side knew what WHEN.
	var traceOut *os.File
	if *trc != "" {
		f, err := os.Create(*trc)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		traceOut = f
		defer traceOut.Close()
	}
	var traceMu sync.Mutex
	tracer := func(side string) radiotransfer.Tracer {
		if traceOut == nil {
			return nil
		}
		return func(ev radiotransfer.TraceEvent) {
			traceMu.Lock()
			fmt.Fprintf(traceOut, "%-4s %s\n", side, ev)
			traceMu.Unlock()
		}
	}

	sendOpts, recvOpts := opts, opts
	sendOpts.Trace = tracer("SEND")
	recvOpts.Trace = tracer("RECV")

	send, err := radiotransfer.Wrap(a, key, sendOpts)
	if err != nil {
		fmt.Println("sender endpoint:", err)
		os.Exit(1)
	}
	defer send.Close()
	recv, err := radiotransfer.Wrap(b, key, recvOpts)
	if err != nil {
		fmt.Println("receiver endpoint:", err)
		os.Exit(1)
	}
	defer recv.Close()

	effGap := *gap
	if effGap == 0 {
		effGap = radiotransfer.DefaultLimits().FrameGap
	}
	fmt.Printf("PHY  %.3f MHz · %d kHz · SF%d · CR4/%d · %d dBm\n",
		float64(phy.FrequencyHz)/1e6, phy.BandwidthHz/1000, phy.SpreadingF,
		phy.CodingRate, phy.TXPowerDBm)
	effWin, effAck := *win, *ack
	if effWin == 0 {
		effWin = radiotransfer.DefaultLimits().Window
	}
	if effAck == 0 {
		effAck = radiotransfer.DefaultLimits().AckTimeout
	}
	fmt.Printf("MTU  %d bytes · ceiling %d · FrameGap %s · Window %d · AckTimeout %s\n\n",
		a.MTU(), send.Capabilities().MaxPayload, effGap, effWin, effAck)

	arrived := make(chan []byte, 64)
	go func() {
		for {
			for _, m := range recv.Poll() {
				arrived <- m
			}
			time.Sleep(150 * time.Millisecond)
		}
	}()

	sizes := []int{300, 700, 1500, 3200, 6600}
	if *only > 0 {
		sizes = []int{*only}
	}
	// Every payload ever sent, so a LATE arrival can be told from CORRUPTION.
	// Without this a transfer that times out and then lands during the NEXT
	// one of the same size looks byte-for-byte like a reassembly defect — and
	// "the transfer layer corrupts data" is not a claim to make on evidence
	// that also fits "it was merely slow".
	var history [][]byte
	type result struct {
		size, frames, ok, tried int
		times                   []time.Duration
	}
	var results []result

	for _, size := range sizes {
		r := result{size: size}
		for i := range *reps {
			// A transfer does not end for BOTH sides at the same instant: when
			// the sender is done, the peer may still be finishing its own
			// half. Starting the next one into that is measurably expensive.
			if *btwn > 0 && len(history) > 0 {
				time.Sleep(*btwn)
			}
			msg := payload(size, i)
			history = append(history, msg)
			before := send.Stats()

			start := time.Now()
			if err := send.Send(msg); err != nil {
				fmt.Printf("  %5d B  #%d  send: %v\n", size, i, err)
				r.tried++
				continue
			}
			r.tried++

			// Generous: a transfer may spend several repair rounds, and the
			// point is to learn WHETHER it completes, not to cut it short and
			// call a slow success a failure.
			budget := time.Duration(size/300+2)*8*effGap + 90*time.Second
			got, late, err := await(arrived, msg, history, budget)
			took := time.Since(start)
			after := send.Stats()
			used := after.FramesOut - before.FramesOut

			switch {
			case err != nil:
				fmt.Printf("  %5d B  #%d  %-9s  %2d frames  %s\n",
					size, i, "LOST", used, took.Round(time.Second))
			case !got:
				fmt.Printf("  %5d B  #%d  %-9s  %2d frames  %s  bytes differ from "+
					"anything sent\n", size, i, "CORRUPT", used,
					took.Round(time.Second))
			default:
				r.ok++
				r.times = append(r.times, took)
				note := ""
				if late > 0 {
					note = fmt.Sprintf("  (+%d late from an earlier transfer)", late)
				}
				fmt.Printf("  %5d B  #%d  %-9s  %2d frames  %s%s\n",
					size, i, "complete", used, took.Round(100*time.Millisecond), note)
			}
			if used > r.frames {
				r.frames = used
			}
		}
		results = append(results, r)
	}

	st := send.Stats()
	fmt.Println("\n──────────────────────────────────────────────────────────────")
	fmt.Printf("%-9s %-8s %-10s %-10s\n", "size", "frames", "complete", "median")
	totalOK, totalTried := 0, 0
	for _, r := range results {
		med := "—"
		if len(r.times) > 0 {
			sort.Slice(r.times, func(i, j int) bool { return r.times[i] < r.times[j] })
			med = r.times[len(r.times)/2].Round(100 * time.Millisecond).String()
		}
		fmt.Printf("%-9d %-8d %d/%-8d %-10s\n", r.size, r.frames, r.ok, r.tried, med)
		totalOK += r.ok
		totalTried += r.tried
	}
	fmt.Println("──────────────────────────────────────────────────────────────")
	fmt.Printf("complete_transfer_rate   %d/%d = %.1f%%   (layer says %.1f%%)\n",
		totalOK, totalTried, pct(totalOK, totalTried),
		st.CompleteTransferRate()*100)
	fmt.Printf("frames out %d · gave up %d · refused %d · frames in %d\n",
		st.FramesOut, st.GaveUp, st.Refused, st.FramesIn)

	// The gate, stated as pass or fail rather than left to impression. A
	// transfer that arrives CORRUPT is worse than one that never arrives, so
	// any mismatch fails outright regardless of the rate.
	fmt.Println()
	switch {
	case totalOK == totalTried:
		fmt.Println("PASSES Stage B: every transfer arrived complete and byte-exact.")
	default:
		fmt.Printf("DOES NOT PASS: %d of %d transfers did not arrive intact.\n",
			totalTried-totalOK, totalTried)
	}
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}

// await waits for the exact message, ignoring anything else on the segment.
func await(ch <-chan []byte, want []byte, history [][]byte,
	budget time.Duration) (ok bool, late int, err error) {
	deadline := time.After(budget)
	for {
		select {
		case got := <-ch:
			if bytes.Equal(got, want) {
				return true, late, nil
			}
			// A message we sent EARLIER that is only arriving now. Slow, and
			// worth counting, but not corruption.
			stale := false
			for _, h := range history {
				if bytes.Equal(got, h) {
					stale = true
					break
				}
			}
			if stale {
				late++
				continue
			}
			// Bytes matching nothing that was ever sent. THAT is a reassembly
			// defect, and the only thing this gate should ever call corrupt.
			return false, late, nil
		case <-deadline:
			return false, late, fmt.Errorf("nothing arrived within %s", budget)
		}
	}
}

// payload is deterministic and incompressible-ish, so a reassembly that
// silently repeats or reorders a chunk cannot compare equal by luck.
func payload(n, seq int) []byte {
	out := make([]byte, 0, n)
	h := sha256.Sum256([]byte{byte(n), byte(n >> 8), byte(seq)})
	for len(out) < n {
		h = sha256.Sum256(h[:])
		out = append(out, h[:]...)
	}
	return out[:n]
}

// Stage A over RNode: the same measurement, the other carrier.
//
// It deliberately mirrors cmd/radio-baseline — same count, same interval, same
// single-frame payload, same gate — because the whole point is a number that
// can be put beside the Meshtastic one without an argument about method. What
// differs is the driver underneath and nothing else.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/drrainlab/quiet_places/transports/rnode"
)

func main() {
	var (
		aDev  = flag.String("a", "", "the sending radio (serial device)")
		bDev  = flag.String("b", "", "the receiving radio (serial device)")
		count = flag.Int("count", 30, "packets to send")
		gap   = flag.Duration("interval", 3*time.Second, "gap between packets")
		set   = flag.Duration("settle", 25*time.Second, "listen this long after the last packet")
		size  = flag.Int("size", 32, "payload bytes (must stay one frame)")
		freq  = flag.Uint("freq", 868950000, "frequency in Hz (RU: 868.7-869.2 MHz)")
		bw    = flag.Uint("bw", 250000, "bandwidth in Hz")
		sf    = flag.Uint("sf", 11, "spreading factor")
		cr    = flag.Uint("cr", 5, "coding rate denominator")
		txp   = flag.Uint("txp", 20, "tx power in dBm (RU: max 20)")
		label = flag.String("label", "", "a name for this run")
	)
	flag.Parse()
	if *aDev == "" || *bDev == "" {
		fmt.Println("both -a and -b are required")
		os.Exit(2)
	}

	s := rnode.Settings{FrequencyHz: uint32(*freq), BandwidthHz: uint32(*bw),
		SpreadingF: uint8(*sf), CodingRate: uint8(*cr), TXPowerDBm: uint8(*txp)}

	fmt.Printf("PHY: %.3f MHz · %d kHz · SF%d · CR4/%d · %d dBm\n",
		float64(s.FrequencyHz)/1e6, s.BandwidthHz/1000, s.SpreadingF,
		s.CodingRate, s.TXPowerDBm)

	a, err := rnode.Open(*aDev, s)
	if err != nil {
		fmt.Println("sender:", err)
		os.Exit(1)
	}
	defer a.Close()
	b, err := rnode.Open(*bDev, s)
	if err != nil {
		fmt.Println("receiver:", err)
		os.Exit(1)
	}
	defer b.Close()

	// Both radios need a moment after the PHY is written before the first
	// frame; sending into a modem that is still applying settings measures the
	// settling, not the air.
	time.Sleep(2 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type arrival struct {
		seq int
		at  time.Time
	}
	got := make(chan arrival, *count*2)
	go func() {
		for {
			_, frame, err := b.Receive(ctx)
			if err != nil {
				return
			}
			// The marker keeps a neighbour's traffic out of the result.
			if len(frame) >= len(marker)+2 &&
				string(frame[:len(marker)]) == marker {
				got <- arrival{seq: int(frame[len(marker)])<<8 |
					int(frame[len(marker)+1]), at: time.Now()}
			}
		}
	}()

	sentAt := make(map[int]time.Time, *count)
	payload := make([]byte, *size)
	copy(payload, marker)

	fmt.Printf("\nsending %d single frames, %s apart\n", *count, *gap)
	for i := range *count {
		payload[len(marker)] = byte(i >> 8)
		payload[len(marker)+1] = byte(i)
		sentAt[i] = time.Now()
		if err := a.Send(ctx, nil, payload); err != nil {
			fmt.Printf("  send %d: %v\n", i, err)
		}
		if (i+1)%10 == 0 {
			fmt.Printf("  sent %3d/%d · refused %d\n", i+1, *count, a.Refused())
		}
		time.Sleep(*gap)
	}

	fmt.Printf("\nlistening for %s more…\n", *set)
	deadline := time.After(*set)
	arrived := map[int]time.Duration{}
collect:
	for {
		select {
		case g := <-got:
			if t, ok := sentAt[g.seq]; ok {
				if _, dup := arrived[g.seq]; !dup {
					arrived[g.seq] = g.at.Sub(t)
				}
			}
		case <-deadline:
			break collect
		}
	}

	// What the serial side actually saw, on BOTH radios. Without this a zero
	// result cannot be told apart into its three very different causes: the
	// modem never spoke to us, it spoke and reported an error, or frames
	// arrived and were dropped above.
	for _, side := range []struct {
		name string
		r    *rnode.Radio
	}{{"sender", a}, {"receiver", b}} {
		n, frames, lastErr := side.r.Counters()
		fmt.Printf("%-9s serial %6d bytes · frames %v · last error 0x%02x\n",
			side.name, n, frames, lastErr)
	}

	report(*label, *count, arrived, a.Refused())
}

const marker = "QSRN"

func report(label string, count int, arrived map[int]time.Duration, refused int) {
	lat := make([]time.Duration, 0, len(arrived))
	for _, d := range arrived {
		lat = append(lat, d)
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(p float64) time.Duration {
		if len(lat) == 0 {
			return 0
		}
		i := int(float64(len(lat)-1) * p)
		return lat[i].Round(10 * time.Millisecond)
	}
	var lost []int
	for i := range count {
		if _, ok := arrived[i]; !ok {
			lost = append(lost, i)
		}
	}
	ratio := float64(len(arrived)) / float64(count) * 100

	fmt.Println("\n──────────────────────────────────────────────────────────────")
	if label != "" {
		fmt.Printf("run          %s\n", label)
	}
	fmt.Printf("delivery     %d/%d = %.1f%%\n", len(arrived), count, ratio)
	fmt.Printf("latency      p50 %s · p95 %s · max %s\n", pct(0.5), pct(0.95), pct(1))
	fmt.Printf("refused      %d   (frames the MODEM would not queue)\n", refused)
	if len(lost) > 0 {
		fmt.Printf("lost         %v\n", lost)
	}
	fmt.Println("──────────────────────────────────────────────────────────────")

	// The SAME gate as the Meshtastic harness, so the two runs are comparable
	// as pass/fail and not only as percentages.
	switch {
	case refused > 0:
		fmt.Printf("\nDOES NOT PASS: the modem refused %d frame(s).\n", refused)
	case ratio >= 98:
		fmt.Println("\nPASSES the Stage A gate (≥ 98% single-frame delivery, nothing refused).")
	default:
		fmt.Printf("\nDOES NOT PASS: %.1f%% delivered, gate is 98%%.\n", ratio)
	}
}

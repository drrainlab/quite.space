// Command relay-load is the RR-7 load harness: it drives a relay in the
// units the protocol actually has — Put/Collect/Fetch/Probe operations
// per second over pooled connections — never in imaginary "sessions".
// Target numbers are fixed AFTER running this on the real hardware, not
// invented in a plan.
//
//	relay-load steady   --relay ADDR --clients 200 --duration 60s
//	relay-load storm    --relay ADDR --clients 500 --window 60s
//	relay-load probes   --relay ADDR --clients 1000
//
// steady — L-1: every client runs the 2s-cadence loop (jittered) with a
// small Put + a Collect per tick; reports ops/sec, error rate, p95.
// storm  — L-4: all clients connect inside the window (full jitter) and
// run one tick; reports handshake success and time spread.
// probes  — L-7: every client runs one 3-sample probe burst; the relay's
// meter must hold and nothing durable may be written.
package main

import (
	"crypto/rand"
	"fmt"
	"math"
	mrand "math/rand"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	mode := os.Args[1]
	addr := "127.0.0.1:7411"
	clients := 100
	duration := 60 * time.Second
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--relay":
			if i+1 < len(args) {
				addr = args[i+1]
				i++
			}
		case "--clients":
			if i+1 < len(args) {
				clients, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "--duration", "--window":
			if i+1 < len(args) {
				if d, err := time.ParseDuration(args[i+1]); err == nil {
					duration = d
				}
				i++
			}
		}
	}
	switch mode {
	case "steady":
		steady(addr, clients, duration)
	case "storm":
		storm(addr, clients, duration)
	case "probes":
		probes(addr, clients)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println("usage: relay-load steady|storm|probes --relay ADDR --clients N [--duration D]")
}

type stats struct {
	ops   atomic.Int64
	errs  atomic.Int64
	mu    sync.Mutex
	rtts  []time.Duration
	limit int
}

func (s *stats) sample(d time.Duration) {
	s.mu.Lock()
	if len(s.rtts) < 100000 {
		s.rtts = append(s.rtts, d)
	}
	s.mu.Unlock()
}

func (s *stats) report(elapsed time.Duration) {
	s.mu.Lock()
	rtts := append([]time.Duration(nil), s.rtts...)
	s.mu.Unlock()
	sort.Slice(rtts, func(i, j int) bool { return rtts[i] < rtts[j] })
	p := func(q float64) time.Duration {
		if len(rtts) == 0 {
			return 0
		}
		return rtts[int(math.Min(float64(len(rtts)-1), q*float64(len(rtts))))]
	}
	ops, errs := s.ops.Load(), s.errs.Load()
	fmt.Printf("ops=%d errs=%d (%.2f%%) ops/sec=%.1f p50=%v p95=%v p99=%v\n",
		ops, errs, 100*float64(errs)/math.Max(1, float64(ops)),
		float64(ops)/elapsed.Seconds(), p(0.50), p(0.95), p(0.99))
}

// hintFor gives every client its own mailbox so steady traffic looks
// like real traffic (many hints), not one hot key.
func hintFor(i int) []byte {
	h := make([]byte, relay.HintLen)
	h[0], h[1] = byte(i), byte(i>>8)
	return h
}

func steady(addr string, clients int, duration time.Duration) {
	fmt.Printf("L-1 steady: %d clients × 2s cadence for %v against %s\n", clients, duration, addr)
	var st stats
	stop := time.After(duration)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := relay.DialClient(addr)
			if err != nil {
				st.errs.Add(1)
				return
			}
			defer c.Close()
			// Jittered phase, like the real client (RR-2).
			time.Sleep(time.Duration(mrand.Int63n(int64(2 * time.Second))))
			body := make([]byte, 2048)
			_, _ = rand.Read(body)
			cap := make([]byte, relay.CapLen)
			_, _ = rand.Read(cap)
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
				}
				t0 := time.Now()
				if _, err := c.Put(hintFor(i), uint64(time.Now().Unix())+300, body); err != nil {
					st.errs.Add(1)
				}
				st.ops.Add(1)
				if _, err := c.Collect([][]byte{cap}); err != nil {
					st.errs.Add(1)
				}
				st.ops.Add(1)
				st.sample(time.Since(t0))
			}
		}(i)
	}
	wg.Wait()
	st.report(time.Since(start))
}

func storm(addr string, clients int, window time.Duration) {
	fmt.Printf("L-4 reconnect storm: %d clients inside %v against %s\n", clients, window, addr)
	var st stats
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Full jitter across the window — the client discipline the
			// real pool applies after an outage.
			time.Sleep(time.Duration(mrand.Int63n(int64(window))))
			t0 := time.Now()
			c, err := relay.DialClient(addr)
			if err != nil {
				st.errs.Add(1)
				return
			}
			defer c.Close()
			st.ops.Add(1)
			if _, _, err := c.Time(); err != nil {
				st.errs.Add(1)
			}
			st.ops.Add(1)
			st.sample(time.Since(t0))
		}(i)
	}
	wg.Wait()
	st.report(time.Since(start))
}

func probes(addr string, clients int) {
	fmt.Printf("L-7 probe storm: %d clients × 3 probes against %s\n", clients, addr)
	var st stats
	var limited atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := relay.DialClient(addr)
			if err != nil {
				st.errs.Add(1)
				return
			}
			defer c.Close()
			nonce := make([]byte, 8)
			_, _ = rand.Read(nonce)
			for k := 0; k < 3; k++ {
				t0 := time.Now()
				_, err := c.Probe(nonce)
				st.ops.Add(1)
				if err != nil {
					if re, ok := err.(relay.ErrRelay); ok && re.Reason == "rate limited" {
						limited.Add(1) // the meter holding is the SUCCESS here
					} else {
						st.errs.Add(1)
					}
					continue
				}
				st.sample(time.Since(t0))
			}
		}()
	}
	wg.Wait()
	st.report(time.Since(start))
	fmt.Printf("rate-limited probes: %d (the meter holding is expected under storm)\n", limited.Load())
}

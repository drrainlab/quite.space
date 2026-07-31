// Measured relay selection (RR-3). Automatic mode picks not the nearest
// relay by geography but the best available one by the actually measured
// path: application-level RTT over the SAME TLS+wire the traffic will
// use, smoothed by EWMA, guarded by hysteresis.
//
// Honest noise floor: the transport polls its connection on 10–30ms
// ticks, so a single RTT sample is quantized — two relays within ~30ms
// of each other are indistinguishable by design. That is exactly why
// switching demands a ≥25–30% advantage (scoreHysteresis) and a minimum
// stable period, not a few milliseconds of difference.
//
// Everything here writes relays.json (derived runtime state) and NOTHING
// here touches Settings: a measurement must never masquerade as a user
// decision.
package node

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"math"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
)

const (
	probeSamples    = 3
	probeEWMAAlpha  = 0.25
	scoreHysteresis = 0.72 // candidate must score below current×0.72 (~28% better)
	minStablePeriod = 5 * time.Minute
	probeParallel   = 4
)

// ---- pure scoring (unit-tested without a network) ----

// relayScore folds one relay's measured history into a single comparable
// number — lower is better. Penalties are expressed in millisecond
// equivalents so the units stay honest.
func relayScore(st RelayProbeStats) float64 {
	if st.LoadClass == relay.LoadOverloaded || st.LoadClass == relay.LoadDraining {
		return math.Inf(1) // excluded, not merely penalized
	}
	s := st.RTTEWMAMs + 0.5*st.JitterEWMAMs
	s += 500 * float64(st.ConsecutiveFailures)
	if st.LoadClass == relay.LoadBusy {
		s += 100
	}
	if st.SuccessCount == 0 {
		return math.Inf(1) // never measured successfully — not a candidate
	}
	return s
}

// shouldSwitch decides whether a candidate displaces the current primary:
// only on a decisive advantage, an unhealthy current, or no current at
// all — and never before the minimum stable period has passed.
func shouldSwitch(cur, cand RelayProbeStats, curRef string,
	sinceSelection time.Duration) bool {
	if curRef == "" {
		return true
	}
	curScore, candScore := relayScore(cur), relayScore(cand)
	if math.IsInf(curScore, 1) {
		return !math.IsInf(candScore, 1) // current is dead/overloaded
	}
	if sinceSelection < minStablePeriod {
		return false
	}
	return candScore < curScore*scoreHysteresis
}

// ewma folds a new sample in (α=0.25: new measurements move the estimate
// gradually — one lucky packet never re-ranks the world).
func ewma(prev, sample float64) float64 {
	if prev == 0 {
		return sample
	}
	return prev + probeEWMAAlpha*(sample-prev)
}

// ---- measurement ----

// probeOnce runs up to probeSamples probes over the pooled control lane
// and returns the MEDIAN RTT with the last reply's facts. A legacy relay
// (unknown msgProbe) falls back to the msgTime-only profile — allowed
// for custom relays; official ones are required to speak probe.
func (r *Runtime) probeOnce(addr string) (relay.ProbeResult, error) {
	var rtts []time.Duration
	var last relay.ProbeResult
	err := r.withRelayControl(addr, func(c *relay.Client) error {
		nonce := make([]byte, 8)
		_, _ = rand.Read(nonce)
		for i := 0; i < probeSamples; i++ {
			res, err := c.Probe(nonce)
			if err != nil {
				var re relay.ErrRelay
				if errors.As(err, &re) && strings.Contains(re.Reason, "unknown message type") {
					// Legacy profile: measure with msgTime alone.
					now, rtt, terr := c.Time()
					if terr != nil {
						return terr
					}
					res = relay.ProbeResult{RTT: rtt, NowMS: now, Accepting: true}
				} else {
					return err
				}
			}
			rtts = append(rtts, res.RTT)
			last = res
		}
		return nil
	})
	if err != nil {
		return relay.ProbeResult{}, err
	}
	sort.Slice(rtts, func(i, j int) bool { return rtts[i] < rtts[j] })
	last.RTT = rtts[len(rtts)/2] // median damps the tick quantization
	return last, nil
}

// recordProbe folds a measurement (or failure) into relays.json.
func (r *Runtime) recordProbe(ref string, res relay.ProbeResult, perr error) {
	nowU := int64(nowUnix())
	_ = r.updateRelayState(func(st *RelayLocalState) {
		s := st.Stats[ref]
		if s == nil {
			s = &RelayProbeStats{}
			st.Stats[ref] = s
		}
		if perr != nil {
			s.FailureCount++
			s.ConsecutiveFailures++
			s.LastFailureUnix = nowU
			return
		}
		ms := float64(res.RTT) / float64(time.Millisecond)
		if s.RTTEWMAMs != 0 {
			s.JitterEWMAMs = ewma(s.JitterEWMAMs, math.Abs(ms-s.LastRTTMs))
		}
		s.RTTEWMAMs = ewma(s.RTTEWMAMs, ms)
		s.LastRTTMs = ms
		s.SuccessCount++
		s.ConsecutiveFailures = 0
		s.LastSuccessUnix = nowU
		s.LoadClass = res.Load
	})
}

// ---- selection ----

// runAutoSelection probes every compatible registry relay (bounded
// parallelism) and picks primary + backup. The backup prefers a
// DIFFERENT region — two relays in one failure domain are one relay
// with extra steps.
func (r *Runtime) runAutoSelection() (primary, backup string) {
	cands := BuiltinRelayRegistry.Compatible(RelayProtocolVersionMin, RelayProtocolVersionMax)
	sem := make(chan struct{}, probeParallel)
	done := make(chan struct{})
	for _, d := range cands {
		go func(d RelayDescriptor) {
			sem <- struct{}{}
			defer func() { <-sem; done <- struct{}{} }()
			ref := RelayRef{Official: d.ID}.String()
			res, err := r.probeOnce(d.Endpoint)
			if err == nil && !res.Accepting {
				err = errRelayCoolingDown // not accepting = not a candidate now
			}
			r.recordProbe(ref, res, err)
		}(d)
	}
	for range cands {
		<-done
	}

	st := r.loadRelayState()
	type scored struct {
		ref    string
		region string
		s      float64
	}
	var table []scored
	for _, d := range cands {
		ref := RelayRef{Official: d.ID}.String()
		if ps := st.Stats[ref]; ps != nil {
			table = append(table, scored{ref, d.Region, relayScore(*ps)})
		}
	}
	sort.Slice(table, func(i, j int) bool { return table[i].s < table[j].s })
	if len(table) == 0 || math.IsInf(table[0].s, 1) {
		return "", ""
	}
	primary = table[0].ref
	primaryRegion := table[0].region
	for _, t := range table[1:] {
		if math.IsInf(t.s, 1) {
			break
		}
		if t.region != primaryRegion {
			backup = t.ref
			break
		}
	}
	if backup == "" && len(table) > 1 && !math.IsInf(table[1].s, 1) {
		backup = table[1].ref // same region beats no backup at all
	}

	// Persist — with hysteresis against the previous selection.
	_ = r.updateRelayState(func(ls *RelayLocalState) {
		since := time.Duration(int64(nowUnix())-ls.LastSelectionUnix) * time.Second
		if ls.SelectedPrimary != "" && ls.SelectedPrimary != primary {
			cur := ls.Stats[ls.SelectedPrimary]
			cand := ls.Stats[primary]
			if cur != nil && cand != nil &&
				!shouldSwitch(*cur, *cand, ls.SelectedPrimary, since) {
				primary = ls.SelectedPrimary // keep — the advantage is not decisive
			}
		}
		ls.SelectedPrimary = primary
		ls.SelectedBackup = backup
		ls.LastSelectionUnix = int64(nowUnix())
		ls.NetworkFingerprint = networkFingerprint()
	})
	return primary, backup
}

// Protocol range this build speaks.
const (
	RelayProtocolVersionMin = 1
	RelayProtocolVersionMax = 1
)

// networkFingerprint is an OPAQUE local hash of the interface set —
// enough to notice "the network changed", never enough to reconstruct
// which network it was.
func networkFingerprint() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var parts []string
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			parts = append(parts, ifc.Name+"/"+a.String())
		}
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return base64.StdEncoding.EncodeToString(sum[:8])
}

// startAutomaticRelay resolves the automatic personal primary and starts
// the sync loop on it. Called from Open when RelayMode == "automatic";
// runs in the background so unlock never waits on a probe.
func (r *Runtime) startAutomaticRelay(interval time.Duration) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		st := r.loadRelayState()
		// Last-known-good first: start syncing immediately, re-measure in
		// the background only when the network moved or the data is stale.
		if st.SelectedPrimary != "" && st.NetworkFingerprint == networkFingerprint() &&
			time.Since(time.Unix(st.LastSelectionUnix, 0)) < time.Hour {
			if ref, err := ParseRelayRef(st.SelectedPrimary); err == nil {
				if ep, ok := ref.Resolve(BuiltinRelayRegistry); ok {
					r.applyRelaySync(ep, interval)
					return
				}
			}
		}
		primary, _ := r.runAutoSelection()
		if primary == "" {
			return // nothing reachable; a later re-measure may succeed
		}
		if ref, err := ParseRelayRef(primary); err == nil {
			if ep, ok := ref.Resolve(BuiltinRelayRegistry); ok {
				r.applyRelaySync(ep, interval)
			}
		}
	}()
}

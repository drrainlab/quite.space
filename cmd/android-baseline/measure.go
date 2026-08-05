package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/node"
	"github.com/drrainlab/quiet_places/protocol/quicklink"
)

// probeResult is one measurement taken in one process. It is JSON because the
// fresh-process probes are separate processes: the parent execs itself and
// reads this back on stdout.
type probeResult struct {
	Probe string `json:"probe"`

	InspectNS int64 `json:"inspect_ns,omitempty"`
	VerifyNS  int64 `json:"verify_ns,omitempty"`
	OpenNS    int64 `json:"open_ns,omitempty"`
	// ReplayNS is Open − Verify, computed IN THIS PROCESS on the same
	// directory. The parent takes quantiles of these deltas rather than
	// subtracting one quantile from another: a quantile of differences is not
	// the difference of quantiles, and the wrong one hides exactly the
	// variance worth seeing.
	ReplayNS int64 `json:"replay_ns,omitempty"`

	SealNS int64 `json:"seal_ns,omitempty"`
	OpenQL int64 `json:"quicklink_open_ns,omitempty"`

	// Four numbers, not one, because they answer four different questions:
	// what the Go runtime costs before anything happens, what the operation
	// peaked at, what the operation ADDED over that baseline, and what it left
	// behind. A single "peak RSS" cannot distinguish a cheap operation in an
	// expensive runtime from an expensive one in a cheap runtime.
	BaseRSSKB uint64 `json:"base_rss_kb,omitempty"` // before the operation
	VmHWMKB   uint64 `json:"vm_hwm_kb,omitempty"`   // peak during it
	VmRSSKB   uint64 `json:"vm_rss_kb,omitempty"`   // settled after it
	HeapAll   uint64 `json:"go_heap_alloc,omitempty"`
	HeapSys   uint64 `json:"go_heap_sys,omitempty"`
	GoSys     uint64 `json:"go_sys,omitempty"`

	Err string `json:"err,omitempty"`
}

// runProbe is the body of the `probe` subcommand: exactly one measurement, in
// a process that has done nothing else. Fresh-process isolation is not
// fastidiousness here — a 128 MiB transient measured in a process that already
// peaked at 128 MiB reads as no transient at all.
//
// EVERY duration below is monotonic: time.Since over a time.Now stamp reads
// Go's monotonic clock, never the wall clock, so a clock adjustment mid-run
// cannot produce a negative or absurd measurement. Wall clock appears nowhere
// in this file. (CLOCK_MONOTONIC stops while a device is suspended, which is
// exactly right for a CPU-bound operation and exactly wrong for measuring a
// Doze — that is why android/quietcore reports CLOCK_BOOTTIME beside it.)
func runProbe(kind, dir, pass string) probeResult {
	r := probeResult{Probe: kind}
	// The baseline is taken before a single byte of work, and the peak is
	// reset with it: otherwise "peak" means "the largest thing this process
	// ever did", including whatever the runtime did while starting.
	r.BaseRSSKB = currentRSS()
	resetPeakRSS()
	switch kind {
	case "rungs":
		t0 := time.Now()
		_ = node.Inspect(dir)
		r.InspectNS = time.Since(t0).Nanoseconds()

		t1 := time.Now()
		if err := node.VerifyPassphrase(dir, []byte(pass)); err != nil {
			r.Err = "verify: " + err.Error()
			return r
		}
		r.VerifyNS = time.Since(t1).Nanoseconds()

		t2 := time.Now()
		rt, err := node.Open(dir, []byte(pass), "measure")
		if err != nil {
			r.Err = "open: " + err.Error()
			return r
		}
		r.OpenNS = time.Since(t2).Nanoseconds()
		rt.Close()

		// Paired, in-process, on one directory.
		r.ReplayNS = r.OpenNS - r.VerifyNS

	case "quicklink-seal":
		t, err := quicklink.New()
		if err != nil {
			r.Err = err.Error()
			return r
		}
		t0 := time.Now()
		if _, err := quicklink.Seal(t, samplePayload()); err != nil {
			r.Err = err.Error()
			return r
		}
		r.SealNS = time.Since(t0).Nanoseconds()

	case "quicklink-open":
		// The seal is done FIRST and not timed: opening is the user's critical
		// path when redeeming an invite, and folding the two together would
		// hide which half costs what.
		t, err := quicklink.New()
		if err != nil {
			r.Err = err.Error()
			return r
		}
		sealed, err := quicklink.Seal(t, samplePayload())
		if err != nil {
			r.Err = err.Error()
			return r
		}
		// COLLECT THE SEAL'S 128 MiB BEFORE TIMING THE OPEN, and reset the
		// high-water mark that survives it.
		//
		// Found by running this: without the collect, the open probe peaked at
		// 264.7 MB on the phone — both scrypt allocations live at once — and
		// that number would have gone into the report as "the cost of opening
		// an invitation". It is not. A person redeeming a link in a fresh app
		// does the open and never the seal, so the honest figure is one KDF,
		// and the doubled one was an artefact of the fixture.
		runtime.GC()
		debug.FreeOSMemory()
		resetPeakRSS()

		t0 := time.Now()
		if _, err := quicklink.Open(t, sealed); err != nil {
			r.Err = err.Error()
			return r
		}
		r.OpenQL = time.Since(t0).Nanoseconds()

	default:
		r.Err = "unknown probe " + kind
		return r
	}

	readMemory(&r)
	return r
}

func samplePayload() quicklink.Payload {
	return quicklink.Payload{
		PassLink:  strings.Repeat("A", 512),
		From:      "measurement",
		Space:     "measurement",
		MaxUses:   1,
		ExpiresAt: 1,
	}
}

// readMemory takes both halves and says which is which. VmHWM is the one that
// catches a transient: a 128 MiB spike lasting a second is invisible to VmRSS
// sampled afterwards, and reporting only the settled figure would report that
// the spike did not happen.
func readMemory(r *probeResult) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	r.HeapAll, r.HeapSys, r.GoSys = ms.HeapAlloc, ms.HeapSys, ms.Sys

	f, err := os.Open("/proc/self/status")
	if err != nil {
		return // darwin has no procfs; the Go figures still stand
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "VmHWM:"):
			r.VmHWMKB = kbOf(line)
		case strings.HasPrefix(line, "VmRSS:"):
			r.VmRSSKB = kbOf(line)
		}
	}
}

// currentRSS is the settled resident size right now, in kB. Zero where there
// is no procfs, which is every darwin run — and the report says n/a rather
// than pretending the reading was taken.
func currentRSS() uint64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "VmRSS:") {
			return kbOf(line)
		}
	}
	return 0
}

// resetPeakRSS clears VmHWM so a later reading measures THIS operation rather
// than the largest thing the process ever did. Linux 4.0+ exposes it as
// /proc/self/clear_refs value 5; a kernel that does not is a silent no-op,
// which is why the report distinguishes peak from settled instead of relying
// on the reset having worked.
func resetPeakRSS() {
	_ = os.WriteFile("/proc/self/clear_refs", []byte("5\n"), 0o200)
}

func kbOf(line string) uint64 {
	f := strings.Fields(line)
	if len(f) < 2 {
		return 0
	}
	v, _ := strconv.ParseUint(f[1], 10, 64)
	return v
}

// ── the checks that are correctness, not cost ────────────────────────────────

type check struct {
	Name   string
	Pass   bool
	Detail string
	// Fatal marks a check whose failure fails the whole gate, as distinct from
	// one that is informational. Saying which in the code, before the run,
	// is what stops a bad result being renegotiated afterwards.
	Fatal bool
}

// flockChecks answers the question that decides where the data directory may
// live. Internal storage MUST pass; external is informational and is never
// auto-selected, because lock.go:72-80 fails CLOSED on EOPNOTSUPP and would
// refuse to start the app with a message about network filesystems.
func flockChecks(internal, external string) []check {
	out := []check{probeLock("flock internal", internal, true)}
	if external != "" {
		c := probeLock("flock external", external, false)
		c.Detail += "  (informational — never auto-selected)"
		out = append(out, c)
	}
	return out
}

func probeLock(name, dir string, fatal bool) check {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return check{Name: name, Detail: "mkdir: " + err.Error(), Fatal: fatal}
	}
	l, err := storage.Lock(dir)
	if err != nil {
		return check{Name: name, Detail: err.Error(), Fatal: fatal}
	}
	_ = l.Release()
	return check{Name: name, Pass: true, Detail: "taken and released", Fatal: fatal}
}

// keystoreChecks is the block the charter names and a KDF timing does not
// touch. Every one of these is about identity surviving, or failing loudly.
func keystoreChecks(work, pass string) []check {
	dir := filepath.Join(work, "keystore-semantics")
	_ = os.RemoveAll(dir)
	var out []check

	rt, err := node.Open(dir, []byte(pass), "ks")
	if err != nil {
		return append(out, check{Name: "keystore create", Detail: err.Error(), Fatal: true})
	}
	first := rt.Principal.Fingerprint()
	rt.Close()
	out = append(out, check{Name: "keystore create", Pass: true,
		Detail: "fingerprint " + short(first), Fatal: true})

	// Reopen: the same identity, not a new one.
	rt2, err := node.Open(dir, []byte(pass), "ks")
	if err != nil {
		out = append(out, check{Name: "reopen", Detail: err.Error(), Fatal: true})
	} else {
		same := rt2.Principal.Fingerprint() == first
		rt2.Close()
		out = append(out, check{Name: "reopen keeps identity", Pass: same,
			Detail: map[bool]string{true: "same fingerprint", false: "FINGERPRINT CHANGED"}[same],
			Fatal:  true})
	}

	// Wrong passphrase: a named failure, and NOT a new identity.
	if rt3, err := node.Open(dir, []byte(pass+"-wrong"), "ks"); err == nil {
		rt3.Close()
		out = append(out, check{Name: "wrong passphrase fails closed",
			Detail: "IT OPENED", Fatal: true})
	} else {
		named := strings.Contains(err.Error(), "passphrase")
		out = append(out, check{Name: "wrong passphrase fails closed", Pass: named,
			Detail: err.Error(), Fatal: true})
	}

	// Modes: dirs 0700, files 0600, as kernel/storage/lock.go intends.
	out = append(out, modeCheck(dir))

	// A truncated keystore must be a NAMED failure, never a fresh identity.
	// This is the one that matters most: silently minting a new identity on a
	// damaged file is indistinguishable, to the person, from losing everything
	// they had.
	out = append(out, truncationCheck(dir, pass, first))

	// Two processes are covered by the package host's :contender activity —
	// a second Open in THIS process would be refused by the runtime before
	// the lock was ever consulted, so it would pass without testing anything.
	out = append(out, check{Name: "two processes, one data dir",
		Pass: true, Detail: "measured by the package host (:contender), not here"})

	return out
}

func modeCheck(dir string) check {
	type want struct {
		path string
		mode os.FileMode
	}
	for _, w := range []want{
		{filepath.Join(dir, "keys"), 0o700},
		{filepath.Join(dir, "keys", "keystore.enc"), 0o600},
		{filepath.Join(dir, "keys", "salt"), 0o600},
	} {
		st, err := os.Stat(w.path)
		if err != nil {
			return check{Name: "file modes", Detail: err.Error()}
		}
		if got := st.Mode().Perm(); got != w.mode {
			return check{Name: "file modes",
				Detail: fmt.Sprintf("%s is %v, want %v", filepath.Base(w.path), got, w.mode)}
		}
	}
	return check{Name: "file modes", Pass: true, Detail: "dirs 0700, files 0600"}
}

func truncationCheck(dir, pass, first string) check {
	ks := filepath.Join(dir, "keys", "keystore.enc")
	backup, err := os.ReadFile(ks)
	if err != nil {
		return check{Name: "truncated keystore is named, not a new identity",
			Detail: err.Error(), Fatal: true}
	}
	defer os.WriteFile(ks, backup, 0o600)

	if err := os.WriteFile(ks, backup[:len(backup)/2], 0o600); err != nil {
		return check{Name: "truncated keystore is named, not a new identity",
			Detail: err.Error(), Fatal: true}
	}
	rt, err := node.Open(dir, []byte(pass), "ks")
	if err == nil {
		got := rt.Principal.Fingerprint()
		rt.Close()
		if got == first {
			return check{Name: "truncated keystore is named, not a new identity",
				Pass: true, Detail: "recovered the same identity"}
		}
		return check{Name: "truncated keystore is named, not a new identity",
			Detail: "OPENED WITH A DIFFERENT IDENTITY — " + short(got), Fatal: true}
	}
	return check{Name: "truncated keystore is named, not a new identity",
		Pass: true, Detail: err.Error(), Fatal: true}
}

func short(fp string) string {
	if len(fp) > 14 {
		return fp[:14] + "…"
	}
	return fp
}

// ── statistics ───────────────────────────────────────────────────────────────

type series struct{ v []time.Duration }

func (s *series) add(ns int64) { s.v = append(s.v, time.Duration(ns)) }

func (s *series) pct(p float64) time.Duration {
	if len(s.v) == 0 {
		return 0
	}
	c := append([]time.Duration(nil), s.v...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	return c[int(float64(len(c)-1)*p)]
}

func (s *series) line() string {
	if len(s.v) == 0 {
		return "no samples"
	}
	return fmt.Sprintf("p50 %s · p95 %s · max %s",
		round(s.pct(0.5)), round(s.pct(0.95)), round(s.pct(1)))
}

func round(d time.Duration) time.Duration {
	switch {
	case d > time.Second:
		return d.Round(10 * time.Millisecond)
	case d > time.Millisecond:
		return d.Round(100 * time.Microsecond)
	default:
		return d.Round(time.Microsecond)
	}
}

func decodeProbe(out []byte) (probeResult, error) {
	var r probeResult
	// The probe prints one JSON line last; anything before it is noise from a
	// runtime that decided to say something, and dropping it here is kinder
	// than making every caller strip it.
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	err := json.Unmarshal([]byte(lines[len(lines)-1]), &r)
	return r, err
}

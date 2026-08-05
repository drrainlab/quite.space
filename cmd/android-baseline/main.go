// Command android-baseline is AR-0's raw-lane measurement harness: what the
// core costs to open on a phone, and whether the things that must not go wrong
// go wrong.
//
// IT DELIBERATELY MIRRORS cmd/rnode-baseline field for field — the aligned
// block, p50/p95/max and never an average, then one PASSES / DOES NOT PASS
// sentence. Two runs of two different reports are not comparable without an
// argument about method, and the argument is worth more than the difference.
//
// WHAT IT IS NOT. This is the RAW lane. It runs as an ordinary process out of
// /data/local/tmp, which is the right place for cross-compilation, syscalls,
// flock and microbenchmarks — and the WRONG place for any claim about process
// lifetime. filesDir is package-private, `am force-stop` acts on a package,
// and App Standby, the App Freezer and LMK all key on an app UID and a package
// process tree. Everything lifecycle-shaped belongs to the package host in
// android/host, and this harness says so rather than quietly overreaching.
//
// TARGETS ARE FIXED AFTER RUNNING THIS ON REAL HARDWARE, NOT INVENTED HERE —
// the same rule cmd/relay-load states about itself. What IS fixed in advance
// is which quantities can fail the gate and which only set a budget, and the
// arithmetic of the replay-shape classification. Deciding that afterwards is
// how a bad result gets renegotiated.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	toolVersion  = "android-baseline/ar0"
	manifestName = "corpus-manifest.json"

	// linearityFactor classifies replay shape, and it is written here, before
	// any data exists, because non-linearity is explicitly NOT a gate failure
	// and a number chosen afterwards would be a number chosen to fit.
	//
	//	slope_1 = (T_2K − T_K)  / K
	//	slope_2 = (T_4K − T_2K) / (2K)
	//	LINEAR if slope_2 ≤ 1.25 × slope_1
	linearityFactor = 1.25
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "gen":
		err = cmdGen(os.Args[2:])
	case "measure":
		err = cmdMeasure(os.Args[2:])
	case "probe":
		err = cmdProbe(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `android-baseline — AR-0's raw-lane measurement

  gen      -out DIR -kind A|B -spaces M -events N [-pass P]
           build a corpus and stamp a manifest beside it

  measure  -corpus DIR [-corpus DIR ...] -runs N [-condition NAME]
           the measurement. Every run works on a FRESH COPY of the corpus,
           and every probe runs in a FRESH PROCESS.

  probe    -kind rungs|quicklink-seal|quicklink-open -dir DIR [-pass P]
           internal: one measurement, one JSON line. measure execs this.

Conditions are named rather than assumed: on a physical phone the kernel page
cache is not ours to control, so "cold" is a claim this harness will not make.
Use -condition first-after-reboot | fresh-process | repeat-open, and the report
prints it beside every number.
`)
}

// ── gen ──────────────────────────────────────────────────────────────────────

func cmdGen(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	out := fs.String("out", "", "corpus directory to create")
	kind := fs.String("kind", "A", "A (controlled) or B (beta-realistic)")
	spaces := fs.Int("spaces", 1, "number of spaces")
	events := fs.Int("events", 1000, "events per space")
	pass := fs.String("pass", "android-baseline-corpus", "keystore passphrase (a fixture, not an identity)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	t0 := time.Now()
	m, err := generateCorpus(*out, *kind, *pass, *spaces, *events)
	if err != nil {
		return err
	}
	fmt.Printf("corpus %s: %d spaces × %d events = %d events, %d files, %.1f MB, in %s\n",
		*kind, m.Spaces, m.EventsEach, m.Events, len(m.Files),
		float64(m.Bytes)/(1<<20), time.Since(t0).Round(time.Millisecond))
	return nil
}

// ── probe ────────────────────────────────────────────────────────────────────

func cmdProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	kind := fs.String("kind", "rungs", "rungs | quicklink-seal | quicklink-open")
	dir := fs.String("dir", "", "data directory")
	pass := fs.String("pass", "", "passphrase")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r := runProbe(*kind, *dir, *pass)
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

// ── measure ──────────────────────────────────────────────────────────────────

type corpusRun struct {
	dir      string
	manifest *corpusManifest
	inspect  series
	verify   series
	open     series
	replay   series
}

func cmdMeasure(args []string) error {
	fs := flag.NewFlagSet("measure", flag.ExitOnError)
	var corpora multiFlag
	fs.Var(&corpora, "corpus", "corpus directory (repeatable; order is the scaling axis)")
	runs := fs.Int("runs", 5, "measurement runs per corpus")
	qlRuns := fs.Int("quicklink-runs", 5, "quicklink probes (each in a fresh process)")
	cond := fs.String("condition", "fresh-process", "first-after-reboot | fresh-process | repeat-open")
	work := fs.String("work", "", "scratch directory (default: alongside the first corpus)")
	external := fs.String("external", "", "an external-storage path, for the informational flock probe")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(corpora) == 0 {
		return fmt.Errorf("-corpus is required at least once")
	}
	if *work == "" {
		*work = filepath.Join(filepath.Dir(corpora[0]), "ab-work")
	}
	if err := os.MkdirAll(*work, 0o700); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find own path (needed for fresh-process probes): %w", err)
	}

	// Verify every corpus BEFORE measuring anything. A mismatch aborts: two
	// numbers over two different histories look exactly as comparable as two
	// numbers over one.
	var rs []*corpusRun
	for _, c := range corpora {
		m, err := verifyCorpus(c)
		if err != nil {
			return err
		}
		rs = append(rs, &corpusRun{dir: c, manifest: m})
	}

	for _, r := range rs {
		for i := 0; i < *runs; i++ {
			copyDir := filepath.Join(*work, "run")
			if err := copyCorpus(r.dir, copyDir); err != nil {
				return fmt.Errorf("fresh copy: %w", err)
			}
			out, err := exec.Command(self, "probe",
				"-kind", "rungs", "-dir", copyDir, "-pass", r.manifest.Passphrase).CombinedOutput()
			if err != nil {
				return fmt.Errorf("probe failed (%v): %s", err, strings.TrimSpace(string(out)))
			}
			p, err := decodeProbe(out)
			if err != nil {
				return fmt.Errorf("probe output unreadable: %w", err)
			}
			if p.Err != "" {
				return fmt.Errorf("probe reported: %s", p.Err)
			}
			r.inspect.add(p.InspectNS)
			r.verify.add(p.VerifyNS)
			r.open.add(p.OpenNS)
			r.replay.add(p.ReplayNS)
		}
	}

	// quicklink, each half separately: opening is the user's critical path
	// when redeeming an invite, and sealing is not.
	var qlSeal, qlOpen series
	var qlSealMem, qlOpenMem probeResult
	for _, k := range []string{"quicklink-seal", "quicklink-open"} {
		for i := 0; i < *qlRuns; i++ {
			out, err := exec.Command(self, "probe", "-kind", k).CombinedOutput()
			if err != nil {
				return fmt.Errorf("%s probe failed (%v): %s", k, err, strings.TrimSpace(string(out)))
			}
			p, err := decodeProbe(out)
			if err != nil || p.Err != "" {
				return fmt.Errorf("%s probe unreadable: %v %s", k, err, p.Err)
			}
			if k == "quicklink-seal" {
				qlSeal.add(p.SealNS)
				qlSealMem = p
			} else {
				qlOpen.add(p.OpenQL)
				qlOpenMem = p
			}
		}
	}

	checks := flockChecks(filepath.Join(*work, "flock-internal"), *external)
	checks = append(checks, keystoreChecks(*work, "android-baseline-keystore")...)

	report(*cond, rs, &qlSeal, &qlOpen, qlSealMem, qlOpenMem, checks)
	return nil
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(s string) error { *m = append(*m, s); return nil }

// ── report ───────────────────────────────────────────────────────────────────

func report(cond string, rs []*corpusRun, qlSeal, qlOpen *series,
	sealMem, openMem probeResult, checks []check) {

	line := strings.Repeat("─", 74)
	fmt.Println("\n" + line)
	fmt.Printf("host         %s/%s · %d cores · %s\n",
		runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.Version())
	fmt.Printf("condition    %s", cond)
	if cond == "fresh-process" || cond == "repeat-open" {
		// Said in the report, not only in the plan: a fresh directory and a
		// fresh process clear APPLICATION state, never the kernel page cache.
		fmt.Print("   (OS cache uncontrolled)")
	}
	fmt.Println()
	fmt.Println(line)

	for _, r := range rs {
		m := r.manifest
		fmt.Printf("\ncorpus       %s\n", filepath.Base(r.dir))
		fmt.Printf("  shape      kind %s · %d spaces (%d private) · %d events · %.1f MB · %d files\n",
			m.Kind, m.Spaces, m.PrivateN, m.Events, float64(m.Bytes)/(1<<20), len(m.Files))
		fmt.Printf("  inspect    %s\n", r.inspect.line())
		fmt.Printf("  verify     %s   (scrypt N=2^15 / 32 MiB + keystore open)\n", r.verify.line())
		fmt.Printf("  open       %s\n", r.open.line())
		fmt.Printf("  replay     %s   (PAIRED Open−Verify, quantiles of the deltas)\n", r.replay.line())
		if m.Events > 0 {
			per := r.replay.pct(0.5) / time.Duration(m.Events)
			mbs := float64(m.Bytes) / (1 << 20) / r.replay.pct(0.5).Seconds()
			fmt.Printf("  per event  %s/event · %.1f MB/s over the corpus\n", round(per), mbs)
		}
	}

	fmt.Printf("\nquicklink    seal  %s\n", qlSeal.line())
	fmt.Printf("             open  %s   ← the redeem path (N=2^17 / 128 MiB)\n", qlOpen.line())
	fmt.Printf("  memory     seal: peak %s settled %s · go heap %s sys %s\n",
		kb(sealMem.VmHWMKB), kb(sealMem.VmRSSKB), by(sealMem.HeapAll), by(sealMem.GoSys))
	fmt.Printf("             open: peak %s settled %s · go heap %s sys %s\n",
		kb(openMem.VmHWMKB), kb(openMem.VmRSSKB), by(openMem.HeapAll), by(openMem.GoSys))

	shape, shapeDetail := replayShape(rs)
	fmt.Printf("\nREPLAY SHAPE: %s", shape)
	if shapeDetail != "" {
		fmt.Printf(" — %s", shapeDetail)
	}
	fmt.Println("\n             (a classification, not an assertion: non-linearity is a")
	fmt.Println("              result, and this harness never fails the gate on a shape)")

	fmt.Println("\ncorrectness")
	var fatalFail []string
	for _, c := range checks {
		mark := "ok  "
		if !c.Pass {
			mark = "FAIL"
			if c.Fatal {
				fatalFail = append(fatalFail, c.Name)
			}
		}
		fmt.Printf("  %s  %-46s %s\n", mark, c.Name, c.Detail)
	}

	fmt.Println(line)
	if len(fatalFail) > 0 {
		fmt.Printf("\nDOES NOT PASS: %s.\n", strings.Join(fatalFail, "; "))
		os.Exit(1)
	}
	fmt.Println("\nPASSES the AR-0b gate: every correctness check held, and every")
	fmt.Println("cost above is a BUDGET to be fixed from these numbers — not a")
	fmt.Println("threshold this run was measured against.")
}

// replayShape applies the criterion fixed in linearityFactor above, and then
// says whether that criterion could be TRUSTED on this data.
//
// The two-slope test silently assumes replay is b·N with a small constant. It
// is not: opening a node costs a fixed amount before a single event is
// replayed, and when that constant is large next to the differences being
// divided, the first slope is mostly constant and the ratio means nothing.
// This was not a hypothesis — the first phone run produced 5.02× while the
// per-event cost FELL from 137µs to 78µs, which no superlinear process does.
//
// So the fit is reported beside the classification. A large intercept next to
// the smallest measurement is the harness telling the reader that its own
// headline is not to be quoted.
func replayShape(rs []*corpusRun) (string, string) {
	if len(rs) < 3 {
		return "NOT CLASSIFIED", fmt.Sprintf("needs 3 corpora on one axis, got %d", len(rs))
	}
	type pt struct {
		n int
		t float64
	}
	var p []pt
	for _, r := range rs {
		p = append(p, pt{r.manifest.Events, r.replay.pct(0.5).Seconds()})
	}
	sort.Slice(p, func(i, j int) bool { return p[i].n < p[j].n })
	if p[0].n == 0 || p[1].n == p[0].n || p[2].n == p[1].n {
		return "NOT CLASSIFIED", "the corpora do not form a scaling axis"
	}

	// Least squares over every point: T = a + b·N.
	var sn, st, snt, snn float64
	for _, q := range p {
		n := float64(q.n)
		sn, st, snt, snn = sn+n, st+q.t, snt+n*q.t, snn+n*n
	}
	m := float64(len(p))
	den := m*snn - sn*sn
	var a, b float64
	if den != 0 {
		b = (m*snt - sn*st) / den
		a = (st - b*sn) / m
	}
	fit := fmt.Sprintf("fit: %s fixed + %s/event", round(secs(a)), round(secs(b)))

	s1 := (p[1].t - p[0].t) / float64(p[1].n-p[0].n)
	s2 := (p[2].t - p[1].t) / float64(p[2].n-p[1].n)
	if s1 <= 0 {
		return "NOT CLASSIFIED", "the first slope is not positive — too fast to resolve; " + fit
	}
	g := s2 / s1

	// The confound test, stated as arithmetic: if the fixed cost is more than
	// a third of the smallest measurement, the two-slope ratio is dominated by
	// the constant and is not evidence about shape.
	if a > p[0].t/3 {
		return "NOT CLASSIFIED", fmt.Sprintf(
			"two-slope says %.2f×, but the fixed cost is %.0f%% of the smallest "+
				"measurement — the ratio is measuring the constant, not the shape. %s. "+
				"Re-measure at larger N.", g, a/p[0].t*100, fit)
	}
	if g <= linearityFactor {
		return "LINEAR", fmt.Sprintf("slope grew %.2f× (criterion ≤ %.2f×); %s", g, linearityFactor, fit)
	}
	return "NONLINEAR", fmt.Sprintf("slope grew %.2f× (criterion ≤ %.2f×); %s", g, linearityFactor, fit)
}

func secs(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

func kb(v uint64) string {
	if v == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f MB", float64(v)/1024)
}

func by(v uint64) string {
	if v == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f MB", float64(v)/(1<<20))
}

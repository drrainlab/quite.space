// Package quietcore is AR-0's Android binding: the smallest surface that lets
// the core run INSIDE an Android application process.
//
// WHY IN-PROCESS AND NOT A SUBPROCESS. The cheap route is to ship the static
// binary as a .so and spawn it with ProcessBuilder. It was rejected, and the
// reason is the whole point of the rig: Android's low-memory killer works from
// oom_adj scores that ActivityManager assigns to APPLICATION processes, and
// Doze throttles an app's network. A forked child inherits its parent's score
// at fork time and is never updated, so it can be killed at a different moment
// than the app — or survive it. That would weaken exactly two of the gates the
// rig exists to run: background → return, and Doze. A measurement rig that is
// lower-fidelity in precisely the place being measured is not a rig.
//
// So the core lives in the app process, the app's lifecycle IS the core's
// lifecycle, and the price of that — an NDK and CGO_ENABLED=1 — is one of the
// two things AR-0 was chartered to find out.
//
// The Java side never reaches past this file into the domain. It gets a port
// and a token and speaks the ordinary local HTTP API, which is ADR-011's
// boundary and the same seam every other client uses.
package quietcore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/protocol/quicklink"

	webui "github.com/drrainlab/quiet_places/clients/web-ui"
	"github.com/drrainlab/quiet_places/node"
	"github.com/drrainlab/quiet_places/transports/lan"
)

// processStartedAt is stamped when the shared library is loaded, i.e. when the
// hosting process came up. It is one half of what makes "the process was kept"
// a provable claim rather than a plausible one — see Status.
var processStartedAt = time.Now().UTC()

// procStartTicks is the kernel's own answer to the same question, read from
// /proc/self/stat field 22 (starttime, in clock ticks since boot). Carried
// beside the Go-level stamp because it cannot be confused by anything
// happening inside the runtime: if Android silently restarted the process,
// this changes. Empty when unreadable, which is a fact and not a failure.
var procStartTicks = readProcStartTicks()

// The runtime's state, as three words a host can act on differently.
//
//	unavailable  nothing is open, and nothing is trying
//	opening      an open is IN FLIGHT — the node is not dead, it is working
//	alive        open and serving
//
// "opening" exists because node.Open is seconds, not milliseconds: scrypt plus
// a full log replay. A controller that cannot see this window has to guess,
// and it will guess "dead" — which is how a UI ends up offering to start a
// node that is already halfway up.
const (
	StateUnavailable = "unavailable"
	StateOpening     = "opening"
	StateAlive       = "alive"
)

var (
	// TWO locks, and the split is the point.
	//
	// openMu serializes the OPERATION (open, close) and is held for seconds.
	// stateMu guards the fields Status reads and is held for microseconds.
	// One lock for both meant Status blocked behind an in-flight Open — so for
	// the whole window where a host most needs to distinguish "opening" from
	// "dead", it could not ask at all.
	openMu  sync.Mutex
	stateMu sync.Mutex

	state = StateUnavailable

	rt       *node.Runtime
	listener net.Listener

	// runtimeEpoch is fresh for every successful Start. Distinct from the
	// process stamps above on purpose: the process surviving and the node
	// having been reopened inside it are different facts, and background →
	// return needs to tell them apart.
	runtimeEpoch string

	dataDir     string
	apiPort     int
	apiToken    string
	fingerprint string
	startedAt   time.Time
	lastError   string
)

// Start opens the node against dataDir and serves the local API on a port the
// OS chooses. Idempotent in the direction that matters: a second Start while
// running is an error rather than a second node, because two runtimes on one
// data directory is the failure the data-dir lock exists to prevent and the
// rig must not be the thing that provokes it.
//
// withLAN is off by default at the call site: on Android multicast needs a
// held lock and a permission, and AR-0c syncs over a relay. Exposed anyway so
// the LAN path can be measured deliberately rather than by accident.
func Start(dir, passphrase, name string, withLAN bool) error {
	return StartAs(dir, passphrase, name, "", withLAN)
}

// StartAs is Start with the host's own name for this device (the phone's
// model), used wherever the person is shown their devices.
func StartAs(dir, passphrase, name, deviceLabel string, withLAN bool) error {
	openMu.Lock()
	defer openMu.Unlock()

	stateMu.Lock()
	if rt != nil {
		d := dataDir
		stateMu.Unlock()
		return fmt.Errorf("quietcore: already running (data dir %s)", d)
	}
	// Published BEFORE the seconds-long work, so a controller asking during
	// the open is told "opening" rather than left to infer "dead".
	state = StateOpening
	lastError = ""
	stateMu.Unlock()

	// Any early return from here leaves the state honest rather than stuck at
	// "opening" — a status that lies about work in flight is worse than one
	// that admits failure.
	ok := false
	defer func() {
		if !ok {
			stateMu.Lock()
			state = StateUnavailable
			stateMu.Unlock()
		}
	}()
	if dir == "" {
		// node.DefaultDataDir() reads $HOME, which is unset for an Android app
		// process, so it would yield the RELATIVE path "quiet-data" under a CWD
		// of "/" and fail at MkdirAll. Refusing here names the cause; letting it
		// through would report a permission error three layers down.
		return fmt.Errorf("quietcore: dataDir is required on Android — " +
			"node.DefaultDataDir() reads $HOME, which an app process does not have")
	}
	if name == "" {
		name = "me"
	}
	// The host's name for this device — the phone's model. os.Hostname is
	// "localhost" in an app sandbox and the devices screen showed exactly
	// that until a tester read it.
	if deviceLabel != "" {
		_ = os.Setenv("QUIET_DEVICE_NAME", deviceLabel)
	}

	r, err := node.Open(dir, []byte(passphrase), name)
	if err != nil {
		lastError = err.Error()
		return err
	}

	// A host registered before the node opened must not be forgotten: the
	// Activity installs it at creation and Start happens later, behind a
	// passphrase somebody has to type.
	applyUsbHost(r)

	if withLAN {
		if err := r.StartLAN(":0", lan.MulticastAddr); err != nil {
			// Not fatal, and not silent: a phone that cannot multicast is an
			// ordinary phone, and the reason belongs in status rather than in
			// a log nobody reads.
			lastError = "LAN disabled: " + err.Error()
		}
	}

	api, err := node.NewAPIServer(r, webui.FS())
	if err != nil {
		r.Close()
		lastError = err.Error()
		return err
	}
	addr, l, err := api.Serve(0)
	if err != nil {
		r.Close()
		lastError = err.Error()
		return err
	}

	port := 0
	if _, p, splitErr := net.SplitHostPort(addr); splitErr == nil {
		port, _ = strconv.Atoi(p)
	}

	var epoch [8]byte
	_, _ = rand.Read(epoch[:])

	stateMu.Lock()
	rt, listener = r, l
	state = StateAlive
	ok = true
	runtimeEpoch = hex.EncodeToString(epoch[:])
	dataDir, apiPort, apiToken = dir, port, api.Token()
	// From the PUBLIC id: a paired phone is a secondary and holds no root
	// keypair — the fourth Principal.Fingerprint() call in the tree, and the
	// one the first live secondary actually walked into (SIGABRT at Open).
	fingerprint = r.Fingerprint()
	startedAt = time.Now().UTC()
	lastError = ""
	stateMu.Unlock()

	// AR-1b: the notification plane is armed HERE, and the position is the
	// invariant rather than a detail. node.Open has returned, so every replay
	// through the absorb funnel is already behind us and history cannot reach
	// a sink that did not exist while it ran. Arming any earlier — inside
	// Open, or from a host that got the runtime first — would turn a first run
	// over a large journal into one notification per event ever written.
	armRuntime(r)
	return nil
}

// QuicklinkProbe runs one seal and one open of a quick-link token and returns
// a JSON line with the timings and the process's memory around them.
//
// It lives here rather than only in cmd/android-baseline because the raw-lane
// harness runs as the shell user, and the shell user is not subject to the
// app memory class or to the low-memory killer's view of an application
// process. The 128 MiB transient is the one cost AR-0 flagged as a risk on a
// low-end device, so it has to be measurable WHERE that risk exists.
//
// The seal is collected before the open is timed. Without that, both KDF
// allocations are live at once and the peak reads as ~2× what redeeming a
// link actually costs — measured, not theorised: the first phone run reported
// 264.7 MB before this was fixed.
func QuicklinkProbe() string {
	base := currentRSSKB()
	resetPeakRSS()

	t, err := quicklink.New()
	if err != nil {
		return `{"err":` + strconv.Quote(err.Error()) + `}`
	}
	payload := quicklink.Payload{
		PassLink: strings.Repeat("A", 512), From: "ar0", Space: "ar0", MaxUses: 1, ExpiresAt: 1,
	}

	t0 := time.Now()
	sealed, err := quicklink.Seal(t, payload)
	if err != nil {
		return `{"err":` + strconv.Quote(err.Error()) + `}`
	}
	sealNS := time.Since(t0).Nanoseconds()
	sealPeak := peakRSSKB()

	runtime.GC()
	debug.FreeOSMemory()
	resetPeakRSS()

	t1 := time.Now()
	if _, err := quicklink.Open(t, sealed); err != nil {
		return `{"err":` + strconv.Quote(err.Error()) + `}`
	}
	openNS := time.Since(t1).Nanoseconds()

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	b, _ := json.Marshal(map[string]any{
		"seal_ns":        sealNS,
		"open_ns":        openNS,
		"base_rss_kb":    base,
		"seal_peak_kb":   sealPeak,
		"open_peak_kb":   peakRSSKB(),
		"settled_rss_kb": currentRSSKB(),
		"go_heap_alloc":  ms.HeapAlloc,
		"go_sys":         ms.Sys,
		"pid":            os.Getpid(),
	})
	return string(b)
}

// Stop closes the node and the listener. Safe to call when not running — the
// rig's stop command must be usable without first asking whether it needs to
// be, or every caller grows the same race.
func Stop() error {
	openMu.Lock()
	defer openMu.Unlock()

	stateMu.Lock()
	r, l := rt, listener
	if r == nil {
		stateMu.Unlock()
		return nil
	}
	// The fields are cleared FIRST and the mutex released, so a Status asked
	// during the close is answered immediately and honestly. Closing a node
	// flushes and joins goroutines; holding the state lock across it would
	// reintroduce exactly the blindness this split exists to remove.
	rt, listener = nil, nil
	state = StateUnavailable
	runtimeEpoch, apiToken, fingerprint = "", "", ""
	apiPort = 0
	stateMu.Unlock()

	if l != nil {
		_ = l.Close()
	}
	// The plane comes down before the node does. The host's sink is KEPT — a
	// stop is not a refusal, and the next Start re-arms the same sink — but a
	// runtime being closed must not be able to announce anything on its way
	// out, least of all to a host that has already been told it stopped.
	r.ArmNotifications(nil)
	r.Close()
	return nil
}

// Status is the Go half of the rig's status contract. The Java half adds
// package, uid and last_exit_reason (ApplicationExitInfo), which only the
// framework can answer.
//
// core_pid + process_started_at + proc_start_ticks are what AR-0c's
// background → return step compares before and after, so that a scenario
// satisfied by an unnoticed process restart is REPORTED as a process restart
// instead of passing as "the process was kept".
func Status() string {
	stateMu.Lock()
	defer stateMu.Unlock()

	mono, boot := clocks()
	s := map[string]any{
		"state":              state,
		"core_pid":           os.Getpid(),
		"process_started_at": processStartedAt.Format(time.RFC3339Nano),
		"proc_start_ticks":   procStartTicks,
		"go_version":         runtime.Version(),

		// BOTH clocks, because their DIFFERENCE is the measurement.
		//
		// Go's time.Since reads CLOCK_MONOTONIC, which on Android STOPS while
		// the device is suspended. CLOCK_BOOTTIME does not. Sampling both
		// before and after a Doze gives the suspended time directly —
		// (boot_delta − mono_delta) — rather than inferring it from a clock
		// that was asleep for the part being measured.
		//
		// That is not an abstract nicety: three places in the tree age things
		// with time.Since — the SyncClock calibration at node/listening.go,
		// the relay pool's idle close, and the attention debounce — and every
		// one of them will believe an overnight doze was minutes. This is the
		// instrument that turns that from a reading of the code into a number.
		"mono_ns": mono, // CLOCK_MONOTONIC — what time.Since sees
		"boot_ns": boot, // CLOCK_BOOTTIME  — includes suspend
	}
	// Three notification numbers, because "nothing appeared" has three
	// different causes — nothing armed, nothing produced, or a host that could
	// not keep up — and they need three different answers.
	for k, v := range notifyStatus() {
		s[k] = v
	}
	if lastError != "" {
		s["last_error"] = lastError
	}
	if rt != nil {
		s["runtime_epoch"] = runtimeEpoch
		s["data_dir"] = dataDir
		s["api_port"] = apiPort
		s["session_token"] = apiToken
		s["fingerprint"] = fingerprint
		s["started_at"] = startedAt.Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(s)
	if err != nil {
		return `{"state":"error","last_error":"status encode failed"}`
	}
	return string(b)
}

// readProcStartTicks returns field 22 of /proc/self/stat. The parse walks back
// from the closing parenthesis of the comm field rather than splitting on
// spaces, because a process name may itself contain spaces and parentheses —
// the classic way this parse goes wrong.
func readProcStartTicks() string {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return ""
	}
	line := string(b)
	rp := strings.LastIndex(line, ")")
	if rp < 0 || rp+2 >= len(line) {
		return ""
	}
	// After "comm)" the fields are: state(3) ppid(4) ... starttime(22).
	f := strings.Fields(line[rp+2:])
	if len(f) < 20 {
		return ""
	}
	return f[19] // field 22 overall
}

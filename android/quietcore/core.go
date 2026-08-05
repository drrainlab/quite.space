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
	"strconv"
	"strings"
	"sync"
	"time"

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

var (
	mu sync.Mutex

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
	mu.Lock()
	defer mu.Unlock()
	if rt != nil {
		return fmt.Errorf("quietcore: already running (data dir %s)", dataDir)
	}
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

	r, err := node.Open(dir, []byte(passphrase), name)
	if err != nil {
		lastError = err.Error()
		return err
	}

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

	rt, listener = r, l
	runtimeEpoch = hex.EncodeToString(epoch[:])
	dataDir, apiPort, apiToken = dir, port, api.Token()
	fingerprint = r.Principal.Fingerprint()
	startedAt = time.Now().UTC()
	lastError = ""
	return nil
}

// Stop closes the node and the listener. Safe to call when not running — the
// rig's stop command must be usable without first asking whether it needs to
// be, or every caller grows the same race.
func Stop() error {
	mu.Lock()
	defer mu.Unlock()
	if rt == nil {
		return nil
	}
	if listener != nil {
		_ = listener.Close()
	}
	rt.Close()
	rt, listener = nil, nil
	runtimeEpoch, apiToken, fingerprint = "", "", ""
	apiPort = 0
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
	mu.Lock()
	defer mu.Unlock()

	s := map[string]any{
		"state":              "stopped",
		"core_pid":           os.Getpid(),
		"process_started_at": processStartedAt.Format(time.RFC3339Nano),
		"proc_start_ticks":   procStartTicks,
		"go_version":         runtime.Version(),
	}
	if lastError != "" {
		s["last_error"] = lastError
	}
	if rt != nil {
		s["state"] = "running"
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

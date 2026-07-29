// The permanent Wails compatibility probe (DS-0).
//
// This is a canary, not a product: it mounts the REAL api.Handler() inside a
// Wails v3 AssetServer and walks the viability gates that decide whether the
// desktop shell (DS-3) can be built on this framework at all. It runs before
// every Wails version bump, when testing a fork, to produce minimal upstream
// reproductions, and in CI on macOS, Linux and Windows.
//
// The gates that matter are the ones a unit test cannot answer: does THIS
// WebView, on THIS platform, stream ranged audio, accept multipart uploads,
// record opus, export canvases — against our real handler, not a mock. The
// probe page exercises each and reports PASS/FAIL both on screen and to
// stdout, so a scripted run can capture the verdicts:
//
//	PROBE gate=api verdict=pass detail=...
//
// Native-side gates (tray, window close ≠ quit) are exercised from Go below;
// the ones needing a human (seek audibly, grant the mic) stay buttons on the
// page with their instructions next to them.
package main

import (
	"bytes"
	"embed"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	webui "github.com/drrainlab/quiet_places/clients/web-ui"
	"github.com/drrainlab/quiet_places/node"
)

//go:embed probe.html
var probeAssets embed.FS

// The probe's own node. A throwaway data dir and a fixed passphrase: the
// point is that the FULL runtime — scrypt unlock, log replay, the 57-route
// mux — lives inside the shell process, exactly as DS-3 will run it.
const probePassphrase = "wails-probe-passphrase"

func main() {
	dataDir := filepath.Join(os.TempDir(), "qp-wails-probe")
	rt, err := node.Open(dataDir, []byte(probePassphrase), "probe")
	if err != nil {
		log.Fatalf("probe: node.Open: %v", err)
	}
	defer rt.Close()

	api, err := node.NewAPIServer(rt, webui.FS())
	if err != nil {
		log.Fatalf("probe: api server: %v", err)
	}
	api.SetToken("probe-token")
	real := api.Handler() // built ONCE — it constructs a fresh mux per call

	// The asset router: /probe* is the harness, everything else is the real
	// application — UI and API — served with NO TCP listener anywhere. That
	// absence is itself gate 1: if these bytes arrive, they arrived through
	// the AssetServer transport.
	mux := http.NewServeMux()
	mux.HandleFunc("/probe", servePage)
	mux.HandleFunc("/probe/tone.wav", serveTone)
	mux.HandleFunc("/probe/upload", serveUpload)
	mux.HandleFunc("/probe/report", serveReport)
	mux.HandleFunc("/probe/echo-size", func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		fmt.Fprintf(w, `{"bytes":%d,"content_length":%d}`, n, r.ContentLength)
	})
	mux.Handle("/", real)

	// Request logging while the probe is young: which paths the WebView
	// actually asks for is the first diagnostic when a gate is silent.
	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("PROBE-REQ %s %s\n", r.Method, r.URL.Path)
		mux.ServeHTTP(w, r)
	})

	app := application.New(application.Options{
		Name:        "Quiet Spaces — Wails Probe",
		Description: "DS-0 permanent compatibility canary",
		Assets:      application.AssetOptions{Handler: logged},
	})

	// Gate: tray. Creating one and attaching a menu is the whole check —
	// platforms without tray support fail loudly here.
	tray := app.SystemTray.New()
	tray.SetLabel("QS probe")
	trayMenu := app.NewMenu()
	trayMenu.Add("Show probe").OnClick(func(_ *application.Context) { app.Show() })
	trayMenu.Add("Quit").OnClick(func(_ *application.Context) { app.Quit() })
	tray.SetMenu(trayMenu)
	report("tray", true, "system tray created with menu")

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Wails Probe — DS-0",
		Width:  980,
		Height: 760,
		URL:    "/probe",
	})

	// Gate: window close ≠ application quit. Closing the window hides it;
	// the tray brings it back; only the tray's Quit (or OS shutdown) ends
	// the process. This is the DS-3 lifecycle in miniature.
	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		win.Hide()
		e.Cancel()
		report("close-is-hide", true, "window close intercepted, app still running")
	})

	if err := app.Run(); err != nil {
		log.Fatalf("probe: %v", err)
	}
}

func report(gate string, ok bool, detail string) {
	verdict := "pass"
	if !ok {
		verdict = "FAIL"
	}
	fmt.Printf("PROBE gate=%s verdict=%s detail=%s\n", gate, verdict, detail)
}

func servePage(w http.ResponseWriter, r *http.Request) {
	b, _ := probeAssets.ReadFile("probe.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

// serveTone generates a 2-second 440Hz WAV in memory and serves it through
// http.ServeContent — which is what gives Range/206 semantics. The <audio>
// element's ability to SEEK it is the gate: WKWebView and WebView2 both
// refuse to seek media a server serves without ranges.
func serveTone(w http.ResponseWriter, r *http.Request) {
	const (
		rate = 8000
		secs = 2
		n    = rate * secs
	)
	pcm := make([]byte, 44+n*2)
	// RIFF/WAVE header, PCM16 mono.
	copy(pcm[0:], "RIFF")
	binary.LittleEndian.PutUint32(pcm[4:], uint32(36+n*2))
	copy(pcm[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(pcm[16:], 16)
	binary.LittleEndian.PutUint16(pcm[20:], 1)
	binary.LittleEndian.PutUint16(pcm[22:], 1)
	binary.LittleEndian.PutUint32(pcm[24:], rate)
	binary.LittleEndian.PutUint32(pcm[28:], rate*2)
	binary.LittleEndian.PutUint16(pcm[32:], 2)
	binary.LittleEndian.PutUint16(pcm[34:], 16)
	copy(pcm[36:], "data")
	binary.LittleEndian.PutUint32(pcm[40:], uint32(n*2))
	for i := 0; i < n; i++ {
		s := int16(6000 * math.Sin(2*math.Pi*440*float64(i)/rate))
		binary.LittleEndian.PutUint16(pcm[44+i*2:], uint16(s))
	}
	http.ServeContent(w, r, "tone.wav", time.Time{}, bytes.NewReader(pcm))
}

// serveUpload consumes a streamed multipart body the way the real block
// upload does — MaxBytesReader BEFORE any read, then MultipartReader part by
// part — and echoes what it measured.
func serveUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	total := int64(0)
	parts := 0
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		n, _ := io.Copy(io.Discard, p)
		total += n
		parts++
	}
	fmt.Fprintf(w, `{"parts":%d,"bytes":%d}`, parts, total)
}

// serveReport receives the page's verdicts and mirrors them to stdout in the
// same PROBE grammar the Go-side gates use.
func serveReport(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line != "" {
			fmt.Println("PROBE " + line)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

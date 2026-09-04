// Quite Space, as an application (DS-3).
//
// One process holding one node, with a window in front of it. The node is the
// same one `terminal ui` runs — same runtime, same 57-route API, same embedded
// interface — mounted inside the shell's AssetServer instead of behind a
// loopback port. ADR-011 is unchanged by that and is the reason it is allowed:
// the shell HOSTS the local HTTP API and the interface, it does not replace
// them with Go bindings. There is not one domain-shaped binding in this
// binary, and boundary_test.go is what keeps it that way.
//
// EXPERIMENTAL, and the builds say so. Neither the macOS nor the Linux
// artifact is signed by a developer certificate, so both arrive with the
// friction their platform gives an unidentified developer. That is a
// deliberate beta trade, not an oversight — see the bundle scripts, which
// print the exact instructions each platform needs.
package main

import (
	"context"
	"embed"
	"fmt"
	"io"
	"log"
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof on DefaultServeMux (debug flag only)
	"os"
	"time"

	webui "github.com/drrainlab/quiet_places/clients/web-ui"
	"github.com/drrainlab/quiet_places/cmd/desktop/internal/wailsx"
	"github.com/drrainlab/quiet_places/node"
)

// The brand marks, embedded so the binary is self-contained. The mono glyph is
// a TEMPLATE image (shape lives in the alpha channel; macOS recolours it to
// match the menubar); the colour glyph feeds the about box. The dock icon
// cannot come from here — that is the bundle's .icns.
//
//go:embed assets/tray-template.png assets/app-icon.png
var brand embed.FS

func main() {
	log.SetFlags(0)

	dataDir := node.DefaultDataDir()
	debug := false
	// Two flags, and both earn their place. --data is for running a second
	// node on the same machine to test two people talking; --debug opens the
	// web inspector and prints one line per request, which is the only way to
	// see inside a WebView from outside it. There is no other configuration
	// surface here.
	for i, a := range os.Args {
		switch a {
		case "--data":
			if i+1 < len(os.Args) {
				dataDir = os.Args[i+1]
			}
		case "--debug":
			debug = true
		}
	}

	// WHERE THE LOG GOES when nobody is watching stderr. Launched from
	// /Applications, this process has a stderr the window server discards,
	// and every verdict the node said — the relay that refused, the route
	// learned and not persisted — was gone before anybody could ask for it.
	// So the standard logger also lands in <data>/logs/node.log: rolling,
	// owner-only, and created only once the data directory exists (a fresh
	// machine's disk stays untouched until a passphrase is chosen). Only the
	// logger goes there — the --debug request lines stay on stdout, because
	// a durable file of what somebody read and when is a diary, not a log.
	nodeLog := node.NewRollingLog(dataDir)
	defer nodeLog.Close()
	log.SetOutput(io.MultiWriter(os.Stderr, nodeLog))

	shell, err := NewShell(dataDir, webui.FS())
	if err != nil {
		log.Fatalf("quite space: %v", err)
	}
	fmt.Println("data root:", dataDir)
	fmt.Println("log file:", nodeLog.Path())

	var handler http.Handler = shell
	if debug {
		handler = withRequestLog(shell)
		fmt.Println("debug: web inspector enabled, logging every request")
		// WHERE THE TIME GOES, when a person says "it runs warm".
		//
		// A shipped build is stripped, and `sample` renders every Go frame
		// as ??? — so an energy question that the outside cannot answer
		// stays a guess. pprof answers it with names. Loopback only, and
		// only under the flag that already opens the inspector and prints
		// every request: this is the debugging surface, not a new one.
		//
		//	go tool pprof -http=: http://127.0.0.1:6060/debug/pprof/profile?seconds=30
		go func() {
			fmt.Println("debug: profiler at http://127.0.0.1:6060/debug/pprof/")
			srv := &http.Server{
				Addr:              "127.0.0.1:6060",
				Handler:           http.DefaultServeMux, // net/http/pprof registers here
				ReadHeaderTimeout: 5 * time.Second,
			}
			if err := srv.ListenAndServe(); err != nil {
				log.Printf("quite space: profiler: %v", err)
			}
		}()
	}

	// OFF the UI thread, deliberately. node.Open is a scrypt derivation plus a
	// replay of every space's log — seconds on a large node — and a window
	// frozen for that long reads as a crash rather than as work.
	go func() {
		if err := shell.Await(context.Background()); err != nil {
			log.Printf("quite space: %v", err)
			return
		}
		fmt.Println("node open")
	}()

	icon, _ := brand.ReadFile("assets/app-icon.png")
	trayIcon, _ := brand.ReadFile("assets/tray-template.png")

	err = wailsx.Run(wailsx.Options{
		Name:        "Quite Space",
		Description: "A quiet place for the people you choose",
		Title:       "Quite Space",
		Width:       1240,
		Height:      840,
		URL:         shell.StartURL(),
		Handler:     handler,
		Icon:        icon,
		TrayIcon:    trayIcon,
		ShowLabel:   "Show Quite Space",
		QuitLabel:   "Quit",
		DevTools:    debug,
		OnAttend:    shell.SetForeground,
		OnQuit:      shell.Shutdown,
	})
	// Belt and braces: OnQuit already ran, and Shutdown is a sync.Once, so
	// this costs nothing and covers a run loop that returns an error before
	// ever reaching its own exit path.
	shell.Shutdown()
	if err != nil {
		log.Fatalf("quite space: %v", err)
	}
}

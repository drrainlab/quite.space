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
	"log"
	"net/http"
	"os"

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

	shell, err := NewShell(dataDir, webui.FS())
	if err != nil {
		log.Fatalf("quite space: %v", err)
	}
	fmt.Println("data root:", dataDir)

	var handler http.Handler = shell
	if debug {
		handler = withRequestLog(shell)
		fmt.Println("debug: web inspector enabled, logging every request")
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

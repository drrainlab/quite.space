// Package webui embeds the built web UI assets (ADR-011). The UI is a pure
// client of the local API: it contains no protocol logic and never talks to
// anything but 127.0.0.1.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed assets
var assets embed.FS

// FS returns the UI file tree rooted at its index.
func FS() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	return sub
}

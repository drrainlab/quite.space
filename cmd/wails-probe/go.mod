// The permanent Wails compatibility probe (DS-0). A NESTED module, and that
// nesting is the point: Wails needs CGO, ADR-011 mandates CGO_ENABLED=0 for
// the main binary, and Go excludes directories with their own go.mod from
// ./... — so the root build, vet and CI never touch this.
//
// EXACT pin, never latest (the plan's rule for an alpha dependency). Bump it
// only by editing this file deliberately, and re-run the probe checklist on
// every bump — that is what "permanent canary" means.
module github.com/drrainlab/quiet_places/cmd/wails-probe

go 1.25.6

replace github.com/drrainlab/quiet_places => ../..

require (
	github.com/drrainlab/quiet_places v0.0.0-00010101000000-000000000000
	github.com/wailsapp/wails/v3 v3.0.0-alpha2.119
)

require (
	github.com/adrg/xdg v0.5.3 // indirect
	github.com/cloudflare/circl v1.6.4 // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e // indirect
	go.bug.st/serial v1.8.0 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
)

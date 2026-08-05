// The AR-0 Android binding. A NESTED module, and the nesting is the point:
// gomobile bind needs CGO and an NDK, ADR-011 mandates CGO_ENABLED=0 for the
// main binary, and Go excludes directories with their own go.mod from ./... —
// so the root build, vet and CI never touch this. Same mechanism, same reason
// as cmd/wails-probe.
//
// This is a MEASUREMENT RIG, not the beginning of an Android app. It carries
// no product interface and never will: the web UI it serves is the one the
// node already embeds, reached over the ordinary local HTTP API (ADR-011's
// boundary), which is exactly what AR-0d needs to look at.
module github.com/drrainlab/quiet_places/android/quietcore

go 1.25.6

replace github.com/drrainlab/quiet_places => ../..

require (
	github.com/drrainlab/quiet_places v0.0.0-00010101000000-000000000000
	golang.org/x/sys v0.47.0
)

require (
	github.com/cloudflare/circl v1.6.4 // indirect
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e // indirect
	go.bug.st/serial v1.8.0 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/mobile v0.0.0-20260803200217-62cee1672c8e // indirect
	golang.org/x/mod v0.38.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	golang.org/x/tools v0.48.0 // indirect
)

tool golang.org/x/mobile/cmd/gobind

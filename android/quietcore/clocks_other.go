//go:build !linux

// Everywhere else — which in practice means a developer running `go build` on
// a Mac. There is no CLOCK_BOOTTIME to report, so nothing is reported: a zero
// reads as absent, and inventing a plausible number here would put a value in
// the suspended-time column of a platform that cannot suspend this way.
package quietcore

func clocks() (mono, boot int64) { return 0, 0 }

//go:build linux

// The two clocks whose DIFFERENCE is the measurement, on the only platform
// where the difference exists. GOOS=android satisfies the linux tag.
package quietcore

import "golang.org/x/sys/unix"

// clocks reads CLOCK_MONOTONIC and CLOCK_BOOTTIME in nanoseconds.
//
// CLOCK_MONOTONIC is what Go's time.Since reads, and on Android it STOPS while
// the device is suspended. CLOCK_BOOTTIME keeps counting. Sample both before
// and after a Doze and (boot_delta − mono_delta) is the suspended time,
// measured rather than inferred from a clock that was asleep for the part
// being measured.
//
// A clock the kernel will not answer for reports 0 rather than a fabricated
// value: a zero is visibly absent, an invented number is not.
func clocks() (mono, boot int64) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err == nil {
		mono = ts.Nano()
	}
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err == nil {
		boot = ts.Nano()
	}
	return
}

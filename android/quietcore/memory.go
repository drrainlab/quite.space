package quietcore

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// Resident-size readings, in kB, from procfs.
//
// VmHWM is the LIFETIME PEAK and is the only one of the two that catches a
// transient: the quick-link KDF holds 128 MiB for a fraction of a second, and
// anything sampled afterwards reports that it never happened. VmRSS is what
// settled. Both are needed, and reporting one as though it were the other is
// how a memory risk gets measured away.
//
// Zero where there is no procfs, and the callers render that as absent rather
// than as a reading of zero.

func currentRSSKB() uint64 { return statusKB("VmRSS:") }
func peakRSSKB() uint64    { return statusKB("VmHWM:") }

func statusKB(prefix string) uint64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		return v
	}
	return 0
}

// resetPeakRSS clears VmHWM, so the next peak reading describes the operation
// about to run rather than the largest thing this process has ever done.
// Linux 4.0+ exposes it at /proc/self/clear_refs value 5; where it is not
// supported this is a silent no-op, which is why peak and settled are always
// reported side by side instead of the reset being assumed to have worked.
func resetPeakRSS() {
	_ = os.WriteFile("/proc/self/clear_refs", []byte("5\n"), 0o200)
}

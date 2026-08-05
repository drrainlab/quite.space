// Thermal and power state, sampled before and after every run.
//
// WHY THIS EXISTS. The same three corpora, on the same phone, with the same
// binary, measured 128–141 ms of scrypt in one session and 177–183 ms an hour
// later. Nothing in the software had changed. Two things had: a node was
// running in the background (removed — the variance collapsed immediately),
// and the phone had spent that hour running memory-hard KDFs and was sitting
// at 52 °C with its big core pegged to 70% of its maximum frequency.
//
// A phone is not a server. Its clock rate is a function of how hard it was
// working a minute ago, and a report that quotes one number without the
// thermal state it was taken in is quoting a number that cannot be reproduced.
// So the state travels WITH the measurement, in the same spirit as the
// "OS cache uncontrolled" note: the harness will not claim a condition it did
// not control, but it will always say what the condition was.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type thermalState struct {
	CPUMilliC   int     // hottest CPU-ish thermal zone, milli-degrees
	BatteryMilC int     // battery temperature, milli-degrees
	BatteryPct  int     // charge level
	Charging    bool    //
	FreqRatio   float64 // current / max on the fastest core, 0 when unknown
	FastestKHz  int
	MaxKHz      int
	Available   bool
}

func readThermal() thermalState {
	var t thermalState

	// Hottest CPU-ish zone. Names differ across vendors, so the filter is by
	// substring rather than by an exact list nobody can keep current.
	if zones, err := filepath.Glob("/sys/class/thermal/thermal_zone*"); err == nil {
		for _, z := range zones {
			name := strings.TrimSpace(readFile(filepath.Join(z, "type")))
			if !strings.Contains(name, "cpu") && !strings.Contains(name, "apc") &&
				!strings.Contains(name, "cpuss") {
				continue
			}
			if v, err := strconv.Atoi(strings.TrimSpace(readFile(filepath.Join(z, "temp")))); err == nil && v > t.CPUMilliC {
				t.CPUMilliC, t.Available = v, true
			}
		}
	}

	// Battery. Its temperature matters as much as the die's: a warm battery is
	// what makes a phone throttle for minutes rather than seconds.
	//
	// On the Nothing Phone (1) these files are SELinux-restricted for the
	// shell user and simply do not read, so they are absent from the report
	// rather than guessed at. The CPU zone and the frequency ceiling below
	// carry the signal on their own — the ceiling especially, because a
	// lowered scaling_max_freq is the governor acting, not a core idling.
	for _, p := range []string{"/sys/class/power_supply/battery", "/sys/class/power_supply/bms"} {
		if v, err := strconv.Atoi(strings.TrimSpace(readFile(filepath.Join(p, "temp")))); err == nil && v != 0 {
			t.BatteryMilC, t.Available = v*100, true // decidegrees → millidegrees
		}
		if v, err := strconv.Atoi(strings.TrimSpace(readFile(filepath.Join(p, "capacity")))); err == nil && v != 0 {
			t.BatteryPct = v
		}
		if s := strings.TrimSpace(readFile(filepath.Join(p, "status"))); s != "" {
			t.Charging = s == "Charging" || s == "Full"
		}
	}

	// The fastest core's headroom. Sampled rather than averaged: what matters
	// is whether the ceiling has been lowered, not what the core happened to
	// be doing this instant.
	best := 0
	for c := 0; c < 12; c++ {
		base := fmt.Sprintf("/sys/devices/system/cpu/cpu%d/cpufreq", c)
		max, err := strconv.Atoi(strings.TrimSpace(readFile(filepath.Join(base, "cpuinfo_max_freq"))))
		if err != nil || max <= best {
			continue
		}
		cur, err := strconv.Atoi(strings.TrimSpace(readFile(filepath.Join(base, "scaling_max_freq"))))
		if err != nil {
			continue
		}
		best, t.MaxKHz, t.FastestKHz, t.Available = max, max, cur, true
	}
	if t.MaxKHz > 0 {
		t.FreqRatio = float64(t.FastestKHz) / float64(t.MaxKHz)
	}
	return t
}

func readFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func (t thermalState) String() string {
	if !t.Available {
		return "not readable on this platform"
	}
	parts := []string{}
	if t.CPUMilliC > 0 {
		parts = append(parts, fmt.Sprintf("cpu %.1f°C", float64(t.CPUMilliC)/1000))
	}
	if t.BatteryMilC > 0 {
		parts = append(parts, fmt.Sprintf("battery %.1f°C", float64(t.BatteryMilC)/1000))
	}
	if t.BatteryPct > 0 {
		s := fmt.Sprintf("%d%%", t.BatteryPct)
		if t.Charging {
			s += " charging"
		}
		parts = append(parts, s)
	}
	if t.MaxKHz > 0 {
		// scaling_max_freq below cpuinfo_max_freq is the thermal governor
		// having LOWERED the ceiling — the state that actually changes a
		// measurement, as opposed to a core merely idling.
		note := ""
		if t.FreqRatio < 0.999 {
			note = "  ← CEILING LOWERED"
		}
		parts = append(parts, fmt.Sprintf("fastest core %d/%d MHz (%.0f%%)%s",
			t.FastestKHz/1000, t.MaxKHz/1000, t.FreqRatio*100, note))
	}
	return strings.Join(parts, " · ")
}

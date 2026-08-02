// Writing firmware to a device (RB-2).
//
// We do NOT reimplement the ESP32 serial bootloader. esptool is the reference
// implementation, it is maintained alongside the silicon, and a hand-rolled
// version of it would be a subtle brick generator on somebody's only radio.
// So this file finds esptool, builds an argument list from a FlashPlan, and
// runs it — reporting what it ran, so a failure can be reproduced by hand.
//
// Two refusals worth naming, because both are tempting shortcuts:
//
//   - esptool is never installed for the person. A tool that silently pulls
//     an executable off the internet and runs it against their hardware has
//     made a decision that was theirs.
//   - a plan is never invented. Every offset comes from the release manifest
//     (firmware.go). If the manifest could not be read, there is no flashing,
//     not a guess at the usual numbers.
package meshtastic

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrNoEsptool is returned when the flashing tool is not on the PATH. The
// message names the two usual ways to get it and does not run either.
var ErrNoEsptool = errors.New(
	"esptool is not installed — it is what writes firmware to an ESP32.\n" +
		"  pip install esptool        (or: pipx install esptool)\n" +
		"  brew install esptool       (macOS)\n" +
		"Install it yourself and run this again; this command will not " +
		"download and run a program on your behalf.")

// FindEsptool locates the flashing tool. Both spellings exist in the wild.
func FindEsptool() (string, error) {
	for _, name := range []string{"esptool", "esptool.py"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", ErrNoEsptool
}

// FlashCommands renders the exact commands a plan will run, given where the
// files were saved. Pure, so it can be shown before anything happens and
// checked in a test without hardware.
//
// Erase comes first and is not optional for a first install: a board arriving
// with another vendor's firmware has partitions and NVS contents that mean
// nothing to Meshtastic, and leaving them behind produces failures that look
// like hardware faults.
func FlashCommands(tool, port string, plan FlashPlan, pathOf map[string]string) [][]string {
	erase := []string{tool, "--port", port, "erase-flash"}
	write := []string{tool, "--port", port, "write-flash"}
	for _, f := range plan.Files {
		write = append(write, fmt.Sprintf("0x%x", f.Offset), pathOf[f.Name])
	}
	return [][]string{erase, write}
}

// connectHint turns esptool's "could not connect" into the one thing that
// actually fixes it.
//
// esptool normally puts a board into download mode itself, by toggling DTR
// and RTS. That works through a USB-to-serial BRIDGE, which holds those lines
// in hardware. A board with NATIVE USB has no bridge: the USB device is
// produced by the firmware, so the automatic reset depends on the firmware
// still running — and a board that needs reflashing is usually one whose
// firmware does not.
//
// The board then enumerates, esptool opens the port, and nothing answers.
// Which is exactly the state that sends somebody looking for a broken cable.
func connectHint(output string) string {
	low := strings.ToLower(output)
	if !strings.Contains(low, "no serial data received") &&
		!strings.Contains(low, "failed to connect") {
		return ""
	}
	return "The board did not enter download mode on its own. On a board with " +
		"native USB\nthat is normal once its firmware has stopped running — " +
		"there is no USB-serial\nchip to pull the reset lines, so esptool " +
		"cannot do it for you.\n\n" +
		"Put it there by hand:\n" +
		"  1. hold BOOT (sometimes labelled IO0, or PRG)\n" +
		"  2. press and release RST (or EN) while still holding BOOT\n" +
		"  3. release BOOT\n\n" +
		"The port name usually CHANGES when it does — the bootloader is a " +
		"different\nUSB device from the firmware. Check with `ls /dev/cu.usb*` " +
		"and pass the new\nname to --port, or the flash will keep talking to a " +
		"port nothing is behind."
}

// Flash runs the plan. Output is streamed through `line` as it arrives —
// erasing and writing take minutes, and a progress-free wait is
// indistinguishable from a hang.
func Flash(ctx context.Context, tool, port string, plan FlashPlan,
	pathOf map[string]string, line func(string)) error {
	for _, argv := range FlashCommands(tool, port, plan, pathOf) {
		if line != nil {
			line("$ " + strings.Join(argv, " "))
		}
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		out, err := cmd.CombinedOutput()
		for _, l := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			if line != nil && strings.TrimSpace(l) != "" {
				line("  " + l)
			}
		}
		if err != nil {
			// argv[1] is "--port", so naming it reported that a FLAG had
			// failed. The subcommand is the operation somebody can act on.
			op := argv[len(argv)-1]
			for _, a := range argv {
				if a == "erase-flash" || a == "write-flash" {
					op = a
				}
			}
			if hint := connectHint(string(out)); hint != "" {
				return fmt.Errorf("%s failed: %w\n\n%s", op, err, hint)
			}
			return fmt.Errorf("%s failed: %w", op, err)
		}
	}
	return nil
}

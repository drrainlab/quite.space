package quietcore

// The passcode half of the Android binding.
//
// The lock screen in cmd/terminal is the CLI and desktop path: there, a
// gate takes the port before node.Open and asks for four digits. Android
// never runs that code — QuietActivity owns its own screen and calls
// Start() directly — so the shim has to expose the same mechanism for the
// Kotlin side to build its keypad on. These four functions are that.
//
// WHAT ANDROID SHOULD DO DIFFERENTLY, and it matters more here than on a
// laptop. kernel/passcode defends four digits with an expensive KDF and a
// durable attempt counter, which bounds somebody guessing at the screen —
// but a copy of passcode.json falls to offline guessing in about an hour,
// because nothing outside the process is enforcing anything.
//
// This app already has the answer to that in PassphraseVault: the Android
// Keystore holds a key the app cannot export, and the OS enforces its own
// attempt limits below us. The strong shape is therefore
//
//	PIN -> Keystore-held key -> passphrase
//
// with kernel/passcode's file used only where hardware is unavailable, or
// as the second factor beside it. BindPasscode below is the portable
// mechanism, not the recommendation; the recommendation is to wrap the key
// with the vault and let hardware do the rate limiting.
//
// Recorded here rather than in a plan because this file is what somebody
// will read when they wire the screen up.

import (
	"encoding/json"
	"errors"

	"github.com/drrainlab/quiet_places/kernel/passcode"
	"github.com/drrainlab/quiet_places/node"
)

// PasscodeStatus reports whether a code is bound in dir, as JSON:
//
//	{"bound":true,"digits":4,"attempts_left":10,"max_attempts":10}
//
// A directory with no code bound is not an error — it is the ordinary
// state, and the caller should ask for the passphrase.
func PasscodeStatus(dir string) string {
	st, err := passcode.Info(dir)
	if err != nil {
		return jsonOf(map[string]any{"bound": false, "error": err.Error()})
	}
	return jsonOf(map[string]any{
		"bound": st.Bound, "digits": st.Digits,
		"attempts_left": st.AttemptsLeft, "max_attempts": passcode.MaxAttempts,
	})
}

// BindPasscode seals passphrase under code, after proving the passphrase
// actually opens dir. The proof is not optional: sealing an unverified
// passphrase produces a code that "works" while opening nothing, and the
// person finds out at the next launch with no way left to tell which of
// the two was wrong.
func BindPasscode(dir, code, passphrase string) error {
	if err := node.VerifyPassphrase(dir, []byte(passphrase)); err != nil {
		return err
	}
	return passcode.Bind(dir, code, []byte(passphrase))
}

// UnwrapPasscode returns the passphrase for a correct code, so the caller
// can hand it straight to Start().
//
// The error is one of four, and the screen should say four different
// things: no code bound, malformed input, wrong code (with attempts left,
// read separately from PasscodeStatus), or locked out — at which point the
// shortcut has erased itself and only the passphrase remains. Losing the
// code costs a person nothing; that is worth saying on the screen.
func UnwrapPasscode(dir, code string) (string, error) {
	pass, err := passcode.Unwrap(dir, code)
	if err != nil {
		return "", err
	}
	return string(pass), nil
}

// PasscodeErrorKind maps an error from UnwrapPasscode to a stable string,
// because gomobile flattens errors to their message and a screen must not
// branch on English prose.
func PasscodeErrorKind(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, passcode.ErrNoPasscode):
		return "none"
	case errors.Is(err, passcode.ErrBadCode):
		return "malformed"
	case errors.Is(err, passcode.ErrLockedOut):
		return "locked_out"
	case errors.Is(err, passcode.ErrWrongPasscode):
		return "wrong"
	}
	return "error"
}

// ForgetPasscode removes the shortcut. It never removes access: the
// passphrase still opens the directory, and this is the action behind a
// "forget my code" button.
func ForgetPasscode(dir string) error { return passcode.Forget(dir) }

// GeneratePassphrase mints one from the frozen 2048-word list — six words,
// 66 bits. For a first run that wants a code and no typing: generate,
// create the directory with it, bind the code, and SHOW the words once so
// they can be written down. It is what a backup is encrypted with, and
// nobody can reset it.
func GeneratePassphrase() (string, error) { return passcode.Generate() }

func jsonOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"bound":false,"error":"encode"}`
	}
	return string(b)
}

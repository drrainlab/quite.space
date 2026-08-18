// Package passcode binds a short numeric code to the real passphrase, so a
// person can get back into their own device with four digits instead of
// typing a sentence.
//
// WHAT THIS IS AND IS NOT, because the difference is the whole design:
//
// Four digits are 10 000 possibilities — about 13 bits. No key derivation
// makes that a secret. What the expensive KDF below buys is TIME, not
// safety, and the amount is measured rather than hoped for:
// TestTheRealKDFIsActuallyExpensive reports 373 ms and 128 MiB per guess on
// an M-series laptop, so the whole code space falls in about AN HOUR
// single-threaded. Memory-hardness makes parallelising it expensive but not
// impossible. One hour is the honest number, and the interface says so
// rather than implying a PIN is a password.
//
// So the threat this defends against is precise: somebody who picks up
// your unlocked laptop, or your phone, and starts guessing. Against that
// it works — the attempt counter below is durable and self-destructs, so
// they get ten tries and then the shortcut is gone.
//
// The threat it does NOT defend against is somebody who copies this file
// to a machine of their own. There is no rate limiting once the bytes have
// left, and hours is hours. The answer is to wrap these bytes again under
// a key the hardware will not hand out — which is a HOST's job, not this
// package's: it happens outside, around the blob Seal and Open pass, and
// this file stays the same on every platform.
//
// ANDROID HAS IT. PasscodeVault seals this file's bytes under an
// AndroidKeyStore key that cannot be exported, so the offline grind does
// not exist there: what remains is guessing at the screen, which is what
// the attempt counter is for.
//
// MACOS DOES NOT, AND THE REASON IS MEASURED RATHER THAN ASSUMED. Probed
// on 2026-08-18 against an unsigned build, which is what the beta ships:
//
//	data-protection keychain      errSecMissingEntitlement (-34018)
//	permanent Secure Enclave key  errSecMissingEntitlement (-34018)
//	ad-hoc signature carrying     process killed at launch (SIGKILL)
//	  keychain-access-groups
//	legacy keychain, default ACL  works — but the ACL is bound to the
//	                              binary's code signature, so a build
//	                              differing only in cdhash raises a GUI
//	                              prompt. Measured with two such builds:
//	                              the second one blocked. That would mean
//	                              asking permission after every update.
//
// So the door is a DEVELOPER ID SIGNATURE plus entitlements, not more
// code: with one, the first two rows work and the wrap is a small piece of
// work around Seal and Open. Until then macOS keeps the honest number
// above — about an hour for four digits, once the file has been copied —
// and nothing in the interface says otherwise.
//
// THE PASSPHRASE REMAINS THE REAL SECRET. The passcode is a shortcut to
// it, never a replacement: the passphrase alone opens the data directory,
// it is what a backup is encrypted with, and it survives this file being
// deleted. Losing the passcode costs a person nothing. Losing the
// passphrase costs them everything, which is why Generate hands back
// something a person can actually write on paper.
package passcode

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"

	"github.com/drrainlab/quiet_places/protocol/quicklink"
)

// FileName is where the wrap lives, beside the keystore rather than inside
// it — it must be readable BEFORE anything is unlocked, which is exactly
// what the keystore is not. Plain JSON in the data directory follows the
// relays.json / quicklinks.json precedent.
const FileName = "passcode.json"

const (
	// The expensive profile (1<<17 = 128 MiB, ~0.5-1 s), not the keystore's
	// cheaper one. The keystore's parameters are sized for a real passphrase
	// where the secret carries the entropy; here the secret carries almost
	// none, so every millisecond of derivation is doing the work instead.
	// Same numbers as the quicklink seal, and for the same reason.
	scryptN = 1 << 17
	scryptR = 8
	scryptP = 1

	// MaxAttempts is what a person gets before the shortcut destroys itself.
	// Ten rather than three: this locks out a forgetful owner far more often
	// than it stops an attacker (who is bounded by the KDF, not by this),
	// and the cost of being wrong is a person locked out of their own
	// device. They still have the passphrase.
	MaxAttempts = 10

	// MinDigits / MaxDigits bound the code itself. Four is the default the
	// interface offers; longer is strictly better and costs nothing here.
	MinDigits = 4
	MaxDigits = 12

	// GeneratedWords is the length of a generated passphrase. Six words from
	// the frozen 2048-word list is 66 bits — enough that the passphrase is
	// never the weak end, and short enough to copy onto paper without
	// resentment.
	GeneratedWords = 6
)

var (
	// ErrNoPasscode: no shortcut is bound on this device. Not a failure —
	// it is the ordinary state of a data directory that has never been
	// given one, and the caller should ask for the passphrase.
	ErrNoPasscode = errors.New("passcode: no passcode is bound on this device")
	// ErrWrongPasscode: the digits did not open it. An attempt was spent.
	ErrWrongPasscode = errors.New("passcode: wrong code")
	// ErrLockedOut: the attempts ran out and the wrap destroyed itself. The
	// data is untouched — the passphrase still opens it.
	ErrLockedOut = errors.New("passcode: too many attempts, the shortcut is gone")
	// ErrBadCode: the input is not a code we would ever have bound.
	ErrBadCode = errors.New("passcode: a code is 4 to 12 digits")
)

// wrap is the on-disk form. The KDF parameters travel with it so a file
// written by an older build stays readable after they are raised.
type wrap struct {
	Version      int    `json:"version"`
	Digits       int    `json:"digits"`
	N            int    `json:"scrypt_n"`
	R            int    `json:"scrypt_r"`
	P            int    `json:"scrypt_p"`
	Salt         []byte `json:"salt"`
	Nonce        []byte `json:"nonce"`
	Sealed       []byte `json:"sealed"`
	AttemptsLeft int    `json:"attempts_left"`
	CreatedAt    int64  `json:"created_at"`
}

// Status is what a caller may know without holding the code.
type Status struct {
	Bound        bool
	Digits       int
	AttemptsLeft int
	CreatedAt    time.Time
}

func path(dataDir string) string { return filepath.Join(dataDir, FileName) }

// ---- test seams ----
//
// The production KDF is 128 MiB and most of a second per call. A test
// suite that paid that price for every case would take eleven minutes and
// stop being run, so the parameters are variables the tests can lower —
// and one test measures the REAL ones so that lowering them here can never
// quietly become lowering them everywhere.
//
// deriveProbe runs inside the derivation. It exists for the one property
// that cannot be observed from outside: that the attempt is already
// durable on disk while the guess is still being computed.
var (
	curN, curR, curP = scryptN, scryptR, scryptP
	deriveProbe      func()
	nowFunc          = time.Now
)

func setParams(n, r, p int) { curN, curR, curP = n, r, p }
func setProbe(fn func())    { deriveProbe = fn }

// ValidCode reports whether a code is one this package would bind — the
// same rule Bind applies, exported so a caller can refuse at the field
// rather than after a derivation somebody waited a second for. Exported
// and unexported must stay one function, not two agreeing ones.
func ValidCode(code string) error { return validCode(code) }

// validCode refuses anything that is not the shape we bind. Digits only:
// a code with a letter in it is a passphrase somebody typed in the wrong
// box, and telling them so beats spending a second deriving from it.
func validCode(code string) error {
	if len(code) < MinDigits || len(code) > MaxDigits {
		return ErrBadCode
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return ErrBadCode
		}
	}
	return nil
}

func derive(code string, w *wrap) ([]byte, error) {
	if deriveProbe != nil {
		deriveProbe()
	}
	return scrypt.Key([]byte(code), w.Salt, w.N, w.R, w.P, chacha20poly1305.KeySize)
}

// Generate returns a fresh passphrase drawn from the project's frozen
// wordlist — the same 2048 words the five-word quicklink uses, chosen for
// unique four-letter prefixes and no homophone pairs, which is what makes
// one safe to read down a telephone.
//
// Words rather than characters because this passphrase has a second job:
// it is what a backup is encrypted with, and the person will one day have
// to type it from a piece of paper.
func Generate() (string, error) {
	parts := make([]string, GeneratedWords)
	for i := range parts {
		n, err := randIndex(len(quicklink.Words))
		if err != nil {
			return "", err
		}
		parts[i] = quicklink.Words[n]
	}
	return strings.Join(parts, "-"), nil
}

// randIndex draws below n without modulo bias.
func randIndex(n int) (int, error) {
	if n <= 0 {
		return 0, errors.New("passcode: empty wordlist")
	}
	max := uint32(n)
	limit := ^uint32(0) - (^uint32(0) % max) // largest multiple of max
	var b [4]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		v := binary.BigEndian.Uint32(b[:])
		if v < limit {
			return int(v % max), nil
		}
	}
}

// Seal returns the wrap as bytes instead of writing it, for a caller that
// has somewhere better to put it than a file.
//
// Android is that caller. There, this blob goes inside a second envelope
// whose key lives in the AndroidKeyStore and cannot leave the device —
// which changes the threat model completely: a copied file is then not
// merely expensive to attack, it is unattackable off the device, because
// the outer key is not in it. The hour that four digits buy on a laptop
// becomes infinity on a phone, and the attempt counter goes back to being
// what it should be, a bound on somebody holding the unlocked handset.
//
// The attempt counter travels in the blob; whoever stores it is
// responsible for storing it back after each try. On Android that is the
// vault, and it has the same durability obligation this package's own
// file writer has: record the spend BEFORE the guess is computed.
func Seal(code string, passphrase []byte) ([]byte, error) {
	w, err := newWrap(code, passphrase)
	if err != nil {
		return nil, err
	}
	return json.Marshal(w)
}

// Open is Seal's inverse over bytes. It returns the passphrase and the
// blob to store back — the attempt counter inside it has moved, whether
// the guess was right (restored) or wrong (spent).
//
// The caller MUST persist the returned blob before acting on the result,
// including on the error path: that is where the spend is recorded, and
// dropping it on failure is exactly the bug that turns ten tries into
// unlimited ones. The blob is never nil, deliberately — at lockout it
// comes back as a spent tombstone, so a caller that always stores what it
// is handed is correct, and one that deletes on ErrLockedOut is correct
// too. There is no branch here that quietly reopens the door.
func Open(code string, blob []byte) (pass []byte, updated []byte, err error) {
	if err := validCode(code); err != nil {
		return nil, blob, err
	}
	var w wrap
	if err := json.Unmarshal(blob, &w); err != nil {
		return nil, blob, fmt.Errorf("passcode: unreadable: %w", err)
	}
	if w.AttemptsLeft <= 0 {
		return nil, blob, ErrLockedOut // already a tombstone; storing it again is fine
	}
	w.AttemptsLeft--
	spent := w.AttemptsLeft

	key, err := derive(code, &w)
	if err != nil {
		return nil, blob, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, blob, err
	}
	pass, err = aead.Open(nil, w.Nonce, w.Sealed, nil)
	if err != nil {
		if spent <= 0 {
			// A TOMBSTONE, not nil. Returning nil here would have made the
			// safe path the unusual one: a caller that simply stores
			// whatever it is handed would have kept the PREVIOUS blob, the
			// one with an attempt still in it, and walked straight back
			// through the lockout. Handing back a spent wrap means storing
			// it is correct, deleting it is correct, and doing nothing is
			// merely untidy — every branch a caller might take is safe.
			out, _ := json.Marshal(&w)
			return nil, out, ErrLockedOut
		}
		out, _ := json.Marshal(&w)
		return nil, out, ErrWrongPasscode
	}
	w.AttemptsLeft = MaxAttempts
	out, _ := json.Marshal(&w)
	return pass, out, nil
}

// newWrap is the sealing shared by Bind and Seal, so the two can never
// disagree about the profile, the nonce or the budget.
func newWrap(code string, passphrase []byte) (*wrap, error) {
	if err := validCode(code); err != nil {
		return nil, err
	}
	if len(passphrase) == 0 {
		return nil, errors.New("passcode: refusing to bind an empty passphrase")
	}
	w := &wrap{
		Version: 1, Digits: len(code),
		N: curN, R: curR, P: curP,
		Salt: make([]byte, 32), Nonce: make([]byte, chacha20poly1305.NonceSizeX),
		AttemptsLeft: MaxAttempts, CreatedAt: nowFunc().Unix(),
	}
	if _, err := rand.Read(w.Salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(w.Nonce); err != nil {
		return nil, err
	}
	key, err := derive(code, w)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	w.Sealed = aead.Seal(nil, w.Nonce, passphrase, nil)
	return w, nil
}

// Bind seals passphrase under code and writes it to the data directory,
// replacing any shortcut already there. The caller must have proven the
// passphrase actually opens the directory first — this package seals what
// it is handed and cannot tell a real passphrase from a typo.
func Bind(dataDir, code string, passphrase []byte) error {
	w, err := newWrap(code, passphrase)
	if err != nil {
		return err
	}
	return write(dataDir, w)
}

// Unwrap returns the passphrase, or an error that says which kind of no it
// is.
//
// THE ATTEMPT IS SPENT BEFORE IT IS TRIED, and the spend is on disk before
// the derivation starts. A counter written after a failed attempt is not a
// counter: kill the process during the KDF's second of work and it never
// records anything, which turns ten tries into unlimited tries for anyone
// willing to script it. So the order is: read, decrement, persist, derive,
// and only restore the count once the answer is known to be right.
func Unwrap(dataDir, code string) ([]byte, error) {
	if err := validCode(code); err != nil {
		return nil, err
	}
	w, err := read(dataDir)
	if err != nil {
		return nil, err
	}
	if w.AttemptsLeft <= 0 {
		_ = Forget(dataDir)
		return nil, ErrLockedOut
	}

	w.AttemptsLeft--
	spent := w.AttemptsLeft
	if err := write(dataDir, w); err != nil {
		return nil, fmt.Errorf("passcode: could not record the attempt: %w", err)
	}

	key, err := derive(code, w)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	pass, err := aead.Open(nil, w.Nonce, w.Sealed, nil)
	if err != nil {
		// Wrong code — or a tampered file, which is indistinguishable here
		// and is treated the same way on purpose: both mean "this did not
		// open", and neither is worth telling an attacker apart.
		if spent <= 0 {
			_ = Forget(dataDir)
			return nil, ErrLockedOut
		}
		return nil, ErrWrongPasscode
	}

	// Right code: the budget is whole again.
	if w.AttemptsLeft != MaxAttempts {
		w.AttemptsLeft = MaxAttempts
		_ = write(dataDir, w)
	}
	return pass, nil
}

// Info reports what is knowable without the code. Never returns the shape
// of the secret, only whether a shortcut exists and how much rope is left.
func Info(dataDir string) (Status, error) {
	w, err := read(dataDir)
	if errors.Is(err, ErrNoPasscode) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, err
	}
	return Status{
		Bound: true, Digits: w.Digits, AttemptsLeft: w.AttemptsLeft,
		CreatedAt: time.Unix(w.CreatedAt, 0),
	}, nil
}

// Forget removes the shortcut. The data directory is untouched and the
// passphrase still opens it — this takes away a convenience, never access.
func Forget(dataDir string) error {
	err := os.Remove(path(dataDir))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func read(dataDir string) (*wrap, error) {
	data, err := os.ReadFile(path(dataDir))
	if os.IsNotExist(err) {
		return nil, ErrNoPasscode
	}
	if err != nil {
		return nil, err
	}
	var w wrap
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("passcode: unreadable: %w", err)
	}
	if w.Version != 1 || w.N <= 0 || w.R <= 0 || w.P <= 0 ||
		len(w.Salt) == 0 || len(w.Nonce) != chacha20poly1305.NonceSizeX || len(w.Sealed) == 0 {
		return nil, fmt.Errorf("passcode: unreadable: malformed wrap")
	}
	return &w, nil
}

// write replaces the file atomically and syncs before the rename, so a
// power cut cannot leave a half-written attempt counter — the one field
// whose loss would matter.
func write(dataDir string, w *wrap) error {
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	tmp := path(dataDir) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path(dataDir))
}

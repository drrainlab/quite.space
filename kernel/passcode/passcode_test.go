package passcode

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The tests use a deliberately cheap KDF: the real one is 128 MiB and most
// of a second per attempt, which is the point in production and eleven
// minutes of nothing in a test suite. Every test that is about the KDF's
// COST says so and is skipped in short mode instead.
func cheap(t *testing.T) {
	t.Helper()
	n, r, p := scryptN, scryptR, scryptP
	setParams(1<<10, 8, 1)
	t.Cleanup(func() { setParams(n, r, p) })
}

func TestABoundCodeReturnsThePassphrase(t *testing.T) {
	cheap(t)
	dir := t.TempDir()
	want := []byte("moss-ember-tide-wren-slate-drift")

	if err := Bind(dir, "4917", want); err != nil {
		t.Fatalf("bind: %v", err)
	}
	got, err := Unwrap(dir, "4917")
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNoPasscodeIsAnOrdinaryStateNotAFailure(t *testing.T) {
	dir := t.TempDir()
	st, err := Info(dir)
	if err != nil {
		t.Fatalf("Info on a fresh directory must not error: %v", err)
	}
	if st.Bound {
		t.Fatal("a fresh directory reports a bound passcode")
	}
	if _, err := Unwrap(dir, "0000"); !errors.Is(err, ErrNoPasscode) {
		t.Fatalf("want ErrNoPasscode, got %v", err)
	}
}

// The headline: an attempt is spent on disk BEFORE the derivation runs, so
// killing the process mid-guess costs the attacker a try rather than
// handing them a free one. Without that ordering, ten attempts is
// unlimited attempts to anyone willing to script a SIGKILL.
func TestAnAttemptIsSpentBeforeItIsTried(t *testing.T) {
	cheap(t)
	dir := t.TempDir()
	if err := Bind(dir, "1234", []byte("real")); err != nil {
		t.Fatal(err)
	}

	// Observe the counter from inside the derivation itself.
	var duringDerive int
	setProbe(func() {
		st, err := Info(dir)
		if err != nil {
			t.Errorf("info during derive: %v", err)
			return
		}
		duringDerive = st.AttemptsLeft
	})
	t.Cleanup(func() { setProbe(nil) })

	if _, err := Unwrap(dir, "9999"); !errors.Is(err, ErrWrongPasscode) {
		t.Fatalf("want ErrWrongPasscode, got %v", err)
	}
	if duringDerive != MaxAttempts-1 {
		t.Fatalf("the attempt was not durable before the KDF ran: saw %d left, want %d",
			duringDerive, MaxAttempts-1)
	}
}

func TestTheRightCodeRestoresTheBudget(t *testing.T) {
	cheap(t)
	dir := t.TempDir()
	if err := Bind(dir, "1234", []byte("real")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := Unwrap(dir, "0000"); !errors.Is(err, ErrWrongPasscode) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if st, _ := Info(dir); st.AttemptsLeft != MaxAttempts-3 {
		t.Fatalf("after 3 wrong tries: %d left, want %d", st.AttemptsLeft, MaxAttempts-3)
	}
	if _, err := Unwrap(dir, "1234"); err != nil {
		t.Fatal(err)
	}
	if st, _ := Info(dir); st.AttemptsLeft != MaxAttempts {
		t.Fatalf("a correct code must restore the budget: %d left", st.AttemptsLeft)
	}
}

func TestRunningOutDestroysTheShortcutAndNothingElse(t *testing.T) {
	cheap(t)
	dir := t.TempDir()
	if err := Bind(dir, "1234", []byte("real")); err != nil {
		t.Fatal(err)
	}
	// The exact sequence matters, so it is asserted rather than looped over:
	// the first MaxAttempts-1 guesses are ordinary refusals, and the LAST one
	// is the one that spends the final attempt and takes the shortcut with
	// it. A test that only checked the end state would not notice the
	// lockout arriving an attempt early or late.
	for i := 0; i < MaxAttempts-1; i++ {
		if _, err := Unwrap(dir, "0000"); !errors.Is(err, ErrWrongPasscode) {
			t.Fatalf("guess %d: want ErrWrongPasscode, got %v", i+1, err)
		}
	}
	if _, err := Unwrap(dir, "0000"); !errors.Is(err, ErrLockedOut) {
		t.Fatalf("guess %d must be the lockout, got %v", MaxAttempts, err)
	}
	if _, err := os.Stat(path(dir)); !os.IsNotExist(err) {
		t.Fatal("the wrap survived lockout — it must destroy itself")
	}
	// And the right code no longer helps, because there is nothing to open.
	if _, err := Unwrap(dir, "1234"); !errors.Is(err, ErrNoPasscode) {
		t.Fatalf("after lockout the state is 'no passcode', got %v", err)
	}
	st, err := Info(dir)
	if err != nil || st.Bound {
		t.Fatalf("Info after lockout: %+v %v", st, err)
	}
}

func TestOnlyDigitsAndOnlyASensibleLength(t *testing.T) {
	cheap(t)
	dir := t.TempDir()
	for _, bad := range []string{"", "123", "12a4", "1 34", "abcd",
		"1234567890123", "-123", "١٢٣٤"} {
		if err := Bind(dir, bad, []byte("x")); !errors.Is(err, ErrBadCode) {
			t.Fatalf("Bind(%q) = %v, want ErrBadCode", bad, err)
		}
	}
	// A passphrase typed into the code box is refused before the KDF runs,
	// rather than costing a second and an attempt.
	if err := Bind(dir, "1234", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := Unwrap(dir, "correct-horse-battery-staple"); !errors.Is(err, ErrBadCode) {
		t.Fatalf("want ErrBadCode, got %v", err)
	}
	if st, _ := Info(dir); st.AttemptsLeft != MaxAttempts {
		t.Fatal("a malformed code must not spend an attempt")
	}
}

func TestRebindingReplacesAndResets(t *testing.T) {
	cheap(t)
	dir := t.TempDir()
	if err := Bind(dir, "1111", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := Unwrap(dir, "0000"); !errors.Is(err, ErrWrongPasscode) {
		t.Fatal(err)
	}
	if err := Bind(dir, "2222", []byte("second")); err != nil {
		t.Fatal(err)
	}
	if st, _ := Info(dir); st.AttemptsLeft != MaxAttempts {
		t.Fatalf("rebinding must reset the budget, %d left", st.AttemptsLeft)
	}
	if _, err := Unwrap(dir, "1111"); !errors.Is(err, ErrWrongPasscode) {
		t.Fatal("the old code still opens it")
	}
	got, err := Unwrap(dir, "2222")
	if err != nil || string(got) != "second" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestATamperedWrapDoesNotOpen(t *testing.T) {
	cheap(t)
	dir := t.TempDir()
	if err := Bind(dir, "1234", []byte("real")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path(dir))
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte inside the base64 of the sealed field.
	s := string(data)
	i := strings.Index(s, `"sealed": "`) + len(`"sealed": "`)
	b := []byte(s)
	if b[i] == 'A' {
		b[i] = 'B'
	} else {
		b[i] = 'A'
	}
	if err := os.WriteFile(path(dir), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Unwrap(dir, "1234"); err == nil {
		t.Fatal("a tampered wrap opened")
	}
}

func TestForgetTakesTheShortcutAndNotTheAccess(t *testing.T) {
	cheap(t)
	dir := t.TempDir()
	if err := Bind(dir, "1234", []byte("real")); err != nil {
		t.Fatal(err)
	}
	if err := Forget(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Unwrap(dir, "1234"); !errors.Is(err, ErrNoPasscode) {
		t.Fatalf("want ErrNoPasscode, got %v", err)
	}
	if err := Forget(dir); err != nil {
		t.Fatalf("Forget must be idempotent: %v", err)
	}
}

func TestGeneratedPassphrasesAreFromTheFrozenListAndDoNotRepeat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p, err := Generate()
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(p, "-")
		if len(parts) != GeneratedWords {
			t.Fatalf("got %d words, want %d: %q", len(parts), GeneratedWords, p)
		}
		for _, w := range parts {
			if w == "" || strings.ContainsAny(w, " \t") {
				t.Fatalf("bad word %q in %q", w, p)
			}
		}
		if seen[p] {
			t.Fatalf("Generate repeated itself within 50 draws: %q", p)
		}
		seen[p] = true
	}
}

// The cost is the security property, so it is measured rather than
// asserted in a comment. Skipped in short mode because it is a second of
// deliberate work.
func TestTheRealKDFIsActuallyExpensive(t *testing.T) {
	if testing.Short() {
		t.Skip("measures the production KDF; -short")
	}
	dir := t.TempDir()
	if err := Bind(dir, "1234", []byte("real")); err != nil {
		t.Fatal(err)
	}
	start := nowFunc()
	if _, err := Unwrap(dir, "0000"); !errors.Is(err, ErrWrongPasscode) {
		t.Fatal(err)
	}
	elapsed := nowFunc().Sub(start)
	// A guess that costs less than a tenth of a second turns the whole
	// 10 000-code space into minutes. If a future change lowers the
	// parameters, this fails and says why.
	if elapsed < 100_000_000 /* 100ms */ {
		t.Fatalf("one guess cost %v — too cheap; 10 000 codes would fall in minutes", elapsed)
	}
	t.Logf("one guess: %v  (10 000 codes ≈ %v single-threaded)", elapsed, elapsed*10000)
}

// A crash between the spend and the answer must not refund the attempt.
// Run in a subprocess because the point is a process that dies.
func TestAKillDuringTheGuessStillCostsTheAttempt(t *testing.T) {
	if os.Getenv("PASSCODE_CRASH_CHILD") == "1" {
		dir := os.Getenv("PASSCODE_CRASH_DIR")
		setParams(1<<10, 8, 1)
		setProbe(func() { os.Exit(7) }) // die inside the derivation
		_, _ = Unwrap(dir, "0000")
		return
	}
	cheap(t)
	dir := t.TempDir()
	if err := Bind(dir, "1234", []byte("real")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestAKillDuringTheGuessStillCostsTheAttempt")
	cmd.Env = append(os.Environ(), "PASSCODE_CRASH_CHILD=1", "PASSCODE_CRASH_DIR="+dir)
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 {
		t.Fatalf("child did not die where expected: %v", err)
	}
	st, err := Info(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.AttemptsLeft != MaxAttempts-1 {
		t.Fatalf("a killed guess refunded itself: %d left, want %d",
			st.AttemptsLeft, MaxAttempts-1)
	}
}

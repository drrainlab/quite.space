package node

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The log follows the data. Nothing on disk before the data directory
// exists — and nothing lost either: the lines from before first run land
// in the file, in order, the moment there is one.
func TestTheLogFollowsTheData(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "not-yet")
	l := NewRollingLog(dataDir)
	defer l.Close()

	if _, err := l.Write([]byte("quite space: first line, before anybody chose a passphrase\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("a log line created the data directory: %v", err)
	}

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Write([]byte("node open")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatalf("the file is not where Path says: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "first line") || !strings.Contains(lines[1], "node open") {
		t.Fatalf("the file does not hold both lines in order:\n%s", b)
	}
	stamp := regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ `)
	for _, ln := range lines {
		if !stamp.MatchString(ln) {
			t.Fatalf("a line is not stamped with a UTC time: %q", ln)
		}
	}

	fi, err := os.Stat(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("log file mode = %o, want 0600 — this is the owner's diary of verdicts, nobody else's", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(l.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("logs dir mode = %o, want 0700", di.Mode().Perm())
	}
}

// Rotation by size keeps the whole history bounded: one live file under
// the cap, a fixed number of generations behind it, nothing older.
func TestTheLogRotatesBySizeAndForgetsTheOldest(t *testing.T) {
	dataDir := t.TempDir()
	l := NewRollingLog(dataDir)
	defer l.Close()
	l.maxBytes = 512
	l.keep = 2
	clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return clock }

	line := []byte("relay: something the owner should be able to read back later\n")
	for i := 0; i < 200; i++ {
		clock = clock.Add(time.Second)
		if _, err := l.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	live, err := os.Stat(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if live.Size() > 512 {
		t.Fatalf("the live file is %d bytes, over the %d cap", live.Size(), 512)
	}
	for _, gen := range []string{".1", ".2"} {
		fi, err := os.Stat(l.Path() + gen)
		if err != nil {
			t.Fatalf("generation %s missing: %v", gen, err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("generation %s mode = %o, want 0600", gen, fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(l.Path() + ".3"); !os.IsNotExist(err) {
		t.Fatalf("a generation past keep survived: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(l.Path()))
	if len(entries) != 3 {
		t.Fatalf("logs/ holds %d files, want live + 2 generations", len(entries))
	}
	// The newest line is in the live file; the oldest surviving one is in
	// the last generation — the order a person reads them in.
	b1, _ := os.ReadFile(l.Path())
	b2, _ := os.ReadFile(l.Path() + ".2")
	if !bytes.Contains(b1, []byte("2026-09-04T12:03:20Z")) || bytes.Contains(b2, []byte("12:03:20Z")) {
		t.Fatalf("generations are out of order:\nlive:\n%s\noldest:\n%s", b1, b2)
	}
}

// After Close the writer is inert — a late line from a goroutine that
// outlives shutdown is dropped, not a panic.
func TestALateLineAfterCloseIsDropped(t *testing.T) {
	l := NewRollingLog(t.TempDir())
	if _, err := l.Write([]byte("one\n")); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Write([]byte("two\n")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(l.Path())
	if strings.Contains(string(b), "two") {
		t.Fatal("a line was written after Close")
	}
}

// The log is not a diary. Every call to the standard logger in this
// package and in the desktop shell is read here, and none of the VALUES it
// interpolates may be a passphrase, a key, a token, a payload or a message
// body. The rolling file makes these lines durable and owner-readable; the
// test makes sure durable never becomes sensitive. Prose (the format
// literal) is free to say "cap" or "token" — it is the argument that would
// carry one.
func TestLogLinesCarryNoSecrets(t *testing.T) {
	dirs := []string{".", filepath.Join("..", "cmd", "desktop"), filepath.Join("..", "cmd", "terminal")}
	seen := 0
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			calls, findings := scanLogCalls(t, path, src)
			seen += calls
			for _, f := range findings {
				t.Errorf("%s — the rolling file keeps this line", f)
			}
		}
	}
	if seen == 0 {
		t.Fatal("found no log calls at all — the scan is looking in the wrong place")
	}
}

// A check that cannot fail is not a check.
func TestTheSecretsScanCanActuallyFail(t *testing.T) {
	src := []byte(`package x
import "log"
func f(passphrase string, hint []byte, n int) {
	log.Printf("the relay item cap is %d and the token word is prose", n) // fine
	log.Printf("opened with %s", passphrase)                                // not fine
	log.Println("mailbox " + string(hint))                                  // not fine
}
`)
	calls, findings := scanLogCalls(t, "synthetic.go", src)
	if calls != 3 {
		t.Fatalf("saw %d log calls in the synthetic file, want 3", calls)
	}
	if len(findings) != 2 {
		t.Fatalf("the scan found %d problems, want 2 (the passphrase and the hint):\n%s",
			len(findings), strings.Join(findings, "\n"))
	}
}

// sensitive is what a log argument must not be named after.
var sensitive = regexp.MustCompile(`(?i)passphrase|password|secret|priv(ate)?key|\.priv\b|token|plaintext|payload|\bbody\b|\.text\b|\bcap\b|hint\b|mailbox`)

// scanLogCalls parses one file and reports how many standard-logger calls
// it holds and which of their non-prose arguments name something sensitive.
func scanLogCalls(t *testing.T, path string, src []byte) (calls int, findings []string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "log" || !strings.HasPrefix(sel.Sel.Name, "Print") && !strings.HasPrefix(sel.Sel.Name, "Fatal") {
			return true
		}
		calls++
		for _, arg := range call.Args {
			if isProse(arg) {
				continue
			}
			text := string(src[fset.Position(arg.Pos()).Offset:fset.Position(arg.End()).Offset])
			if m := sensitive.FindString(text); m != "" {
				findings = append(findings, fmt.Sprintf("%s: a log argument names %q: %s",
					fset.Position(arg.Pos()), m, text))
			}
		}
		return true
	})
	return calls, findings
}

// isProse reports whether an expression is made only of string literals —
// a format string, possibly concatenated across lines. Prose is free to
// name what it must not carry.
func isProse(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.BasicLit:
		return x.Kind == token.STRING
	case *ast.BinaryExpr:
		return x.Op == token.ADD && isProse(x.X) && isProse(x.Y)
	case *ast.ParenExpr:
		return isProse(x.X)
	}
	return false
}

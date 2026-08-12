package main

// The boundary, checked by the parser rather than by intention.
//
// ADR-011 puts the UI boundary at the local HTTP API: a native shell is
// allowed because it HOSTS that API and the interface, not because it
// replaces them with Go bindings. The moment this package can reach a
// terminal, a protocol type or the kernel, somebody will — reasonably, to
// fix something small — and the shell will have quietly become a second,
// worse copy of the node's API with none of its tests.
//
// The second rule is the one that decides what a Wails upgrade costs.
// internal/wailsx exists so the framework is named in exactly one file; a
// direct import here would make that claim false while everything still
// compiled.
//
// Checked across EVERY file in the package, not just main.go — a rule
// enforced in one file is a rule that moves to the next one.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestTheShellReachesNoFurtherThanTheAPI(t *testing.T) {
	// Each entry is a path fragment and the reason it is out of bounds, so a
	// failure explains itself instead of pointing at this list.
	forbidden := map[string]string{
		"quiet_places/terminals":  "the shell must speak to the node through its API, not to terminals",
		"quiet_places/protocol":   "protocol types belong behind the API; a shell that decodes them is a second client",
		"quiet_places/kernel":     "the kernel is the node's business — reaching it bypasses every check the API makes",
		"quiet_places/transports": "a transport in the shell is a second delivery path with no ledger behind it",
		"wailsapp/wails":          "Wails is named in internal/wailsx and nowhere else — that is what an upgrade costs",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	seen := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if f.Name.Name != "main" {
			continue
		}
		seen++
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for frag, why := range forbidden {
				if strings.Contains(path, frag) {
					t.Errorf("%s imports %s — %s", name, path, why)
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("parsed no files: the check would pass on an empty package")
	}
}

// TestTheBoundaryCheckCanActuallyFail. A guard that cannot fail is a comment,
// and this one is easy to write in a way that silently matches nothing.
func TestTheBoundaryCheckCanActuallyFail(t *testing.T) {
	src := `package main
import _ "github.com/drrainlab/quiet_places/kernel/storage"
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "hypothetical.go", src, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	var hit bool
	ast.Inspect(f, func(n ast.Node) bool {
		imp, ok := n.(*ast.ImportSpec)
		if !ok {
			return true
		}
		p, _ := strconv.Unquote(imp.Path.Value)
		if strings.Contains(p, "quiet_places/kernel") {
			hit = true
		}
		return true
	})
	if !hit {
		t.Fatal("the same matching the real check uses missed an obvious violation")
	}
}

package webui

import (
	"path/filepath"
	"testing"
)

// TestNoTwoScriptsDeclareTheSameGlobal runs scripts/webui/globals.cjs.
// Classic scripts share one global scope: a second `const SKY` is a parse
// error that silently disables the whole file that carries it. 1.0.2
// shipped that — the starfield vanished and Settings broke everywhere.
func TestNoTwoScriptsDeclareTheSameGlobal(t *testing.T) {
	runHarness(t, filepath.Join("webui", "globals.cjs"),
		"two asset scripts declare the same top-level name")
}

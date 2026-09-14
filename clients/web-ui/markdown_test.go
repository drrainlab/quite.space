package webui

import (
	"path/filepath"
	"testing"
)

// TestMarkdownTablesAndTheReaderThreshold runs scripts/webui/markdown.cjs:
// a GFM table renders as a table, a lone pipe line stays prose, and the
// long-message threshold is where reader.js says it is.
func TestMarkdownTablesAndTheReaderThreshold(t *testing.T) {
	runHarness(t, filepath.Join("webui", "markdown.cjs"),
		"the markdown renderer or the reader threshold changed")
}

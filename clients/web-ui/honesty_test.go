package webui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// runHarness executes one of the scripts/webui or scripts/i18n checks under
// node, the way the other stubs are run. Skipped, not failed, where node is
// absent: the Go side cannot evaluate the app, and a red build on a machine
// without node would teach people to ignore red.
func runHarness(t *testing.T, rel, complaint string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not available")
	}
	script := filepath.Join("..", "..", "scripts", rel)
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("the harness is missing: %v", err)
	}
	out, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", complaint, err, out)
	}
	t.Logf("%s", out)
}

// TestTheHonestySurfacesSayWhatIsHeld runs scripts/webui/honesty.cjs: the
// line under the feed counts frames parked behind a hole in a chain, and
// the relay diagnostics panel shows members without a route and frames no
// relay item can carry. Each number already existed in the node's API and
// reached no screen — which is how a newcomer's stuck room looked empty.
func TestTheHonestySurfacesSayWhatIsHeld(t *testing.T) {
	runHarness(t, filepath.Join("webui", "honesty.cjs"),
		"a number the node reports does not reach the screen")
}

// TestTheCatalogsDoNotDrift runs scripts/i18n/catalogs.cjs, which existed
// and was run by hand. A key added to one locale and not the other falls
// back to English mid-paragraph and nobody notices; now the build does.
func TestTheCatalogsDoNotDrift(t *testing.T) {
	runHarness(t, filepath.Join("i18n", "catalogs.cjs"),
		"the EN and RU catalogs disagree")
}

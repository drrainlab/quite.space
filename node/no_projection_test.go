// "The publisher is not here right now" is not a broken relay.
//
// This was found in use: a node contributing to a public space whose owner
// was offline showed "relay · issue" for hours while every message it sent
// and received arrived perfectly. The condition was already understood to
// be routine — the fetch path filtered it — but it was recognised by
// COMPARING err.Error() to a literal, and the uplink path wraps the same
// error with %w. One path stayed quiet, the other painted the light red.
package node

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// readSource reads a file of this package, so a rule about HOW the code
// judges an error can be pinned as well as the judgement itself.
func readSource(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}

// The sentinel must survive wrapping, because the only reason this bug
// existed is that a string does not.
func TestNoProjectionSurvivesWrapping(t *testing.T) {
	wrapped := fmt.Errorf("node: no ingress address and none could be fetched: %w",
		ErrNoProjection)
	if !errors.Is(wrapped, ErrNoProjection) {
		t.Fatal("the wrapped routine condition is no longer recognisable")
	}
	// And it must not swallow a genuine transport failure that merely
	// mentions the relay.
	other := fmt.Errorf("node: relay refused the write: %w", errors.New("rate limited"))
	if errors.Is(other, ErrNoProjection) {
		t.Fatal("a real failure was classified as routine")
	}
}

// The guard against the fix regressing into a string comparison: if the
// text is the only thing holding this together, the next wrapper breaks it
// again exactly as before.
func TestRoutineConditionIsNotMatchedByText(t *testing.T) {
	src, err := readSource("relaysync.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(src, `"node: no projection available at the relay"`) {
		t.Fatal("relaysync.go compares the routine condition by spelling again")
	}
	if !strings.Contains(src, "errors.Is(err, ErrNoProjection)") {
		t.Fatal("the routine condition is no longer recognised at all")
	}
	// Both consumers, not one: the whole defect was that a second path
	// existed with no filter on it.
	if n := strings.Count(src, "errors.Is(err, ErrNoProjection)"); n < 2 {
		t.Fatalf("only %d of the two paths judges the condition", n)
	}
}

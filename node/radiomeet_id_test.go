// The id a screen is handed must be an id a screen can hand back.
package node

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// "Start a line" answered with the DISPLAY form of a terminal id — "terminal:"
// plus a truncated hex. The client stores that as the current space and
// immediately asks for its entries, palette and appearance, so all three came
// back 400 and the console filled with "encoding/hex: invalid byte: U+0074
// 't'". That 't' is the first letter of "terminal:". One wrong method call,
// and pressing a button looked like the whole screen breaking.
func TestTheMeetAnswerCarriesAnIdThatParsesBack(t *testing.T) {
	var tid id.TerminalID
	for i := range tid {
		tid[i] = byte(i * 7)
	}
	got := meetResponse(tid)["space"]
	back, err := id.ParseTerminalID(got)
	if err != nil {
		t.Fatalf("the client cannot use the id it was given (%q): %v", got, err)
	}
	if back != tid {
		t.Fatal("the id survived parsing but is not the same terminal")
	}
	// And the display form must genuinely be unusable here, or this test is
	// only asserting a coincidence.
	if _, err := id.ParseTerminalID(tid.String()); err == nil {
		t.Fatal("the display form parses too, so this test proves nothing — " +
			"if that changed deliberately, delete this check and say why")
	}
}

// The status a client reads must answer for EVERY carrier.
//
// RadioStatus was written as the carrier-neutral face and then had no caller
// at all: handleStatus and handleGateway both read Mesh(), which returns an
// empty struct whenever the Meshtastic driver is not the one attached. So an
// RNode could be connected, transmitting, and carrying a whole conversation
// while the chip was blank, the radio-meet screen said "no radio is
// connected", and the gateway screen showed a node number and a channel of
// zero in positions that read as measurements.
//
// It survived the live product gate because the gate drove the API directly
// and never looked at a screen. That is the gap these tests close.
package node

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// callsInFunc reports whether fn's body contains a call to a method of the
// given name, at any depth.
func callsInFunc(fn *ast.FuncDecl, method string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == method {
			found = true
		}
		return true
	})
	return found
}

// Every handler that answers "is a radio there" reads the neutral face.
//
// This is a structural rule rather than a behavioural one because the honest
// alternative needs a serial port: the carrier branch in RadioState is chosen
// by a live *rnode.Radio, and a fake would prove only that the fake works.
// What CAN be pinned exactly is the rule that was broken — a status surface
// must not take its answer from one carrier's diagnostic.
func TestTheStatusSurfacesReadTheCarrierNeutralFace(t *testing.T) {
	fset := token.NewFileSet()
	for file, handlers := range map[string][]string{
		"api.go":         {"handleStatus"},
		"api_gateway.go": {"handleGateway"},
	} {
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, want := range handlers {
			var fn *ast.FuncDecl
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == want {
					fn = fd
				}
			}
			if fn == nil {
				t.Fatalf("%s: %s is gone — if it was renamed, this rule has "+
					"to move with it rather than quietly stop applying", file, want)
			}
			if !callsInFunc(fn, "RadioState") {
				t.Errorf("%s: %s does not call RadioState().\n"+
					"A radio's PRESENCE must come from the carrier-neutral face. "+
					"Mesh() is a Meshtastic diagnostic and is an empty struct for "+
					"every other driver, so reading it here reports a working "+
					"radio as no radio at all — which is exactly what shipped.",
					file, want)
			}
		}
	}
}

// And the carrier that ships today did not change meaning underneath.
//
// The neutral face is derived from Mesh() on the Meshtastic path, so the two
// must agree about presence — a refactor that made the new answer right for
// RNode and wrong for Meshtastic would be no better than what it replaced.
func TestTheNeutralFaceAgreesWithMeshtasticAboutPresence(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	if got := rt.RadioState(); got.Connected != rt.Mesh().Connected {
		t.Fatalf("no radio attached, yet the two faces disagree: "+
			"neutral %v, meshtastic %v", got.Connected, rt.Mesh().Connected)
	}
	// This test originally also asserted that the neutral face NAMES a carrier
	// even with nothing attached, "so a client never has to guess". That was
	// wrong, and the claim is now inverted in TestNoRadioNamesNoCarrier:
	// answering "meshtastic" on a node with no radio is a driver that is not
	// present, sitting in a field that reads as a fact. Not guessing is worth
	// less than not asserting.
}

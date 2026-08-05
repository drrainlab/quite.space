// "Has this person chosen a name" and "is there nothing here yet" are two
// different questions, and the interface may only take the screen for the
// second one.
//
// FOUND ON A PHONE (AR-0d). The client opened the modal first-run wall on
// `needs_name`, which is only ever "no display name has been chosen". But a
// display name is chosen in the UI and nowhere else, so a node opened by a
// shell or an embedding host has none — even holding an identity, eight spaces
// and an open conversation. Those people got the whole welcome flow dropped
// over a working interface, and because a <dialog> opened with showModal()
// closes on Esc and a phone has no Esc, and because that dialog had no dismiss
// control of its own, they were HELD there. The only exit anybody found was to
// open a different dialog and cancel that one.
//
// This test is what keeps the two questions apart.
package node

import "testing"

func TestAFirstRunIsAnEmptyNodeNotMerelyAnUnnamedOne(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "") // no launch name, nothing created yet
	defer rt.Close()

	if !rt.NeedsOnboarding() {
		t.Fatal("a node with no chosen name should report needs_name")
	}
	if !rt.IsFirstRun() {
		t.Fatal("a node with no name and no spaces IS a first run")
	}

	// The moment anything exists here, this is no longer a first run — and the
	// welcome flow must stop being entitled to the screen, whatever the name
	// situation is.
	if _, err := rt.CreateSpace("Room"); err != nil {
		t.Fatal(err)
	}
	if rt.IsFirstRun() {
		t.Error("a node holding a space is NOT a first run — this is the state " +
			"that trapped a person on a phone: the welcome wall over a working UI")
	}
	if !rt.NeedsOnboarding() {
		t.Error("needs_name must still be true — the nudge is still owed, " +
			"it just may not take the screen")
	}

	// And naming yourself does not resurrect it.
	if err := rt.SetName("Robert"); err != nil {
		t.Fatal(err)
	}
	if rt.IsFirstRun() || rt.NeedsOnboarding() {
		t.Errorf("named node with a space: first_run=%v needs_name=%v, want both false",
			rt.IsFirstRun(), rt.NeedsOnboarding())
	}
}

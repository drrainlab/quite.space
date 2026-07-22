package node

import (
	"testing"
	"time"
)

// UI-1: onboarding + rename. A fresh node needs a name; setting one persists
// across restart; renaming bumps the self manifest and members see it.
func TestOnboardingAndRename(t *testing.T) {
	dir := t.TempDir()

	rt := openRuntime(t, dir, "") // no launch name
	if !rt.NeedsOnboarding() {
		t.Fatal("fresh node should need onboarding")
	}
	if err := rt.SetName("Robert"); err != nil {
		t.Fatal(err)
	}
	if rt.NeedsOnboarding() || rt.DisplayName() != "Robert" {
		t.Fatalf("name not set: %q needs=%v", rt.DisplayName(), rt.NeedsOnboarding())
	}
	tid, err := rt.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Say(tid, "hi"); err != nil {
		t.Fatal(err)
	}
	rev1 := rt.Self.Manifest.Revision
	rt.Close()

	// Restart: name is remembered without a launch flag.
	rt2 := openRuntime(t, dir, "")
	defer rt2.Close()
	if rt2.NeedsOnboarding() || rt2.DisplayName() != "Robert" {
		t.Fatalf("name lost across restart: %q", rt2.DisplayName())
	}
	if rt2.Self.Manifest.Revision != rev1 {
		t.Fatalf("self revision changed on restart: %d -> %d", rev1, rt2.Self.Manifest.Revision)
	}

	// Rename bumps the revision and republishes; the member card reflects it.
	if err := rt2.SetName("Bobby"); err != nil {
		t.Fatal(err)
	}
	if rt2.Self.Manifest.Revision != rev1+1 {
		t.Fatalf("rename did not bump revision: %d", rt2.Self.Manifest.Revision)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		cards, _ := rt2.Members(tid)
		named := ""
		for _, c := range cards {
			if c.Principal == rt2.Principal.ID {
				named = c.Name
			}
		}
		if named == "Bobby" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("member card still shows old name: %q", named)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

package contract

import (
	"errors"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// fakeContract is a minimal test contract.
type fakeContract struct {
	id       string
	validErr error
	fallback string
	refs     []schemas.AssetRef
}

func (f fakeContract) SchemaID() string        { return f.id }
func (f fakeContract) Validate(p []byte) error { return f.validErr }
func (f fakeContract) Fallback(p []byte) (string, error) {
	return f.fallback, f.validErr
}
func (f fakeContract) AssetRefs(p []byte) ([]schemas.AssetRef, error) {
	return f.refs, f.validErr
}

func mustPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("%s: expected panic", name)
		}
	}()
	fn()
}

func TestRegisterDuplicatePanics(t *testing.T) {
	r := New(nil)
	c := fakeContract{id: "test.alpha.v1"}
	r.Register(c, Descriptor{SchemaID: "test.alpha.v1"})
	mustPanic(t, "duplicate", func() {
		r.Register(c, Descriptor{SchemaID: "test.alpha.v1"})
	})
}

func TestRegisterInvalidIDPanics(t *testing.T) {
	r := New(nil)
	mustPanic(t, "three segments", func() {
		r.Register(fakeContract{id: "a.b.c.v1"}, Descriptor{SchemaID: "a.b.c.v1"})
	})
	mustPanic(t, "descriptor mismatch", func() {
		r.Register(fakeContract{id: "test.beta.v1"}, Descriptor{SchemaID: "test.other.v1"})
	})
}

func TestFreezeIsReadOnlyAndIdempotent(t *testing.T) {
	r := New(nil)
	r.Register(fakeContract{id: "test.alpha.v1"}, Descriptor{SchemaID: "test.alpha.v1"})
	if r.Frozen() {
		t.Fatal("frozen before Freeze")
	}
	r.Freeze()
	r.Freeze() // idempotent
	if !r.Frozen() {
		t.Fatal("not frozen after Freeze")
	}
	mustPanic(t, "register after freeze", func() {
		r.Register(fakeContract{id: "test.beta.v1"}, Descriptor{SchemaID: "test.beta.v1"})
	})
	// Reads still work.
	if _, ok := r.Lookup("test.alpha.v1"); !ok {
		t.Fatal("lookup lost after freeze")
	}
}

func TestDescriptorsStableOrder(t *testing.T) {
	r := New(nil)
	// Register out of order; enumeration must sort by schema id.
	for _, id := range []string{"test.zeta.v1", "test.alpha.v1", "test.mid.v1"} {
		r.Register(fakeContract{id: id}, Descriptor{SchemaID: id, Keepable: id == "test.mid.v1"})
	}
	ds := r.Descriptors()
	want := []string{"test.alpha.v1", "test.mid.v1", "test.zeta.v1"}
	if len(ds) != len(want) {
		t.Fatalf("got %d descriptors, want %d", len(ds), len(want))
	}
	for i, w := range want {
		if ds[i].SchemaID != w {
			t.Fatalf("descriptor[%d] = %q, want %q", i, ds[i].SchemaID, w)
		}
	}
	if !ds[1].Keepable || ds[0].Keepable {
		t.Fatal("Keepable flag not carried through")
	}
}

func TestHookMirrorsRegistration(t *testing.T) {
	var mirrored []string
	r := New(func(id string, v schemas.Validator) {
		mirrored = append(mirrored, id)
		if v == nil {
			t.Fatal("hook got nil validator")
		}
	})
	r.Register(fakeContract{id: "test.alpha.v1"}, Descriptor{SchemaID: "test.alpha.v1"})
	if len(mirrored) != 1 || mirrored[0] != "test.alpha.v1" {
		t.Fatalf("hook mirrored %v", mirrored)
	}
}

func TestLookupFallbackAssets(t *testing.T) {
	r := New(nil)
	ref := schemas.AssetRef{}
	r.Register(
		fakeContract{id: "test.alpha.v1", fallback: "hello", refs: []schemas.AssetRef{ref}},
		Descriptor{SchemaID: "test.alpha.v1"},
	)
	c, ok := r.Lookup("test.alpha.v1")
	if !ok {
		t.Fatal("lookup failed")
	}
	fb, err := c.Fallback(nil)
	if err != nil || fb != "hello" {
		t.Fatalf("fallback = %q, %v", fb, err)
	}
	refs, err := c.AssetRefs(nil)
	if err != nil || len(refs) != 1 {
		t.Fatalf("assets = %v, %v", refs, err)
	}
	if _, ok := r.Lookup("test.unknown.v1"); ok {
		t.Fatal("unknown schema resolved")
	}
}

func TestValidateFlowsThroughContract(t *testing.T) {
	r := New(nil)
	sentinel := errors.New("bad payload")
	r.Register(fakeContract{id: "test.alpha.v1", validErr: sentinel},
		Descriptor{SchemaID: "test.alpha.v1"})
	c, _ := r.Lookup("test.alpha.v1")
	if err := c.Validate([]byte{0x01}); !errors.Is(err, sentinel) {
		t.Fatalf("validate error = %v, want sentinel", err)
	}
}

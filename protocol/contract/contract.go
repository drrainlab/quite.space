// Package contract is the runtime seed of the Space Runtime contract layer
// (LR-0a): a strict, freezable registry of payload contracts. A Contract
// bundles everything the runtime may ask about a schema — validation, the
// mandatory text fallback, and the asset references a payload carries —
// behind one interface, so renderers and reducers stop growing per-schema
// switches.
//
// LR-0a registers ONLY the new schemas of the Listening Room wave. Existing
// block.* / publication.* / appdef registrations migrate here in LR-0b
// (Registry Consolidation), bit-for-bit: golden vectors must not change.
package contract

import (
	"fmt"
	"sort"
	"sync"

	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// Contract is one schema's full runtime surface. Implementations are
// stateless values; every method must treat the payload as untrusted input.
type Contract interface {
	// SchemaID returns the ADR-009 schema id (<domain>.<type>.vN).
	SchemaID() string
	// Validate structurally checks a payload (authoring/ingest gate).
	Validate(payload []byte) error
	// Fallback returns the mandatory plain-text rendering of a payload.
	// Every contract MUST produce a fallback for every valid payload.
	Fallback(payload []byte) (string, error)
	// AssetRefs returns the asset references a payload carries, by value.
	// Payloads without assets return a nil slice and nil error.
	AssetRefs(payload []byte) ([]schemas.AssetRef, error)
}

// Descriptor is the stable, enumerable description of a registered contract.
// Keepable is reserved growth (formalized in LR-0b); until then the Keep
// feature enforces its own explicit allowlist (LR-1) and this field is
// informational only.
type Descriptor struct {
	SchemaID string
	Keepable bool
}

// Registry is a strict schema-contract registry: duplicate registration is a
// programming error (panic), and after Freeze the registry is read-only.
type Registry struct {
	mu        sync.RWMutex
	frozen    bool
	contracts map[string]Contract
	descs     map[string]Descriptor
	// hook mirrors each registration into an outer registry (the ADR-009
	// schemas registry in production; nil in unit tests).
	hook func(schemaID string, v schemas.Validator)
}

// New returns an empty registry. hook, when non-nil, is invoked once per
// successful Register with the schema id and the contract's validator.
func New(hook func(schemaID string, v schemas.Validator)) *Registry {
	return &Registry{
		contracts: map[string]Contract{},
		descs:     map[string]Descriptor{},
		hook:      hook,
	}
}

// Register adds a contract. It panics on: frozen registry, invalid schema id,
// duplicate schema id, or a descriptor whose SchemaID disagrees with the
// contract. Panicking is deliberate — registration happens at bootstrap and a
// conflict is a build-time bug, never a runtime condition.
func (r *Registry) Register(c Contract, d Descriptor) {
	id := c.SchemaID()
	if !schemas.ValidID(id) {
		panic(fmt.Sprintf("contract: invalid schema id %q", id))
	}
	if d.SchemaID != id {
		panic(fmt.Sprintf("contract: descriptor id %q != contract id %q", d.SchemaID, id))
	}
	r.mu.Lock()
	if r.frozen {
		r.mu.Unlock()
		panic(fmt.Sprintf("contract: register %q after Freeze", id))
	}
	if _, dup := r.contracts[id]; dup {
		r.mu.Unlock()
		panic(fmt.Sprintf("contract: duplicate registration %q", id))
	}
	r.contracts[id] = c
	r.descs[id] = d
	hook := r.hook
	r.mu.Unlock()
	if hook != nil {
		hook(id, c.Validate)
	}
}

// Freeze makes the registry read-only. Idempotent: freezing twice is a no-op,
// so every bootstrap path may call it defensively.
func (r *Registry) Freeze() {
	r.mu.Lock()
	r.frozen = true
	r.mu.Unlock()
}

// Frozen reports whether Freeze has been called.
func (r *Registry) Frozen() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.frozen
}

// Lookup returns the contract for a schema id.
func (r *Registry) Lookup(schemaID string) (Contract, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.contracts[schemaID]
	return c, ok
}

// Descriptor returns the descriptor for a schema id.
func (r *Registry) Descriptor(schemaID string) (Descriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.descs[schemaID]
	return d, ok
}

// Descriptors returns all descriptors sorted by schema id — the stable
// enumeration order regardless of registration order.
func (r *Registry) Descriptors() []Descriptor {
	r.mu.RLock()
	out := make([]Descriptor, 0, len(r.descs))
	for _, d := range r.descs {
		out = append(out, d)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].SchemaID < out[j].SchemaID })
	return out
}

// std is the production registry: registrations mirror into the ADR-009
// schemas registry so envelope validation keeps working unchanged.
var std = New(schemas.Register)

// Register adds a contract to the production registry.
func Register(c Contract, d Descriptor) { std.Register(c, d) }

// Freeze freezes the production registry; call after bootstrap. Idempotent.
func Freeze() { std.Freeze() }

// Frozen reports whether the production registry is frozen.
func Frozen() bool { return std.Frozen() }

// Lookup finds a contract in the production registry.
func Lookup(schemaID string) (Contract, bool) { return std.Lookup(schemaID) }

// GetDescriptor finds a descriptor in the production registry.
func GetDescriptor(schemaID string) (Descriptor, bool) { return std.Descriptor(schemaID) }

// Descriptors enumerates the production registry, sorted by schema id.
func Descriptors() []Descriptor { return std.Descriptors() }

// Fallback renders the mandatory text fallback via the production registry.
// Unknown schemas return schemas.ErrUnknownSchema — the caller falls back to
// its own opaque rendering, it never treats unknown as invalid.
func Fallback(schemaID string, payload []byte) (string, error) {
	c, ok := std.Lookup(schemaID)
	if !ok {
		return "", schemas.ErrUnknownSchema
	}
	return c.Fallback(payload)
}

// AssetRefs extracts asset references via the production registry. Unknown
// schemas return (nil, schemas.ErrUnknownSchema).
func AssetRefs(schemaID string, payload []byte) ([]schemas.AssetRef, error) {
	c, ok := std.Lookup(schemaID)
	if !ok {
		return nil, schemas.ErrUnknownSchema
	}
	return c.AssetRefs(payload)
}

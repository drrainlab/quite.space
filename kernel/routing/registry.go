// Adapter registry (TN-1): transports register endpoint factories by
// scheme; a bridge or node resolves "mesh:serial:/dev/ttyUSB0" without
// linking every transport into every binary path.
package routing

import (
	"fmt"
	"sync"

	"github.com/drrainlab/quiet_places/transports"
)

// Well-known schemes. SchemeReticulum is RESERVED for the next wave — the
// whole seam the Reticulum adapter needs (transports/reticulum registers
// under this name when it lands).
const (
	SchemeLAN       = "lan"
	SchemeMesh      = "mesh"
	SchemeRelay     = "relay"
	SchemeBundle    = "bundle"
	SchemeSim       = "sim"
	SchemeReticulum = "reticulum" // reserved, no adapter this wave
)

// EndpointFactory builds a live endpoint from a scheme-specific target
// (e.g. "serial:/dev/ttyUSB0", "relay.example:7411").
type EndpointFactory func(target string) (transports.Endpoint, error)

var (
	regMu     sync.Mutex
	factories = map[string]EndpointFactory{}
)

// RegisterScheme adds a factory; duplicates are a programming error.
func RegisterScheme(scheme string, f EndpointFactory) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := factories[scheme]; dup {
		panic(fmt.Sprintf("routing: duplicate scheme %q", scheme))
	}
	factories[scheme] = f
}

// OpenEndpoint resolves scheme + target through the registry.
func OpenEndpoint(scheme, target string) (transports.Endpoint, error) {
	regMu.Lock()
	f, ok := factories[scheme]
	regMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("routing: unknown transport scheme %q", scheme)
	}
	return f(target)
}

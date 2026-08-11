// The route book (RT-0): two DIRECTED tables, and the direction is the whole
// design.
//
//	SelfIngress          where THIS device is obliged to listen for its mail
//	PeerRoutes[device]   where this device may DELIVER to that one
//
// They are never derived from each other. A frame that arrived through
// endpoint E proves that WE were reachable at E — it says nothing about
// where its author listens — so local ingress subscriptions must never be
// built from another device's reachability, and a peer route must never be
// invented from an arrival. That inversion is how a client starts treating
// its own mailboxes as other people's addresses, and it reads plausible
// right up until two participants pick different relays and every message
// silently misses.
//
// Kept in the ENCRYPTED keystore rather than relays.json: a map of who is
// reachable where is a picture of the social graph, and relays.json is
// plaintext and disposable by contract.
package storage

import (
	"errors"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

var errMalformedRouteEntry = errors.New("storage: malformed route entry")

// Route provenance — how a route came to be believed. Order is strength:
// a route learned from the sealed invitation exchange outranks one merely
// carried over from the single-relay era. Serialized; values must not move.
const (
	RouteManual      uint8 = 1 // this person configured it for themselves
	RouteInvitation  uint8 = 2 // the sealed join exchange carried it
	RouteAdvertised  uint8 = 3 // a signed reachability advertisement (T3)
	RouteObserved    uint8 = 4 // met on a live link (LAN/radio)
	RouteLegacy      uint8 = 5 // the single-relay era's one recorded address
)

// Route is one way to reach an endpoint, with the story of how we know.
type Route struct {
	Endpoint   string // dialable address (host:port for relays)
	Transport  string // "relay" today; "lan"/"radio" become valid in T6
	Provenance uint8
	LearnedAt  int64 // unix seconds, when the route entered the book
	LastSeen   int64 // unix seconds, last time it demonstrably worked
}

// routeFields is the record arity, NAMED — see SpaceMeta's three unnamed
// 15s for what a bare literal costs. Bump it when appending a field, and
// only ever append.
const routeFields = 5

func appendRoute(buf []byte, r Route) []byte {
	buf = codec.AppendArray(buf, routeFields)
	buf = codec.AppendText(buf, r.Endpoint)
	buf = codec.AppendText(buf, r.Transport)
	buf = codec.AppendUint(buf, uint64(r.Provenance))
	buf = codec.AppendUint(buf, uint64(r.LearnedAt))
	buf = codec.AppendUint(buf, uint64(r.LastSeen))
	return buf
}

func readRoute(d *codec.Decoder) (Route, error) {
	var r Route
	acount, err := d.ReadArray()
	if err != nil {
		return r, err
	}
	if acount >= 1 {
		if r.Endpoint, err = d.ReadText(); err != nil {
			return r, err
		}
	}
	if acount >= 2 {
		if r.Transport, err = d.ReadText(); err != nil {
			return r, err
		}
	}
	if acount >= 3 {
		v, err := d.ReadUint()
		if err != nil {
			return r, err
		}
		r.Provenance = uint8(v)
	}
	if acount >= 4 {
		v, err := d.ReadUint()
		if err != nil {
			return r, err
		}
		r.LearnedAt = int64(v)
	}
	if acount >= 5 {
		v, err := d.ReadUint()
		if err != nil {
			return r, err
		}
		r.LastSeen = int64(v)
	}
	// A NEWER build appended something: skip it rather than dying
	// mid-record — the lesson SpaceMeta's decoder already carries.
	for i := routeFields; i < acount; i++ {
		if err := d.SkipItem(); err != nil {
			return r, err
		}
	}
	return r, nil
}

// appendRouteBook writes SelfIngress and PeerRoutes as one top-level value:
// a 2-array of [selfRoutes, peerEntries], each entry [deviceID, routes].
// Deterministic order, like every other keystore section.
func appendRouteBook(buf []byte, self []Route, peers map[id.DeviceID][]Route) []byte {
	buf = codec.AppendArray(buf, 2)
	buf = codec.AppendArray(buf, len(self))
	for _, r := range self {
		buf = appendRoute(buf, r)
	}
	buf = codec.AppendArray(buf, len(peers))
	for _, pe := range sortedPeerRoutes(peers) {
		buf = codec.AppendArray(buf, 2)
		buf = codec.AppendBytes(buf, pe.dev[:])
		buf = codec.AppendArray(buf, len(pe.routes))
		for _, r := range pe.routes {
			buf = appendRoute(buf, r)
		}
	}
	return buf
}

type peerRoutesEntry struct {
	dev    id.DeviceID
	routes []Route
}

func sortedPeerRoutes(m map[id.DeviceID][]Route) []peerRoutesEntry {
	out := make([]peerRoutesEntry, 0, len(m))
	for dev, rs := range m {
		out = append(out, peerRoutesEntry{dev, rs})
	}
	sortByID(out, func(e peerRoutesEntry) id.TerminalID { return id.TerminalID(e.dev) })
	return out
}

func readRouteBook(d *codec.Decoder) (self []Route, peers map[id.DeviceID][]Route, err error) {
	peers = map[id.DeviceID][]Route{}
	outer, err := d.ReadArray()
	if err != nil {
		return nil, nil, err
	}
	if outer >= 1 {
		n, er := d.ReadArray()
		if er != nil {
			return nil, nil, er
		}
		for i := 0; i < n; i++ {
			r, er := readRoute(d)
			if er != nil {
				return nil, nil, er
			}
			self = append(self, r)
		}
	}
	if outer >= 2 {
		n, er := d.ReadArray()
		if er != nil {
			return nil, nil, er
		}
		for i := 0; i < n; i++ {
			pair, er := d.ReadArray()
			if er != nil || pair < 2 {
				if er == nil {
					er = errMalformedRouteEntry
				}
				return nil, nil, er
			}
			raw, er := d.ReadBytes()
			if er != nil || len(raw) != len(id.DeviceID{}) {
				if er == nil {
					er = errMalformedRouteEntry
				}
				return nil, nil, er
			}
			var dev id.DeviceID
			copy(dev[:], raw)
			cnt, er := d.ReadArray()
			if er != nil {
				return nil, nil, er
			}
			var rs []Route
			for j := 0; j < cnt; j++ {
				r, er := readRoute(d)
				if er != nil {
					return nil, nil, er
				}
				rs = append(rs, r)
			}
			peers[dev] = rs
			for k := 2; k < pair; k++ {
				if er := d.SkipItem(); er != nil {
					return nil, nil, er
				}
			}
		}
	}
	for i := 2; i < outer; i++ {
		if er := d.SkipItem(); er != nil {
			return nil, nil, er
		}
	}
	return self, peers, nil
}

// Package routing is the Terminal Network layer above a single link
// (ADR-015): loop prevention, durable custody queues, subscriptions,
// delivery-class policy and route projections. The first full consumer is
// the blind quiet-bridge; the personal node keeps its attach-all pump and
// adopts pieces incrementally.
package routing

import (
	"encoding/binary"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// LinkID names one live link instance (e.g. "mesh:serial:/dev/ttyUSB0",
// "relay:relay.example:7411", "lan:192.168.1.7:53412").
type LinkID string

// LoopDomainID names the loop domain a link belongs to — the shared medium
// where a frame sent back would echo (e.g. "meshtastic-quiet@device-1",
// "relay:relay.example"). Split-horizon suppresses same-link and
// same-domain forwarding; same CLASS is explicitly allowed
// (radio-A → bridge → radio-B is a legal topology, ADR-015 §4).
type LoopDomainID string

// SuppressForward is the split-horizon rule.
func SuppressForward(ingressLink, egressLink LinkID, ingressDomain, egressDomain LoopDomainID) bool {
	if ingressLink != "" && ingressLink == egressLink {
		return true
	}
	if ingressDomain != "" && ingressDomain == egressDomain {
		return true
	}
	return false
}

// SeenKey is the bounded dedup key: a 16-byte prefix of the content-derived
// EventID. Collisions only cost a duplicate suppression — the full-hash
// dedup at ingest is the final authority.
type SeenKey [16]byte

// KeyOf derives the seen-cache key from a full frame.
func KeyOf(frame []byte) SeenKey {
	var k SeenKey
	eid := id.EventIDOf(frame)
	copy(k[:], eid[:16])
	return k
}

// SeenCache is a bounded TTL'd set of recently forwarded frames. Losing it
// (crash between snapshots) costs airtime, never correctness.
type SeenCache struct {
	mu      sync.Mutex
	entries map[SeenKey]int64 // key → unix seconds last seen
	cap     int
	ttl     time.Duration
}

// DefaultSeenCap holds ~7 days of busy traffic in a few MB (Pi-friendly).
const DefaultSeenCap = 262144

// NewSeenCache creates a cache; cap<=0 uses DefaultSeenCap, ttl<=0 means
// entries never expire by age (cap eviction only).
func NewSeenCache(capacity int, ttl time.Duration) *SeenCache {
	if capacity <= 0 {
		capacity = DefaultSeenCap
	}
	return &SeenCache{entries: map[SeenKey]int64{}, cap: capacity, ttl: ttl}
}

// Seen records the key and reports whether it was already present (and not
// expired). One call does both — the forwarding loop wants exactly
// "mark, and tell me if this is a repeat".
func (c *SeenCache) Seen(k SeenKey, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	nu := now.Unix()
	at, ok := c.entries[k]
	if ok && (c.ttl <= 0 || now.Sub(time.Unix(at, 0)) <= c.ttl) {
		c.entries[k] = nu // refresh recency
		return true
	}
	c.entries[k] = nu
	if len(c.entries) > c.cap {
		c.evict(nu)
	}
	return false
}

// evict removes expired entries first, then the oldest ~10% (approximate
// LRU — exactness is not needed for a dedup hint). Caller holds the lock.
func (c *SeenCache) evict(nowUnix int64) {
	if c.ttl > 0 {
		limit := nowUnix - int64(c.ttl/time.Second)
		for k, at := range c.entries {
			if at < limit {
				delete(c.entries, k)
			}
		}
		if len(c.entries) <= c.cap {
			return
		}
	}
	type kv struct {
		k  SeenKey
		at int64
	}
	all := make([]kv, 0, len(c.entries))
	for k, at := range c.entries {
		all = append(all, kv{k, at})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at < all[j].at })
	drop := len(c.entries) - c.cap + c.cap/10
	for i := 0; i < drop && i < len(all); i++ {
		delete(c.entries, all[i].k)
	}
}

// Len reports the live entry count.
func (c *SeenCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// ---- snapshot (length ‖ crc32c ‖ record, the eventlog segment discipline) ----

var crcTable = crc32.MakeTable(crc32.Castagnoli)

const seenRecLen = 16 + 8 // key + unix seconds

// Save atomically writes a snapshot (tmp + rename).
func (c *SeenCache) Save(path string) error {
	c.mu.Lock()
	recs := make([]byte, 0, len(c.entries)*seenRecLen)
	for k, at := range c.entries {
		recs = append(recs, k[:]...)
		recs = binary.BigEndian.AppendUint64(recs, uint64(at))
	}
	c.mu.Unlock()

	var buf []byte
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(recs)))
	buf = binary.BigEndian.AppendUint32(buf, crc32.Checksum(recs, crcTable))
	buf = append(buf, recs...)

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load replaces the cache contents from a snapshot. A missing file is a
// clean start; a corrupt one is discarded (cost: airtime, not correctness).
func (c *SeenCache) Load(path string, now time.Time) error {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	var hdr [8]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return nil // torn header → clean start
	}
	n := binary.BigEndian.Uint32(hdr[:4])
	want := binary.BigEndian.Uint32(hdr[4:])
	if n%seenRecLen != 0 || n > uint32(c.cap*seenRecLen*2) {
		return nil
	}
	recs := make([]byte, n)
	if _, err := io.ReadFull(f, recs); err != nil {
		return nil
	}
	if crc32.Checksum(recs, crcTable) != want {
		return nil // corrupt snapshot → clean start
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[SeenKey]int64, n/seenRecLen)
	limit := int64(0)
	if c.ttl > 0 {
		limit = now.Unix() - int64(c.ttl/time.Second)
	}
	for off := uint32(0); off < n; off += seenRecLen {
		var k SeenKey
		copy(k[:], recs[off:off+16])
		at := int64(binary.BigEndian.Uint64(recs[off+16 : off+24]))
		if c.ttl > 0 && at < limit {
			continue // expired while we were down
		}
		c.entries[k] = at
	}
	return nil
}

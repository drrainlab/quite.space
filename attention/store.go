package attention

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
)

// Persistence. Everything here is device-local and sealed; none of it is
// ever emitted into a log, packed into a bundle, or handed to a relay.
//
// A single sealed blob is capped at 1 MiB by the storage layer, and the
// vector cache and signal ring can both outgrow that, so every collection is
// SHARDED with an explicit byte budget rather than trusting a record count.

// Sealed is the subset of the node's sealed storage this package needs. Taking
// an interface keeps the package testable without a data root.
type Sealed interface {
	SaveSealed(name string, data []byte) error
	LoadSealed(name string) ([]byte, error)
	ListSealed(prefix string) ([]string, error)
	DeleteSealed(name string) error
}

const (
	// shardBudget stays well under the 1 MiB sealed ceiling so a shard can
	// never fail to write because of encoding overhead.
	shardBudget = 512 << 10

	namePrefixSignals  = "attention-signals-"
	namePrefixSeen     = "attention-seen-"
	nameProfileLexical = "attention-profile-lexical"
)

// SaveInbox writes the signal ring and the seen-set as sharded blobs.
func SaveInbox(s Sealed, in *Inbox) error {
	if err := saveShards(s, namePrefixSignals, len(in.Signals), func(i int) (any, error) {
		return in.Signals[i], nil
	}); err != nil {
		return err
	}
	recs := in.SeenSnapshot()
	return saveSeenShards(s, recs)
}

// LoadInbox restores the ring and the seen-set.
func LoadInbox(s Sealed) (*Inbox, error) {
	in := NewInbox()
	names, err := s.ListSealed(namePrefixSignals)
	if err != nil {
		return in, err
	}
	sort.Strings(names)
	for _, n := range names {
		raw, err := s.LoadSealed(n)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return in, err
		}
		var batch []Signal
		if json.Unmarshal(raw, &batch) != nil {
			continue // a corrupt shard costs history, never correctness
		}
		in.Signals = append(in.Signals, batch...)
	}
	seen, err := loadSeenShards(s)
	if err != nil {
		return in, err
	}
	in.LoadSeen(seen)
	return in, nil
}

// saveShards writes items into numbered shards, rolling over on byte budget.
func saveShards(s Sealed, prefix string, n int, get func(int) (any, error)) error {
	if err := clearShards(s, prefix); err != nil {
		return err
	}
	shard := 0
	batch := []any{}
	size := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		raw, err := json.Marshal(batch)
		if err != nil {
			return err
		}
		if err := s.SaveSealed(shardName(prefix, shard), raw); err != nil {
			return err
		}
		shard++
		batch = batch[:0]
		size = 0
		return nil
	}
	for i := range n {
		item, err := get(i)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(item)
		if err != nil {
			return err
		}
		if size+len(raw) > shardBudget && len(batch) > 0 {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, item)
		size += len(raw) + 1
	}
	return flush()
}

// The seen-set is written in a compact binary form: 4096 records of a
// 32-byte id plus a timestamp is far too much JSON for what it is.
func saveSeenShards(s Sealed, recs []SeenRecord) error {
	if err := clearShards(s, namePrefixSeen); err != nil {
		return err
	}
	const recSize = 40 // 32-byte event id + int64
	perShard := shardBudget / recSize
	for shard := 0; shard*perShard < len(recs); shard++ {
		end := min((shard+1)*perShard, len(recs))
		part := recs[shard*perShard : end]
		buf := make([]byte, 0, len(part)*recSize)
		for _, r := range part {
			buf = append(buf, r.Event[:]...)
			buf = binary.BigEndian.AppendUint64(buf, uint64(r.ReceivedAt))
		}
		if err := s.SaveSealed(shardName(namePrefixSeen, shard), buf); err != nil {
			return err
		}
	}
	return nil
}

func loadSeenShards(s Sealed) ([]SeenRecord, error) {
	names, err := s.ListSealed(namePrefixSeen)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []SeenRecord
	for _, n := range names {
		raw, err := s.LoadSealed(n)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return out, err
		}
		for off := 0; off+40 <= len(raw); off += 40 {
			var r SeenRecord
			copy(r.Event[:], raw[off:off+32])
			r.ReceivedAt = int64(binary.BigEndian.Uint64(raw[off+32 : off+40]))
			out = append(out, r)
		}
	}
	return out, nil
}

// SaveModel persists the lexical ranking head in a compact binary form.
// The SEMANTIC calibration lives under its own key (AT-0B) so swapping the
// encoder never erases what the lexical layer learned about this person.
func SaveModel(s Sealed, m *Model) error {
	if m == nil {
		return nil
	}
	idxs := make([]int, 0, len(m.W))
	for i := range m.W {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	buf := make([]byte, 0, 12+len(idxs)*8)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(idxs)))
	buf = binary.BigEndian.AppendUint32(buf, uint32(m.Positive))
	buf = binary.BigEndian.AppendUint32(buf, uint32(m.Negative))
	buf = binary.BigEndian.AppendUint32(buf, math.Float32bits(m.Bias))
	for _, i := range idxs {
		buf = binary.BigEndian.AppendUint32(buf, uint32(i))
		buf = binary.BigEndian.AppendUint32(buf, math.Float32bits(m.W[i]))
	}
	return s.SaveSealed(nameProfileLexical, buf)
}

func LoadModel(s Sealed) (*Model, error) {
	m := NewModel()
	raw, err := s.LoadSealed(nameProfileLexical)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, nil
		}
		return m, err
	}
	if len(raw) < 16 {
		return m, nil
	}
	n := int(binary.BigEndian.Uint32(raw[0:4]))
	m.Positive = int(binary.BigEndian.Uint32(raw[4:8]))
	m.Negative = int(binary.BigEndian.Uint32(raw[8:12]))
	m.Bias = math.Float32frombits(binary.BigEndian.Uint32(raw[12:16]))
	off := 16
	for range n {
		if off+8 > len(raw) {
			break
		}
		idx := int(binary.BigEndian.Uint32(raw[off : off+4]))
		w := math.Float32frombits(binary.BigEndian.Uint32(raw[off+4 : off+8]))
		m.W[idx] = w
		off += 8
	}
	return m, nil
}

// DeleteProfile erases the learned model. "Delete what it learned about me"
// has to actually delete.
func DeleteProfile(s Sealed) error { return s.DeleteSealed(nameProfileLexical) }

// DeleteAll erases every attention artefact on this device.
func DeleteAll(s Sealed) error {
	if err := clearShards(s, namePrefixSignals); err != nil {
		return err
	}
	if err := clearShards(s, namePrefixSeen); err != nil {
		return err
	}
	return DeleteProfile(s)
}

func clearShards(s Sealed, prefix string) error {
	names, err := s.ListSealed(prefix)
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := s.DeleteSealed(n); err != nil {
			return err
		}
	}
	return nil
}

func shardName(prefix string, i int) string { return fmt.Sprintf("%s%04d", prefix, i) }

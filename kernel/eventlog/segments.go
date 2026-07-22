// Segment storage (ADR-006): received canonical frames are persisted
// verbatim in append-only segment files, each record framed as
// length ‖ crc32c ‖ frame-bytes. Append is the only mutation. A torn tail
// record (crash mid-write) is truncated on open; corruption anywhere else
// is an error, never silently skipped.
package eventlog

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const (
	recordHeaderLen = 8        // uint32 length + uint32 crc32c
	maxSegmentSize  = 64 << 20 // roll threshold (ADR-006)
	maxRecordLen    = 1 << 20
	segmentPattern  = "%06d.seg"
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// SegmentStore persists frames for one terminal's log.
type SegmentStore struct {
	dir      string
	cur      *os.File
	curSize  int64
	curIndex int
}

// OpenSegments opens (creating if needed) the segment directory, validates
// the last segment's tail, and prepares for appends.
func OpenSegments(dir string) (*SegmentStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	indexes, err := listSegments(dir)
	if err != nil {
		return nil, err
	}
	s := &SegmentStore{dir: dir}
	if len(indexes) == 0 {
		s.curIndex = 1
		return s, s.openCurrent(os.O_CREATE | os.O_WRONLY | os.O_APPEND)
	}
	last := indexes[len(indexes)-1]
	validLen, err := validateSegment(s.segmentPath(last), true)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.segmentPath(last), os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(validLen); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	s.curIndex = last
	s.curSize = validLen
	return s, s.openCurrent(os.O_WRONLY | os.O_APPEND)
}

func (s *SegmentStore) segmentPath(index int) string {
	return filepath.Join(s.dir, fmt.Sprintf(segmentPattern, index))
}

func (s *SegmentStore) openCurrent(flags int) error {
	f, err := os.OpenFile(s.segmentPath(s.curIndex), flags, 0o600)
	if err != nil {
		return err
	}
	s.cur = f
	return nil
}

func listSegments(dir string) ([]int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var indexes []int
	for _, e := range entries {
		var idx int
		if _, err := fmt.Sscanf(e.Name(), segmentPattern, &idx); err == nil {
			indexes = append(indexes, idx)
		}
	}
	sort.Ints(indexes)
	return indexes, nil
}

// validateSegment scans records; returns the byte length of the valid
// prefix. A torn/corrupt tail is tolerated only when tailOK is true (last
// segment); mid-file corruption is always an error.
func validateSegment(path string, tailOK bool) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var offset int64
	header := make([]byte, recordHeaderLen)
	for {
		_, err := io.ReadFull(f, header)
		if err == io.EOF {
			return offset, nil
		}
		if err != nil { // partial header
			if tailOK {
				return offset, nil
			}
			return 0, fmt.Errorf("eventlog: torn header at %d in %s", offset, path)
		}
		length := binary.BigEndian.Uint32(header[0:4])
		sum := binary.BigEndian.Uint32(header[4:8])
		if length == 0 || length > maxRecordLen {
			if tailOK {
				return offset, nil
			}
			return 0, fmt.Errorf("eventlog: bad record length at %d in %s", offset, path)
		}
		frame := make([]byte, length)
		if _, err := io.ReadFull(f, frame); err != nil {
			if tailOK {
				return offset, nil
			}
			return 0, fmt.Errorf("eventlog: torn record at %d in %s", offset, path)
		}
		if crc32.Checksum(frame, crcTable) != sum {
			if tailOK {
				return offset, nil
			}
			return 0, fmt.Errorf("eventlog: crc mismatch at %d in %s", offset, path)
		}
		offset += int64(recordHeaderLen) + int64(length)
	}
}

// Append persists one frame durably (fsync per append in v0).
func (s *SegmentStore) Append(frame []byte) error {
	if len(frame) == 0 || len(frame) > maxRecordLen {
		return fmt.Errorf("eventlog: frame length %d out of range", len(frame))
	}
	if s.curSize >= maxSegmentSize {
		if err := s.cur.Close(); err != nil {
			return err
		}
		s.curIndex++
		s.curSize = 0
		if err := s.openCurrent(os.O_CREATE | os.O_WRONLY | os.O_APPEND); err != nil {
			return err
		}
	}
	rec := make([]byte, recordHeaderLen+len(frame))
	binary.BigEndian.PutUint32(rec[0:4], uint32(len(frame)))
	binary.BigEndian.PutUint32(rec[4:8], crc32.Checksum(frame, crcTable))
	copy(rec[recordHeaderLen:], frame)
	if _, err := s.cur.Write(rec); err != nil {
		return err
	}
	if err := s.cur.Sync(); err != nil {
		return err
	}
	s.curSize += int64(len(rec))
	return nil
}

// Scan replays every stored frame in append order.
func (s *SegmentStore) Scan(fn func(frame []byte) error) error {
	indexes, err := listSegments(s.dir)
	if err != nil {
		return err
	}
	for i, idx := range indexes {
		lastSegment := i == len(indexes)-1
		path := s.segmentPath(idx)
		validLen, err := validateSegment(path, lastSegment)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		header := make([]byte, recordHeaderLen)
		var offset int64
		for offset < validLen {
			if _, err := io.ReadFull(f, header); err != nil {
				f.Close()
				return err
			}
			length := binary.BigEndian.Uint32(header[0:4])
			frame := make([]byte, length)
			if _, err := io.ReadFull(f, frame); err != nil {
				f.Close()
				return err
			}
			offset += int64(recordHeaderLen) + int64(length)
			if err := fn(frame); err != nil {
				f.Close()
				return err
			}
		}
		f.Close()
	}
	return nil
}

// Close releases the current segment handle.
func (s *SegmentStore) Close() error {
	if s.cur != nil {
		return s.cur.Close()
	}
	return nil
}

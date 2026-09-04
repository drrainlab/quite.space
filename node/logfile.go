// Where a node's log goes when nobody is watching stderr.
//
// A desktop launched from /Applications has a stderr that nobody reads; the
// terminal that would have shown it does not exist. Every line the node
// said — the relay that refused, the frame that could not travel, the route
// that was learned and not persisted — went to a descriptor the window
// server discards, and a field report about "the room stayed empty"
// arrived with no log to read. This file gives those lines a place: a
// rolling file inside the data directory, owner-readable only, bounded in
// size so a node that runs for a year does not fill a disk.
//
// TWO RULES, both older than this file. The log follows the data: nothing
// touches the disk until the data directory exists, because a launch on a
// fresh machine must leave the disk exactly as it was until somebody chooses
// a passphrase (see cmd/desktop NewShell). And the log is not a diary: it
// receives what the standard logger receives — verdicts, counts, short ids
// — never a request line, a body, or a key. logfile_test.go reads every
// log call in this package and the desktop shell to keep it that way.
package node

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// logFileName under <dataDir>/logs.
	logFileName = "node.log"
	// logMaxBytes is where one file stops and the next begins. Two
	// megabytes is weeks of a quiet node and a day of a loud one; with
	// logKeep rotated copies the whole history is bounded at ~8 MB.
	logMaxBytes = 2 << 20
	logKeep     = 3
	// logBacklogMax bounds what is remembered while the data directory
	// does not exist yet. The lines from before first run are few and
	// short; if they are not, the oldest go.
	logBacklogMax = 64 << 10
)

// RollingLog is an io.Writer for the standard logger that lands in
// <dataDir>/logs/node.log, rotating by size and keeping a few generations.
// Each line is stamped with a UTC time — the standard logger's own flags
// stay the caller's business, so stderr keeps looking the way it did.
type RollingLog struct {
	dataDir  string
	maxBytes int64
	keep     int
	now      func() time.Time

	mu      sync.Mutex
	f       *os.File
	size    int64
	backlog []byte
	closed  bool
}

// NewRollingLog prepares the writer. It creates nothing: the first write
// after the data directory exists creates logs/ and the file; writes before
// that are held in memory (bounded) and flushed then.
func NewRollingLog(dataDir string) *RollingLog {
	return &RollingLog{dataDir: dataDir, maxBytes: logMaxBytes, keep: logKeep, now: time.Now}
}

// Path is where the lines go — for the line that tells a person where to
// look.
func (l *RollingLog) Path() string {
	return filepath.Join(l.dataDir, "logs", logFileName)
}

// Write stamps and stores one logger record. It never fails the caller for
// a disk problem — the standard logger discards the error anyway, and a log
// that cannot be written must not become a reason the node cannot run.
func (l *RollingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return len(p), nil
	}
	line := make([]byte, 0, len(p)+32)
	line = append(line, l.now().UTC().Format(time.RFC3339)...)
	line = append(line, ' ')
	line = append(line, p...)
	if len(line) == 0 || line[len(line)-1] != '\n' {
		line = append(line, '\n')
	}
	if l.f == nil && !l.open() {
		l.remember(line)
		return len(p), nil
	}
	if l.size > 0 && l.size+int64(len(line)) > l.maxBytes {
		l.rotate()
	}
	if l.f == nil {
		l.remember(line)
		return len(p), nil
	}
	n, err := l.f.Write(line)
	l.size += int64(n)
	if err != nil {
		// The file went away underneath us (a cleared data directory, a
		// full disk). Start over next time rather than write into nothing.
		_ = l.f.Close()
		l.f, l.size = nil, 0
	}
	return len(p), nil
}

// remember keeps a line for later, dropping the oldest whole lines when
// the backlog is full.
func (l *RollingLog) remember(line []byte) {
	l.backlog = append(l.backlog, line...)
	for len(l.backlog) > logBacklogMax {
		nl := bytes.IndexByte(l.backlog, '\n')
		if nl < 0 {
			l.backlog = nil
			return
		}
		l.backlog = l.backlog[nl+1:]
	}
}

// open creates logs/ and the file, if and only if the data directory
// already exists. Returns false when it does not (yet). Held lines are
// flushed first, so the file reads in order.
func (l *RollingLog) open() bool {
	if fi, err := os.Stat(l.dataDir); err != nil || !fi.IsDir() {
		return false
	}
	dir := filepath.Join(l.dataDir, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	path := filepath.Join(dir, logFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return false
	}
	// A file that existed before with looser bits is tightened rather than
	// trusted: the mode in OpenFile applies only to creation.
	_ = f.Chmod(0o600)
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return false
	}
	l.f, l.size = f, fi.Size()
	if len(l.backlog) > 0 {
		if l.size > 0 && l.size+int64(len(l.backlog)) > l.maxBytes {
			l.rotate()
			if l.f == nil {
				return false
			}
		}
		n, _ := l.f.Write(l.backlog)
		l.size += int64(n)
		l.backlog = nil
	}
	return true
}

// rotate shifts node.log → node.log.1 → … → node.log.<keep> and opens a
// fresh file. The oldest generation falls off the end.
func (l *RollingLog) rotate() {
	if l.f != nil {
		_ = l.f.Close()
		l.f, l.size = nil, 0
	}
	path := l.Path()
	for i := l.keep; i >= 1; i-- {
		from := path
		if i > 1 {
			from = fmt.Sprintf("%s.%d", path, i-1)
		}
		_ = os.Rename(from, fmt.Sprintf("%s.%d", path, i))
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	l.f, l.size = f, 0
}

// Close releases the file. Writes after Close are accepted and dropped, so
// a logger that outlives the shutdown cannot panic the process on its way
// out.
func (l *RollingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

var _ io.WriteCloser = (*RollingLog)(nil)

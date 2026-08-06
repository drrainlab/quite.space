package node

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// SD-0 — deleting a space, and being honest about what that can mean.
//
// A PERSON CAN DELETE THEIR COPY. Nobody can delete anybody else's, and
// pretending otherwise would be the one unforgivable lie here: there is no
// "delete for everyone" in a system where every device holds its own log and
// may be offline for a month. So this removes the space FROM THIS DEVICE —
// its events, its keys, its drafts, and the media nothing else refers to —
// and says so in those words wherever a person can see it.
//
// It is not a protocol operation and emits nothing. Announcing a departure is
// a separate decision with its own design (a membership event others apply),
// and building it into deletion would mean a person cannot leave quietly —
// which, for somebody deleting a conversation, is often the entire point.
//
// THE COMMIT POINT IS ONE KEYSTORE WRITE. Everything before it is preparation
// and everything after it is cleanup, so a process that dies mid-way leaves a
// space that is already gone with files that are not — never a half-deleted
// space that still opens. [sweepForgotten] finishes the job at the next open.
//
// WHAT IS DELETED, and it is worth naming exactly:
//
//	the event log      — every message, both directions
//	the terminal seed  — for a space this device created
//	the epoch keys     — so the bytes could not be read even if they survived
//	drafts             — sealed, node-private, and keyed by space
//	media              — ONLY blobs no remaining space refers to (see below)
//	the notification watermark — so a later re-join starts clean
//
// WHAT SURVIVES, deliberately: blobs another space still refers to. Media is
// content-addressed and shared — the same photo sent in two rooms is stored
// once — so deleting it with the first room would take it out of the second.
//
// RE-JOINING LATER IS ALLOWED. The tombstone stops a join saga that was
// already in flight from bringing the space back behind the person's back; it
// does not stop them from deliberately opening the pass again tomorrow. One
// is a decision, the other is a leftover.

// ErrNoSuchSpace is returned when a space is not on this device — including
// when it has already been deleted, which is the same thing from here.
var ErrNoSuchSpace = errors.New("node: no such space on this device")

// DeleteSpace removes a space from THIS DEVICE. See the note above for what
// that does and does not mean.
func (r *Runtime) DeleteSpace(tid id.TerminalID) error {
	r.mu.Lock()
	st, known := r.spaces[tid]
	if !known {
		r.mu.Unlock()
		return ErrNoSuchSpace
	}

	// 1. Detach it from everything running. Done before the commit so no
	// goroutine can be holding it open when the files go.
	delete(r.spaces, tid)
	delete(r.relayWants, tid)
	delete(r.replyBoxes, tid)
	// A join saga still in flight for this space would re-adopt it after the
	// delete — a resurrection the person never asked for twice.
	for reqID, at := range r.joins {
		if at.space == tid {
			delete(r.joins, reqID)
		}
	}
	if r.notifyLedger != nil {
		r.notifyLedger.forget(tid)
	}
	blobs := r.unreferencedAfterLocked(tid)

	// 2. THE COMMIT. After this write the space does not exist here, whatever
	// happens next.
	delete(r.ks.Spaces, tid)
	delete(r.ks.TerminalSeeds, tid)
	delete(r.ks.Epochs, tid)
	if r.ks.Forgotten == nil {
		r.ks.Forgotten = map[id.TerminalID]int64{}
	}
	r.ks.Forgotten[tid] = time.Now().Unix()
	if err := r.root.SaveKeystore(r.ks); err != nil {
		// Nothing was removed from disk yet and the space is still attached
		// nowhere: put it back rather than leave the person with a space that
		// is half here.
		r.spaces[tid] = st
		r.mu.Unlock()
		return fmt.Errorf("node: could not record the deletion: %w", err)
	}
	root := r.root
	forgot := r.notify.forgetHook()
	r.mu.Unlock()

	// TOLD BEFORE THE FILES GO, and after the commit — the host has surfaces
	// of its own to take down and no way to learn about this otherwise. If it
	// throws, that is its problem and not a reason to leave the events on
	// disk: the deletion is already committed.
	if forgot != nil {
		func() {
			defer func() { _ = recover() }()
			forgot(tid)
		}()
	}

	// 3. Cleanup. Failures here are reported but do not un-delete anything —
	// the space is already gone, and the sweep at the next open will retry.
	var errs []string
	if err := st.space.Log.Close(); err != nil {
		errs = append(errs, "closing the log: "+err.Error())
	}
	if err := os.RemoveAll(root.EventsDir(tid)); err != nil {
		errs = append(errs, "removing the events: "+err.Error())
	}
	if err := r.deleteSealedFor(tid); err != nil {
		errs = append(errs, "removing the drafts: "+err.Error())
	}
	for _, h := range blobs {
		if err := root.DeleteBlob(h); err != nil {
			errs = append(errs, "removing media: "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("node: the space is deleted, but some of it is still on disk (%s)",
			strings.Join(errs, "; "))
	}
	return nil
}

// unreferencedAfterLocked lists the blobs that would be left with no space
// referring to them once tid is gone, and drops tid from the index.
//
// The index already answers this: it records which spaces publish each wire
// id, because a space may only fetch what it can prove it was offered. That
// makes it a reference count nobody had to invent.
func (r *Runtime) unreferencedAfterLocked(tid id.TerminalID) []id.Hash {
	if r.assetIdx == nil {
		return nil
	}
	var orphans []id.Hash
	for h, spaces := range r.assetIdx.wireSpaces {
		if _, ours := spaces[tid]; !ours {
			continue
		}
		delete(spaces, tid)
		if len(spaces) == 0 {
			delete(r.assetIdx.wireSpaces, h)
			orphans = append(orphans, h)
		}
	}
	for k := range r.assetIdx.refs {
		if k.Space == tid {
			delete(r.assetIdx.refs, k)
			delete(r.assetIdx.fetching, k)
			delete(r.assetIdx.failed, k)
			delete(r.assetIdx.silent, k)
		}
	}
	for h, k := range r.assetIdx.manifestOwner {
		if k.Space == tid {
			delete(r.assetIdx.manifestOwner, h)
		}
	}
	delete(r.assetIdx.refOrder, tid)
	return orphans
}

// deleteSealedFor removes the node-private sealed blobs belonging to a space.
// Their names carry the space's short hex, which is the whole reason they can
// be found again without a second index.
func (r *Runtime) deleteSealedFor(tid id.TerminalID) error {
	names, err := r.root.ListSealed("")
	if err != nil {
		return err
	}
	mark := tid.Hex()[:16]
	var last error
	for _, n := range names {
		if !strings.Contains(n, mark) {
			continue
		}
		if err := r.root.DeleteSealed(n); err != nil {
			last = err
		}
	}
	return last
}

// sweepForgotten finishes deletions the process did not live to finish.
//
// Called at open, once, before anything is served. A tombstone with events
// still on disk is exactly the crash window this exists for — and the events
// of a space nobody lists are not merely clutter: they are the messages
// somebody asked to be rid of, still readable by anything with the key.
func (r *Runtime) sweepForgotten() {
	for tid := range r.ks.Forgotten {
		if _, alive := r.ks.Spaces[tid]; alive {
			// Deleted and then deliberately joined again. The tombstone has
			// done its job and must not now delete the new copy.
			delete(r.ks.Forgotten, tid)
			continue
		}
		dir := r.root.EventsDir(tid)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			continue
		}
		_ = r.deleteSealedFor(tid)
	}
}

// ForgottenSpaces reports how many spaces this device was told to forget, and
// when the most recent one was. Counts only — a tombstone that could tell you
// what it buried would not be a tombstone.
func (r *Runtime) ForgottenSpaces() (count int, mostRecentUnix int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, at := range r.ks.Forgotten {
		count++
		if at > mostRecentUnix {
			mostRecentUnix = at
		}
	}
	return count, mostRecentUnix
}

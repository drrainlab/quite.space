// Corpus generation, and the manifest that makes "the same corpus" a CHECKED
// FACT rather than an agreement between two machines.
//
// Two corpora, because one cannot answer both questions this wave asks:
//
//	A — controlled       uniform small text events. Its uniformity is the
//	                     point: it is the comparison corpus (desktop vs phone)
//	                     and the axis the replay-shape classification runs on.
//	B — beta-realistic   several spaces, private and public, mixed event
//	                     sizes, reactions. The corpus that predicts a real
//	                     user, and therefore the one whose ms/event is worth
//	                     quoting a year from now.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/drrainlab/quiet_places/node"
)

// corpusManifest travels with the bytes. Every field exists because without it
// two runs could differ in a way nobody would notice.
type corpusManifest struct {
	Kind       string            `json:"kind"`
	Spaces     int               `json:"spaces"`
	EventsEach int               `json:"events_each"`
	Passphrase string            `json:"passphrase"`
	Seed       int64             `json:"seed"`
	Tool       string            `json:"tool"`
	Events     int               `json:"events"`
	Bytes      int64             `json:"bytes"`
	Segments   int               `json:"segments"`
	PrivateN   int               `json:"private_spaces"`
	PublicN    int               `json:"public_spaces"`
	Files      map[string]string `json:"files"` // relative path → sha256
}

// generateCorpus builds a data directory and stamps a manifest beside it.
//
// The passphrase is IN the manifest on purpose. This is a measurement fixture,
// never an identity: the whole directory is disposable, it is copied for every
// run, and a fixture whose passphrase lived somewhere else would be a fixture
// somebody eventually runs with the wrong one.
func generateCorpus(dir, kind, pass string, spaces, eventsEach int) (*corpusManifest, error) {
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	rt, err := node.Open(dir, []byte(pass), "corpus")
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}

	m := &corpusManifest{
		Kind: kind, Spaces: spaces, EventsEach: eventsEach,
		Passphrase: pass, Tool: toolVersion, Files: map[string]string{},
	}

	for s := 0; s < spaces; s++ {
		tid, err := rt.CreateSpace(fmt.Sprintf("corpus-%s-%02d", kind, s))
		if err != nil {
			rt.Close()
			return nil, fmt.Errorf("create space %d: %w", s, err)
		}
		m.PrivateN++
		for e := 0; e < eventsEach; e++ {
			if _, err := rt.Say(tid, corpusText(kind, s, e), node.SayOptions{}); err != nil {
				rt.Close()
				return nil, fmt.Errorf("say %d/%d: %w", s, e, err)
			}
			m.Events++
		}
	}
	rt.Close()

	if err := stampManifest(dir, m); err != nil {
		return nil, err
	}
	return m, nil
}

// corpusText is deterministic: the same (kind, space, index) always produces
// the same bytes, so a corpus regenerated on another machine hashes the same.
// No clock, no randomness — the two things that would quietly break that.
func corpusText(kind string, s, e int) string {
	switch kind {
	case "A":
		// Uniform and small. Nothing interesting, which is the point.
		return fmt.Sprintf("corpus A space %02d event %06d", s, e)
	default:
		// Mixed sizes, in a fixed rotation so it stays reproducible. A real
		// log is not a column of identical rows, and replay cost per event
		// depends on what the events are.
		switch e % 5 {
		case 0:
			return fmt.Sprintf("s%02d e%06d ok", s, e)
		case 1:
			return fmt.Sprintf("s%02d e%06d %s", s, e, strings.Repeat("a longer line of ordinary conversation. ", 4))
		case 2:
			return fmt.Sprintf("s%02d e%06d %s", s, e, strings.Repeat("x", 900))
		case 3:
			return fmt.Sprintf("s%02d e%06d — с юникодом, потому что настоящий лог не только ASCII", s, e)
		default:
			return fmt.Sprintf("s%02d e%06d %s", s, e, strings.Repeat("mid ", 40))
		}
	}
}

func stampManifest(dir string, m *corpusManifest) error {
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == manifestName {
			return nil
		}
		// node.lock is a live artefact of whoever last held the directory and
		// is legitimately empty; hashing it would make the manifest depend on
		// the run rather than on the corpus.
		if rel == "node.lock" {
			return nil
		}
		sum, n, err := hashFile(p)
		if err != nil {
			return err
		}
		m.Files[rel] = sum
		m.Bytes += n
		if strings.Contains(rel, "events") {
			m.Segments++
		}
		return nil
	})
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, manifestName), b, 0o600)
}

func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func readManifest(dir string) (*corpusManifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, err
	}
	var m corpusManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// verifyCorpus re-hashes every file and reports what disagrees.
//
// A mismatch ABORTS the run. Two numbers taken over two different histories
// look exactly as comparable as two numbers taken over one, which is why this
// is a hard stop and not a warning.
func verifyCorpus(dir string) (*corpusManifest, error) {
	m, err := readManifest(dir)
	if err != nil {
		return nil, fmt.Errorf("no corpus manifest in %s: %w", dir, err)
	}
	var bad []string
	seen := map[string]bool{}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == manifestName || rel == "node.lock" {
			return nil
		}
		seen[rel] = true
		want, ok := m.Files[rel]
		if !ok {
			bad = append(bad, "unexpected file "+rel)
			return nil
		}
		got, _, err := hashFile(p)
		if err != nil {
			return err
		}
		if got != want {
			bad = append(bad, "changed "+rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for rel := range m.Files {
		if !seen[rel] {
			bad = append(bad, "missing "+rel)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return nil, fmt.Errorf("corpus does not match its manifest: %s", strings.Join(bad, "; "))
	}
	return m, nil
}

// copyCorpus makes the fresh copy a run measures against. Every run gets its
// own, because opening a data directory mutates it — Open always rewrites the
// keystore — and a second run over a mutated corpus is not a repeat of the
// first.
func copyCorpus(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if rel == "node.lock" {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

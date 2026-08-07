// The words the ordinary interface may not say (CAT-0b).
//
// The wave's governing rule is that a person browses PLACES, not sources,
// replicas, kinds or publish modes. Copy drifts back — somebody adds a
// tooltip, a diagnostic string leaks into a button — and a review does not
// catch it twice in a row. A grep does.
//
// This is a copy test, not an architecture test. It reads the shipped
// interface strings and fails on a mechanism word appearing where an
// ordinary person would meet it. The Advanced surfaces and the diagnostics
// screens are exempt BY LINE, not by file, so the exemption cannot quietly
// widen to cover the whole client.
package node

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// banned are words that describe a MECHANISM rather than an intention.
// Each one is a thing the person did not choose and cannot act on.
//
// Matched on WORD BOUNDARIES, which is not pedantry: "curated" is a
// publish mode nobody picked, while "curators" are named people somebody
// deliberately added — the second is a product concept and stays.
var banned = []string{
	"broadcast", "curated", "publish mode", "replica", "replicas",
	"projection", "space card", "space-card", "kind_hint",
}

var (
	htmlTag  = regexp.MustCompile(`<[^>]*>`)
	i18nKey  = regexp.MustCompile(`^\s*'[a-z0-9_.]+'\s*:`)
	wordOnly = map[string]*regexp.Regexp{}
)

func init() {
	for _, w := range banned {
		wordOnly[w] = regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(w) + `\b`)
	}
}

// visible reduces a source line to what a person could actually READ.
//
// Without this the test flags `data-v="broadcast"` and
// `onclick="setAccessMode('curated')"` — machine identifiers that no
// person meets — and a test that cries wolf gets deleted rather than
// obeyed. HTML keeps only its text nodes; an i18n line keeps only its
// value, because the KEY is a name for us, not copy.
func visible(line string, html bool) string {
	if html {
		return htmlTag.ReplaceAllString(line, " ")
	}
	if loc := i18nKey.FindStringIndex(line); loc != nil {
		return line[loc[1]:]
	}
	return line
}

// exemptLine marks a line as belonging to a surface where the machinery is
// the subject: the Directories sheet, the protocol view, diagnostics, and
// anything that is plainly a comment rather than copy.
func exemptLine(line string) bool {
	t := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(t, "//"), strings.HasPrefix(t, "/*"), strings.HasPrefix(t, "*"):
		return true // a comment is for us, not for a person
	case strings.HasPrefix(t, "<!--"):
		return true
	}
	// Named exemptions, each a surface a person reaches deliberately.
	for _, id := range []string{
		"dlgDirs", "dirList", // the Advanced Directories sheet
		"protoToggle", "Protocol view", // the technical layer, by request
		"relayPublicPanel", "diagnostics", "relayDiag", // operator screens
		"dlgSettings", "settings-cat", // settings names the machinery on purpose
	} {
		if strings.Contains(line, id) {
			return true
		}
	}
	return false
}

func TestTheOrdinaryInterfaceDoesNotSpeakInMechanisms(t *testing.T) {
	// Walk up out of node/ to the client's shipped copy.
	root := filepath.Join("..", "clients", "web-ui", "assets")
	files := []string{
		filepath.Join(root, "index.html"),
		filepath.Join(root, "i18n.js"),
	}
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("cannot read the shipped copy: %v", err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for n := 1; sc.Scan(); n++ {
			line := sc.Text()
			if exemptLine(line) {
				continue
			}
			text := visible(line, strings.HasSuffix(path, ".html"))
			for _, w := range banned {
				if wordOnly[w].MatchString(text) {
					t.Errorf("%s:%d says %q to an ordinary person:\n\t%s",
						filepath.Base(path), n, w, strings.TrimSpace(line))
				}
			}
		}
		if err := sc.Err(); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}

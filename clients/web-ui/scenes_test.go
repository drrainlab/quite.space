package webui

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// AM-3's budget, stated precisely: scenes receive only a bounded drawing API
// (assets/brush.js); this is a reviewed implementation boundary, not a
// security sandbox, because a scene runs in the same JavaScript realm as the
// app and could reach these names whatever API we hand it. What makes the
// boundary hold is that the modules are small, are read, and are checked here.
//
// A name on this list inside a scene module is a defect even when it would
// work, and the reason differs per line — so each says which.
var forbidden = []struct {
	pattern *regexp.Regexp
	why     string
}{
	{regexp.MustCompile(`\bfetch\b|\bXMLHttpRequest\b|\bWebSocket\b|\bEventSource\b|\bnavigator\b|\bsendBeacon\b`),
		"a scene must not reach the network: a recipe that phones home turns a post into a beacon"},
	{regexp.MustCompile(`\blocalStorage\b|\bsessionStorage\b|\bindexedDB\b|\bdocument\.cookie\b|\bcaches\b`),
		"a scene must not reach storage: it would let atmosphere carry state between posts"},
	// `top` and `parent` are browser globals too, and deliberately absent
	// here: they are far too plausible as ordinary local names in drawing
	// code, and a scene that actually reached for them would throw in the
	// flashcheck harness, whose vm context has no such globals at all.
	{regexp.MustCompile(`\bdocument\b|\bwindow\b|\blocation\b|\bpostMessage\b`),
		"a scene must not reach the page: it gets a brush, and the brush is the whole surface"},
	{regexp.MustCompile(`\beval\b|\bnew Function\b|\bimport\s*\(|\brequire\s*\(|\bWorker\b`),
		"a scene must not load or build code: ADR-013 invariant 2 forbids executable payload"},
	{regexp.MustCompile(`\bsetTimeout\b|\bsetInterval\b|\brequestAnimationFrame\b|\bqueueMicrotask\b`),
		"a scene must not schedule itself: stage.js owns the clock, and the frame budget with it"},
	{regexp.MustCompile(`\bMath\.random\b|\bDate\b|\bperformance\b|\bcrypto\b`),
		"a scene must be deterministic: the seed is inside a signature, so live randomness makes it lie"},
	{regexp.MustCompile(`\bfillText\b|\bstrokeText\b|\bmeasureText\b|\bdrawImage\b|\bgetContext\b|\bcanvas\b`),
		"a scene must not draw glyphs or images: one that can render text can forge system UI"},
}

// comments is deliberately crude — it strips // and /* */ so that prose about
// `document` does not fail a test about touching it. Scene modules are small
// and have no reason to hold string literals, so nothing further is needed.
var (
	lineComment  = regexp.MustCompile(`(?m)//.*$`)
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

func sceneModules(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join("assets", "scenes")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		out[e.Name()] = string(b)
	}
	return out
}

func TestSceneModulesTouchNothingButTheBrush(t *testing.T) {
	mods := sceneModules(t)
	if len(mods) == 0 {
		t.Skip("no scenes yet — AM-4 ships them, and this test is waiting")
	}
	for name, src := range mods {
		src = blockComment.ReplaceAllString(src, "")
		src = lineComment.ReplaceAllString(src, "")
		for _, f := range forbidden {
			if m := f.pattern.FindString(src); m != "" {
				t.Errorf("scenes/%s references %q: %s", name, m, f.why)
			}
		}
	}
}

// TestSceneLintDetectsWhatItClaimsTo keeps the test above honest while there
// are no scenes to check. A lint that skips is indistinguishable from a lint
// that matches nothing, and the difference only shows up on the day it
// matters — so the patterns are run against source that is deliberately full
// of what they forbid.
func TestSceneLintDetectsWhatItClaimsTo(t *testing.T) {
	bad := []string{
		`const r = await fetch('/api/spaces')`,
		`new WebSocket('wss://elsewhere.example')`,
		`localStorage.setItem('seen', '1')`,
		`document.querySelector('.bubble').remove()`,
		`window.location = 'https://elsewhere.example'`,
		`eval(recipe.params.code)`,
		`new Function('return 1')()`,
		`setInterval(tick, 16)`,
		`requestAnimationFrame(draw)`,
		`const n = Math.random()`,
		`const now = Date.now()`,
		`b.ctx.fillText('System', 10, 10)`,
		`host.getContext('2d').drawImage(img, 0, 0)`,
	}
	for _, src := range bad {
		matched := false
		for _, f := range forbidden {
			if f.pattern.MatchString(src) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("the scene lint would let this through: %s", src)
		}
	}

	// ...and does not fire on what a scene legitimately says. A lint nobody
	// can satisfy gets disabled, which is the same as not having one.
	good := []string{
		`b.glow(x, y, r, 1, 0.3)`,
		`const n = b.rand()`,
		`for (let i = 0; i < 40; i++) b.dot(px[i], py[i], 2, 2, 0.4)`,
		`b.line(x0, y0, x1, y1, 1.5, 0, 0.2)`,
		`t += dt * speed`,
		`b.wash(0, 0.04)`,
	}
	for _, src := range good {
		for _, f := range forbidden {
			if m := f.pattern.FindString(src); m != "" {
				t.Errorf("the scene lint rejects ordinary scene code %q on %q", src, m)
			}
		}
	}
}

// TestSceneFramesStayUnderTheFlashThreshold is the assertion AM-3 actually
// rests on. The brush's cap is a model of what a scene painted; this measures
// the sequence that model produces, across every scene, three seeds and every
// corner of each scene's parameter space — plus adversarial fixtures that try
// to strobe, so a green run means the cap held rather than that nobody tried.
//
// It shells out to node because the scenes are JavaScript and there is no
// other way to run them; the skip mirrors channelurl_test.go, which skips when
// its reference implementation is absent rather than pretending to pass.
func TestSceneFramesStayUnderTheFlashThreshold(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not available")
	}
	script := filepath.Join("..", "..", "scripts", "atmosphere", "flashcheck.cjs")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("the flash harness is missing: %v", err)
	}
	// The full sweep is around a minute: six scenes times three seeds times
	// every corner of a twelve-parameter space, four seconds each. That is the
	// gate, so it is the default. `-short` runs one seed for local iteration
	// and the harness prints how many cases it skipped, because a sweep that
	// quietly narrows itself looks exactly like a sweep that found nothing.
	args := []string{script}
	if testing.Short() {
		args = append(args, "--quick")
	}
	out, err := exec.Command(node, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("flashcheck failed: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}

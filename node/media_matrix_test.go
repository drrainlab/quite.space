package node

// PHASE 0 OF STREAM 1A — the differential stand (BETA_AUDIT_2026-08-20,
// plan "route honesty"). The reported production symptom is a TABLE:
//
//	text ✅   voice ✅   image ❌   file ❌     (everyone online)
//
// and the whole point of this file is to reproduce that table under
// instruments, per media KIND and per pipeline STAGE, before any fix is
// written. Voice is the built-in positive control: it uses the same
// asset machinery as images, so wherever its column diverges from the
// image column — that stage is the root, measured rather than argued.
//
// The stand is the production topology: the holder (a friend) on relay
// A; the person's laptop and paired phone on relay B; the friend↔laptop
// relationship formed through the PASS flow (JoinByPass), because that
// is the path production relationships actually took — a direct
// MintInvite records no routes on either side and models nothing real.
//
// TestMediaMatrixAcrossTwoRelays is the PRODUCT INVARIANT: every kind
// the composer offers arrives across relays. It is expected RED until
// the route-honesty + convergence fixes land, and its failure output IS
// the owner's table.
//
// ── G1 VERDICT (measured 2026-08-20, this stand, both tests) ─────────
//
//	kind          frame  want  who-saw-the-want                 state
//	voice         ✓      ✓     friend:NEVER  laptop:answer→B    fetching
//	photo-small   ✓      ✓     friend:NEVER  laptop:answer→B    fetching
//	photo-large   ✓      ✓     friend:NEVER  laptop:answer→B    fetching
//	video         ✓      ✓     friend:NEVER  laptop:answer→B    fetching
//	file          ✓      ✓     friend:NEVER  laptop:answer→B    fetching
//
// P1 CONFIRMED, twice over: (1) every kind behaves identically — voice
// was never special, the production report's healthy voice was the
// sibling cache (TestSiblingCacheDiagnosis: the kind the laptop fetched
// reached the phone, the kind it never touched starved); (2) the stage
// every kind dies at is the same one: THE TRUE HOLDER NEVER SEES THE
// WANT — the phone's want-bundle is tentative-put onto the phone's own
// relay by the zero-knowledge bootstrap, the friend never polls there,
// and the only node that does see it (the laptop, same relay) answers
// into its own emptiness because it holds no bytes. Phase 2 of the plan
// (route honesty + freight routes + bundle key 8) is therefore ungated.
// AFTER THE FIX (route honesty + freight routes + bundle key 8) the same
// table reads: every kind complete, friend:answer→relay-B — the true
// holder sees the want and answers at the wanter's stated relay. The
// matrix test below is now the enforced product invariant.
// ─────────────────────────────────────────────────────────────────────
//
// TestSiblingCacheDiagnosis (Phase 0) confirmed P1 and is superseded by
// TestNoSiblingCacheDependency below — the enforced negative invariant.

import (
	"bytes"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// emitKind puts one asset-bearing block of the given kind into a space,
// sized to cross a chosen branch of the asset pipeline (≤512KiB inline
// chunk refs vs external manifest).
func emitKind(t *testing.T, rt *Runtime, tid id.TerminalID, kind string, size int) *schemas.AssetRef {
	t.Helper()
	data := randBytes(t, size)
	ref, err := rt.IngestAsset(bytes.NewReader(data), int64(len(data)),
		assets.Metadata{MediaType: mediaTypeFor(kind), Role: "original", ChunkSize: assets.DefaultChunkSize})
	if err != nil {
		t.Fatal(err)
	}
	var payload []byte
	switch kind {
	case "voice":
		payload, err = (&schemas.VoiceBlock{DurationMS: 7000, Waveform: bytes.Repeat([]byte{8}, 48), Original: ref}).Encode()
	case "photo-small", "photo-large":
		payload, err = (&schemas.VisualBlock{Alt: kind, Original: ref}).Encode()
	case "video":
		payload, err = (&schemas.VideoBlock{Alt: "video", DurationMS: 9000, Original: ref}).Encode()
	case "file":
		payload, err = (&schemas.FileBlock{Filename: "doc.pdf", MediaType: "application/pdf", Size: uint64(size), Original: ref}).Encode()
	default:
		t.Fatalf("unknown kind %q", kind)
	}
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.spaceForTest(tid)
	rt.mu.Lock()
	_, err = rt.Self.Emit(sp, schemaFor(kind), payload, signal.AuthorshipHuman, uint64(time.Now().Unix()))
	rt.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func mediaTypeFor(kind string) string {
	switch kind {
	case "voice":
		return "audio/webm"
	case "video":
		return "video/mp4"
	case "file":
		return "application/pdf"
	}
	return "image/webp"
}

func schemaFor(kind string) string {
	switch kind {
	case "voice":
		return schemas.BlockVoice
	case "video":
		return schemas.BlockVideo
	case "file":
		return schemas.BlockFile
	}
	return schemas.BlockVisual
}

// matrixStand is the production topology, pass-joined.
type matrixStand struct {
	friend, laptop, phone *Runtime
	addrA, addrB          string
	tid                   id.TerminalID
}

func newMatrixStand(t *testing.T) *matrixStand {
	t.Helper()
	srvA, addrA := startRelay(t)
	t.Cleanup(srvA.Close)
	srvB, addrB := startRelay(t)
	t.Cleanup(srvB.Close)
	now := uint64(time.Now().Unix())

	friend := openRuntime(t, t.TempDir(), "friend")
	t.Cleanup(func() { friend.Close() })
	setPersonalRelay(t, friend, addrA)
	tid, err := friend.CreateSpace("семья")
	if err != nil {
		t.Fatal(err)
	}

	laptop := openRuntime(t, t.TempDir(), "gleb")
	t.Cleanup(func() { laptop.Close() })
	setPersonalRelay(t, laptop, addrB)
	// THE PASS FLOW, deliberately: it is how production relationships
	// formed, and it is what records routes on both sides
	// (JoinRequest.ReturnRoutes at the host, Accepted.Routes at the guest).
	pass, err := friend.MintPass(tid, 1, 24, addrA)
	if err != nil {
		t.Fatal(err)
	}
	req, err := laptop.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, laptop, req, JoinReady)
	if _, err := laptop.Say(tid, "мы тут", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	laptop.relaySyncOnce(addrB)
	if _, err := friend.PullFromRelay(addrA); err != nil {
		t.Fatal(err)
	}

	phone := pairChild(t, laptop, now)
	t.Cleanup(func() { phone.Close() })
	setPersonalRelay(t, phone, addrB)
	// The laptop's next cycle publishes the phone's certificate; the
	// friend pulls so the phone is at least KNOWN before the matrix runs.
	laptop.relaySyncOnce(addrB)
	if _, err := friend.PullFromRelay(addrA); err != nil {
		t.Fatal(err)
	}
	return &matrixStand{friend: friend, laptop: laptop, phone: phone,
		addrA: addrA, addrB: addrB, tid: tid}
}

// syncRound advances everyone once, gently (the shipped relay budget is
// four collects a second per connection).
func (m *matrixStand) syncRound() {
	m.friend.relaySyncOnce(m.addrA)
	m.laptop.relaySyncOnce(m.addrB)
	m.phone.relaySyncOnce(m.addrB)
	time.Sleep(700 * time.Millisecond)
}

// stageTrace is one kind's row: which pipeline stage it reached on the
// receiving phone, and what the holder saw of its want.
type stageTrace struct {
	kind          string
	frameArrived  bool
	wantEmitted   bool
	holderSawWant string // what wantsProbe reported at the friend, if anything
	state         string
	reason        string
}

func (m *matrixStand) traceFetch(t *testing.T, kind string, ref *schemas.AssetRef, rounds int) stageTrace {
	t.Helper()
	tr := stageTrace{kind: kind, holderSawWant: "—"}

	// Frame arrival: the block event reaches the phone via ordinary sync.
	deadline := time.Now().Add(45 * time.Second)
	for {
		m.syncRound()
		if _, err := m.phone.AssetStatus(m.tid, ref.PublicIDHex()); err == nil {
			tr.frameArrived = true
			break
		}
		if time.Now().After(deadline) {
			return tr
		}
	}

	if err := m.phone.RequestAsset(m.tid, ref.PublicIDHex()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < rounds; i++ {
		m.syncRound()
		m.phone.mu.Lock()
		tr.wantEmitted = len(m.phone.relayWants[m.tid]) > 0
		m.phone.mu.Unlock()
		st, err := m.phone.AssetStatus(m.tid, ref.PublicIDHex())
		if err == nil && st.State == assets.StateComplete {
			tr.state = string(st.State)
			return tr
		}
	}
	st, _ := m.phone.AssetStatus(m.tid, ref.PublicIDHex())
	tr.state, tr.reason = string(st.State), string(st.Reason)
	return tr
}

// TestMediaMatrixAcrossTwoRelays — the product invariant, red until the
// fix: EVERY kind the composer offers crosses two relays to a paired
// phone while everyone is online. Sizes deliberately straddle the
// 512KiB inline-refs/external-manifest boundary and the multi-chunk
// paths.
func TestMediaMatrixAcrossTwoRelays(t *testing.T) {
	m := newMatrixStand(t)

	// The probe distinguishes WHO saw the phone's want. The first run of
	// this table did not, and the answer→B entries it showed were the
	// LAPTOP answering into its own emptiness (it holds no bytes) — while
	// the actual holder, the friend, never saw the want at all. Which
	// node observes the want is the load-bearing column.
	seen := map[string]string{}
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	wantsProbe = func(holder *Runtime, dev id.DeviceID, outcome string) {
		<-mu
		if dev == m.phone.Device.ID {
			who := "?"
			switch holder {
			case m.friend:
				who = "friend"
			case m.laptop:
				who = "laptop"
			}
			if prev, ok := seen[who]; !ok || prev != outcome {
				seen[who] = outcome
			}
		}
		mu <- struct{}{}
	}
	defer func() { wantsProbe = nil }()

	kinds := []struct {
		kind string
		size int
	}{
		{"voice", 100 << 10},       // ≤512KiB: inline chunk refs, no manifest
		{"photo-small", 300 << 10}, // ≤512KiB: inline refs
		{"photo-large", 2 << 20},   // >512KiB: external manifest
		{"video", 6 << 20},         // large, many chunks
		{"file", 5 << 20},
	}

	var rows []stageTrace
	failed := false
	for _, k := range kinds {
		ref := emitKind(t, m.friend, m.tid, k.kind, k.size)
		m.friend.relaySyncOnce(m.addrA)
		tr := m.traceFetch(t, k.kind, ref, 12)
		<-mu
		f, fok := seen["friend"]
		l, lok := seen["laptop"]
		if !fok {
			f = "NEVER"
		}
		if !lok {
			l = "never"
		}
		tr.holderSawWant = "friend:" + f + " laptop:" + l
		for k := range seen {
			delete(seen, k)
		}
		mu <- struct{}{}
		rows = append(rows, tr)
		if tr.state != string(assets.StateComplete) {
			failed = true
		}
	}

	// THE TABLE — this output is the deliverable of Phase 0.
	t.Log("kind          frame  want  who-saw-the-want                              state/reason")
	for _, r := range rows {
		t.Logf("%-13s %-6v %-5v %-45s %s/%s", r.kind, r.frameArrived, r.wantEmitted, r.holderSawWant, r.state, r.reason)
	}
	if failed {
		t.Fatal("not every media kind crossed two relays — the table above names the stage each one died at")
	}
}

// TestNoSiblingCacheDependency — the owner's negative invariant, and the
// successor of Phase-0's TestSiblingCacheDiagnosis (whose G1 verdict —
// P1, the sibling cache explained production's "healthy voice" — is
// preserved in the header above). After the fix, the receiver must NEVER
// depend on what its sibling happened to open: the friend sends a photo
// and a video, the laptop does NOTHING, and the phone fetches the photo
// straight from the friend; mirrored, the laptop fetches the video while
// the phone does nothing. A hidden dependency on "did my other device
// open it first" is the one failure a person can never diagnose.
func TestNoSiblingCacheDependency(t *testing.T) {
	m := newMatrixStand(t)

	photo := emitKind(t, m.friend, m.tid, "photo-small", 300<<10)
	video := emitKind(t, m.friend, m.tid, "video", 2<<20)
	m.friend.relaySyncOnce(m.addrA)

	// The phone learns of both; the LAPTOP IS NOT TOUCHED beyond its
	// ordinary sync ticks inside syncRound (it relays frames — that is
	// its job — but fetches nothing).
	waitUntil(t, 45*time.Second, "the phone never heard the blocks", func() bool {
		m.syncRound()
		_, e1 := m.phone.AssetStatus(m.tid, photo.PublicIDHex())
		_, e2 := m.phone.AssetStatus(m.tid, video.PublicIDHex())
		return e1 == nil && e2 == nil
	})

	if err := m.phone.RequestAsset(m.tid, photo.PublicIDHex()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 60*time.Second, "the phone could not fetch a photo the laptop never opened", func() bool {
		m.syncRound()
		st, err := m.phone.AssetStatus(m.tid, photo.PublicIDHex())
		return err == nil && st.State == assets.StateComplete
	})

	// Mirrored: the laptop fetches the video; the phone has not touched it.
	if err := m.laptop.RequestAsset(m.tid, video.PublicIDHex()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 60*time.Second, "the laptop could not fetch a video the phone never opened", func() bool {
		m.syncRound()
		st, err := m.laptop.AssetStatus(m.tid, video.PublicIDHex())
		return err == nil && st.State == assets.StateComplete
	})
}

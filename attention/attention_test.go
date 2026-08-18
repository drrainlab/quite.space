package attention

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

func p(b byte) id.PrincipalID { var x id.PrincipalID; x[0] = b; return x }
func ev(b byte) id.EventID    { var x id.EventID; x[0] = b; return x }
func sp(b byte) id.TerminalID { var x id.TerminalID; x[0] = b; return x }

const now = int64(1_700_000_000)

func viewer() Viewer {
	me := p(1)
	mine := ev(200)
	return Viewer{
		Principal:    me,
		Aliases:      []string{"Алиса", "alice"},
		AuthoredByMe: func(e id.EventID) bool { return e == mine },
	}
}

func cand(text string) Candidate {
	return Candidate{
		EventID: ev(10), SpaceID: sp(1), Author: p(2), Kind: "text",
		Text: text, CreatedAt: uint64(now), ReceivedAt: now,
	}
}

// RU and EN questions and requests are both detected, and so is a message
// that switches languages mid-sentence.
func TestRulesRussianEnglishAndCodeSwitching(t *testing.T) {
	cases := []struct {
		name, text string
		want       string
	}{
		{"ru question", "посмотришь конфиг реле?", ReasonQuestion},
		{"ru question no mark", "кто сможет проверить реле", ReasonQuestion},
		{"en question", "can you confirm the mesh test?", ReasonQuestion},
		{"ru action", "нужно проверить реле до вечера", ReasonAction},
		{"en action", "please review the fallback, deadline friday", ReasonAction},
		{"code-switching", "Реле готово, but we still need someone to test the mesh fallback", ReasonAction},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reasons, _ := Detect(cand(tc.text), viewer())
			if !hasReason(reasons, tc.want) {
				t.Fatalf("%q → %+v, want %s", tc.text, reasons, tc.want)
			}
		})
	}
}

// Word boundaries matter: "надо" must not fire inside "надоел".
func TestLexiconRespectsWordBoundaries(t *testing.T) {
	reasons, _ := Detect(cand("этот дождь надоел"), viewer())
	if hasReason(reasons, ReasonAction) {
		t.Fatal("action fired on a substring inside another word")
	}
}

// Aliases are explicit and inflection-tolerant at the prefix; unrelated
// words that merely start alike must not count as being addressed.
func TestNameInTextUsesExplicitAliases(t *testing.T) {
	reasons, _ := Detect(cand("Алисау стоит посмотреть"), viewer())
	if !hasReason(reasons, ReasonNameInText) {
		t.Fatal("inflected alias not matched")
	}
	v := viewer()
	v.Aliases = []string{"Ян"}
	reasons, _ = Detect(cand("сегодня январь"), v)
	if hasReason(reasons, ReasonNameInText) {
		t.Fatal("alias matched a different word")
	}
}

// A signed mention and a reply to one of my events are HARD facts.
func TestHardSignals(t *testing.T) {
	c := cand("см. выше")
	c.Mentions = []id.PrincipalID{p(1)}
	if _, hard := Detect(c, viewer()); !hard {
		t.Fatal("signed mention is not hard")
	}
	c2 := cand("да, согласен")
	mine := ev(200)
	c2.ReplyTo = &mine
	if _, hard := Detect(c2, viewer()); !hard {
		t.Fatal("reply to my event is not hard")
	}
	c3 := cand("да, согласен")
	other := ev(201)
	c3.ReplyTo = &other
	if _, hard := Detect(c3, viewer()); hard {
		t.Fatal("reply to someone else's event counted as hard")
	}
}

// The core invariant: a hard signal survives any amount of negative
// feedback, while a soft one can be demoted out of priority.
func TestHardSurvivesFeedbackSoftDoesNot(t *testing.T) {
	e := NewEngine(DefaultPolicy())
	ctx := Context{Viewer: viewer()}

	// Teach the model to dislike this kind of text, hard.
	for i := range 30 {
		c := cand("расписание рутинных отчётов")
		c.EventID = ev(byte(100 + i))
		e.Feedback(c, ctx, false)
	}

	soft := cand("посмотришь расписание рутинных отчётов?")
	soft.EventID = ev(50)
	sig, ok := e.Judge(soft, ctx, now)
	if ok && sig.Delivery == DeliveryPriority {
		t.Fatal("soft candidate stayed priority after sustained negative feedback")
	}

	hard := cand("посмотришь расписание рутинных отчётов?")
	hard.EventID = ev(51)
	hard.Mentions = []id.PrincipalID{p(1)}
	hsig, ok := e.Judge(hard, ctx, now+300)
	if !ok {
		t.Fatal("hard signal suppressed entirely — the model overrode a fact")
	}
	if hsig.Delivery == DeliverySuppressed {
		t.Fatalf("hard signal suppressed: %+v", hsig)
	}
	if !hsig.Hard {
		t.Fatal("hard flag lost")
	}
}

// "You should have caught this" is a POSITIVE example: without it the model
// would only ever learn to go quiet.
func TestNoticeFeedbackRaisesSimilarMessages(t *testing.T) {
	e := NewEngine(DefaultPolicy())
	ctx := Context{Viewer: viewer()}
	before := e.Model.Score(Extract(cand("что там по LoRa-шлюзу?"), false, 0))
	for i := range 12 {
		c := cand("что там по LoRa-шлюзу?")
		c.EventID = ev(byte(60 + i))
		e.Feedback(c, ctx, true)
	}
	after := e.Model.Score(Extract(cand("что там по LoRa-шлюзу?"), false, 0))
	if after <= before {
		t.Fatalf("positive feedback did not raise the score: %.3f → %.3f", before, after)
	}
}

// Muting a space is a POLICY act, not a statement that its content is
// worthless — it must not move the ranking weights.
func TestMutingASpaceDoesNotPoisonTheModel(t *testing.T) {
	e := NewEngine(DefaultPolicy())
	probe := Extract(cand("вопрос про реле?"), false, 0)
	before := e.Model.Score(probe)

	e.Policy.Spaces = map[string]Scope{sp(1).Hex(): ScopeOff}
	if _, ok := e.Judge(cand("вопрос про реле?"), Context{Viewer: viewer()}, now); ok {
		t.Fatal("muted space still produced a signal")
	}
	if after := e.Model.Score(probe); after != before {
		t.Fatalf("muting changed the model: %.4f → %.4f", before, after)
	}
}

// A late mesh event carrying an ancient clock must still be judged: the
// seen-set, not a clock cursor, decides what is new to us.
func TestLateEventWithAncientClockIsStillJudged(t *testing.T) {
	e := NewEngine(DefaultPolicy())
	ctx := Context{Viewer: viewer()}

	recent := cand("свежий вопрос?")
	recent.EventID = ev(70)
	recent.Mentions = []id.PrincipalID{p(1)}
	if _, ok := e.Judge(recent, ctx, now); !ok {
		t.Fatal("recent event not judged")
	}

	// Arrives now, but claims to have been written years ago.
	late := cand("старый вопрос, дошедший по радио?")
	late.EventID = ev(71)
	late.Mentions = []id.PrincipalID{p(1)}
	late.CreatedAt = 1_000_000
	late.ReceivedAt = now + 60
	sig, ok := e.Judge(late, ctx, now+60)
	if !ok {
		t.Fatal("late event with an old clock was skipped")
	}
	if sig.ReceivedAt != now+60 {
		t.Fatalf("received_at should be the local fact, got %d", sig.ReceivedAt)
	}
	// Newest-first ordering follows LOCAL arrival, not the author's clock.
	if list := e.Inbox.List(); list[0].EventHex != late.EventID.Hex() {
		t.Fatal("inbox ordered by the author's clock instead of arrival")
	}
}

// ReceivedAt is minted once and never revised, so rescans cannot reshuffle
// the inbox or slip an old event past quiet hours.
func TestReceivedAtIsStable(t *testing.T) {
	in := NewInbox()
	first, fresh := in.FirstSeen(ev(80), now)
	if !fresh || first != now {
		t.Fatal("first sighting not recorded")
	}
	again, fresh := in.FirstSeen(ev(80), now+9999)
	if fresh || again != now {
		t.Fatalf("received_at drifted on rescan: %d", again)
	}
	// Survives a persistence round-trip.
	in2 := NewInbox()
	in2.LoadSeen(in.SeenSnapshot())
	back, _ := in2.FirstSeen(ev(80), now+12345)
	if back != now {
		t.Fatalf("received_at lost across restart: %d", back)
	}
}

// Over-budget signals are demoted to digest — never dropped.
func TestBudgetDemotesRatherThanDrops(t *testing.T) {
	pol := DefaultPolicy()
	pol.Budget = Budget{MaxPerDay: 1, MinGapSecs: 0}
	e := NewEngine(pol)
	ctx := Context{Viewer: viewer()}

	var priority, digest int
	for i := range 5 {
		c := cand("срочный вопрос?")
		c.EventID = ev(byte(90 + i))
		c.Mentions = []id.PrincipalID{p(1)}
		sig, ok := e.Judge(c, ctx, now+int64(i))
		if !ok {
			t.Fatalf("hard signal %d dropped by the budget", i)
		}
		switch sig.Delivery {
		case DeliveryPriority:
			priority++
		case DeliveryDigest:
			digest++
		}
	}
	if priority != 1 {
		t.Fatalf("budget of 1 allowed %d priority signals", priority)
	}
	if digest != 4 {
		t.Fatalf("expected 4 demoted-to-digest, got %d", digest)
	}
}

// Quiet hours are judged on the LOCAL arrival time.
func TestQuietHoursUseLocalArrival(t *testing.T) {
	b := Budget{MaxPerDay: 100, QuietEnabled: true, QuietFromHr: 22, QuietToHr: 8}
	st := &budgetState{}
	night := time.Date(2026, 7, 25, 23, 30, 0, 0, time.UTC).Unix()
	if b.admit(st, night, time.UTC) {
		t.Fatal("priority allowed during quiet hours")
	}
	day := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC).Unix()
	if !b.admit(st, day, time.UTC) {
		t.Fatal("priority refused during the day")
	}
}

// Off means off: nothing is scanned and nothing is stored.
func TestOffProducesNothing(t *testing.T) {
	pol := DefaultPolicy()
	pol.Mode = ModeOff
	e := NewEngine(pol)
	c := cand("прямой вопрос?")
	c.Mentions = []id.PrincipalID{p(1)}
	if _, ok := e.Judge(c, Context{Viewer: viewer()}, now); ok {
		t.Fatal("Off still produced a signal")
	}
	if len(e.Inbox.Signals) != 0 {
		t.Fatal("Off still stored something")
	}
}

// direct_only carries hard facts and nothing else.
func TestDirectOnlyScope(t *testing.T) {
	pol := DefaultPolicy()
	pol.Spaces = map[string]Scope{sp(1).Hex(): ScopeDirectOnly}
	e := NewEngine(pol)
	ctx := Context{Viewer: viewer()}

	if _, ok := e.Judge(cand("а кто чинит реле?"), ctx, now); ok {
		t.Fatal("soft candidate surfaced under direct_only")
	}
	c := cand("посмотри пожалуйста")
	c.EventID = ev(120)
	c.Mentions = []id.PrincipalID{p(1)}
	if _, ok := e.Judge(c, ctx, now); !ok {
		t.Fatal("hard signal missing under direct_only")
	}
}

// Your own message is never a signal to you.
func TestOwnMessagesAreNeverSignals(t *testing.T) {
	e := NewEngine(DefaultPolicy())
	c := cand("напоминание себе: проверить реле?")
	c.Author = p(1) // me
	if _, ok := e.Judge(c, Context{Viewer: viewer()}, now); ok {
		t.Fatal("my own message became a signal")
	}
}

// The same event is judged once, however often it is rediscovered.
func TestNeverJudgedTwice(t *testing.T) {
	e := NewEngine(DefaultPolicy())
	ctx := Context{Viewer: viewer()}
	c := cand("вопрос?")
	c.Mentions = []id.PrincipalID{p(1)}
	if _, ok := e.Judge(c, ctx, now); !ok {
		t.Fatal("first judgement missing")
	}
	if _, ok := e.Judge(c, ctx, now+1); ok {
		t.Fatal("same event judged twice")
	}
}

// Explanations from hashed features are marked approximate: collisions make
// an exact "this term did it" claim dishonest.
func TestLearnedExplanationsAreMarkedApproximate(t *testing.T) {
	e := NewEngine(DefaultPolicy())
	ctx := Context{Viewer: viewer()}
	for i := range 10 {
		c := cand("сможешь глянуть?")
		c.EventID = ev(byte(130 + i))
		e.Feedback(c, ctx, true)
	}
	feats := Extract(cand("сможешь глянуть?"), false, 0)
	for _, r := range e.Model.Explain(feats, e.Model.Score(feats)) {
		if r.Code == ReasonLearned && r.Exact {
			t.Fatal("a hashed-feature explanation claimed to be exact")
		}
	}
	// Rule reasons, by contrast, are exact facts.
	reasons, _ := Detect(cand("сможешь глянуть?"), viewer())
	for _, r := range reasons {
		if !r.Exact {
			t.Fatalf("rule reason %s marked approximate", r.Code)
		}
	}
}

// The ring is bounded by age as well as by count.
func TestInboxBoundedByCountAndAge(t *testing.T) {
	in := NewInbox()
	for i := range MaxSignals + 50 {
		in.Add(Signal{ID: string(rune(i)), ReceivedAt: now}, now)
	}
	if len(in.Signals) != MaxSignals {
		t.Fatalf("count bound not enforced: %d", len(in.Signals))
	}
	old := now - int64((MaxSignalAge+time.Hour)/time.Second)
	in2 := NewInbox()
	in2.Add(Signal{ID: "ancient", ReceivedAt: old}, now)
	in2.Add(Signal{ID: "fresh", ReceivedAt: now}, now)
	if len(in2.Signals) != 1 || in2.Signals[0].ID != "fresh" {
		t.Fatalf("age bound not enforced: %+v", in2.Signals)
	}
}

// Watched phrases power anchors before the encoder exists.
func TestWatchedPhrasesMatch(t *testing.T) {
	got := DetectWatched("обсуждаем LoRa-шлюз и антенны", []string{"lora", "антенн"})
	if len(got) != 2 {
		t.Fatalf("watched phrases missed: %+v", got)
	}
	for _, r := range got {
		if r.Code != ReasonWatched || !r.Exact {
			t.Fatalf("bad watched reason: %+v", r)
		}
	}
}

func hasReason(rs []Reason, code string) bool {
	for _, r := range rs {
		if r.Code == code {
			return true
		}
	}
	return false
}

// Inflected forms of "need" all ask the same thing. A live run caught this:
// listing only "нужно" as an exact word silently missed "нужен твой взгляд".
func TestActionLexiconCoversInflectedNeed(t *testing.T) {
	for _, text := range []string{
		"нужен твой взгляд на антенну",
		"нужна помощь с реле",
		"нужно проверить фидер",
		"нужны свежие данные",
		"требуется второй тестер",
	} {
		reasons, _ := Detect(cand(text), viewer())
		if !hasReason(reasons, ReasonAction) {
			t.Errorf("%q did not read as a request: %+v", text, reasons)
		}
	}
	// And it still does not fire on unrelated words.
	for _, text := range []string{"нуга к чаю", "требуха"} {
		reasons, _ := Detect(cand(text), viewer())
		if hasReason(reasons, ReasonAction) {
			t.Errorf("%q wrongly read as a request", text)
		}
	}
}

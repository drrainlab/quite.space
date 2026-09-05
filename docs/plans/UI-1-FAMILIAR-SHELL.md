# UI-1 — the familiar shell

**Why.** Every field complaint this week was about legibility, not
features: "read or not?", "the history is unclear", "I don't see the
space". People arrive from ordinary messengers; each unfamiliar pixel is
a tax on entry. The shell lives in web-ui — embedded in desktop AND
Android — so one wave, zero protocol risk.

**What we take from the mockup (layout):**
1. Inbox rows: icon · title · last moment (author: text) · time · fresh mark.
2. Space header: icon · title · "N people" · tabs incl. **Files** and **Links**
   (derived views of the same log — no new events).
3. Rich things as cards inside chat (link cards already; more later).
4. A curated built-in icon set for spaces (index rides the character
   manifest like relic/presence_glyphs; unknown → generated glyph).

**What we refuse (semantics the mockup smuggled in):** "5 drawing now",
"Live", automatic "online", call buttons. Presence stays a person's own
declared state; no typing lights; no calls.

**Deferred:** people avatars (a participant-manifest field — protocol
surface; a follow-up wave), Sky (shared drawing) on top of this shell.

**Plan.**
- A. API: `spaceResp.last` = the last human-visible moment (kind, text
  summary, author name, mine, at) — computed from State.Entries().
- B. Navigator rows: preview line + time; archetype only when empty.
- C. Header: icon + "N people"; archetype in the tooltip.
- D. Views `files`, `links` (library.js): filtered from fetchEntries.
- E. icons.js: ~16 line icons; Character.Icon (kv "icon", allowlisted
  on the Go side); create dialog picker with per-archetype default.
- F. Tests: character kv roundtrip + refusal; /api/spaces last; web-ui
  suite. Live pair check + screenshot.

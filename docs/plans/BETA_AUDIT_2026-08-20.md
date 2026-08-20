# Beta audit — 2026-08-20

Where the product actually stands after the first week of real people
using it, and the work that falls out of that. Everything here is
**measured** — a stand, an instrument, or a reproduced report — not
supposed. Written to be handed to parallel work sessions as their
starting brief; each stream names its branch, its red test, and its
first task.

## The headline

**Delivery between two relays silently loses media, and two official
relays is what production runs.** This is not an edge case: any two
people whose nodes auto-selected different relays hit it on their first
photo. It is the single biggest threat to beta trust — the app looks
broken while every diagnostic reads healthy — and it is Stream 1.

## What is shipped and solid

- **v0.1.4 is out** and the Android unlock crash is fixed at the root
  (missing USE_BIOMETRIC + a fence so no biometric question can ever
  take an unlock down again). Verified on the emulator over the broken
  state; confirmed working by the reporter.
- **CI is green on main for the first time since v0.1.0**, both jobs,
  and `release.yml` now refuses to publish unless ci succeeded on the
  tagged SHA. Three releases shipped on red before that; structurally
  impossible now.
- **Release notes live in the repository** (`docs/releases/<v>.md`),
  publish with the standing install section appended, and the release
  takes its title from the note's first line.
- The node test suite runs in ~6 min plain / ~16 min race (was: killed
  at the 10-minute default), 601/601, zero data races. The cadence and
  KDF seams that bought this are documented in
  `node/relayreg_testmain_test.go` and `node/cadence.go`.
- Production fixes shipped along the way: a stopping relay hangs up on
  its clients (relayserver), and a gateway's own device is no longer a
  delivery recipient.

## Stream 1 — route honesty (branch `stream-1-media-routing`, red test waiting)

**Symptoms reported:** media hangs at "fetching…" with both sides
online while text and voice cross fine; a space joined by quick link on
one of the person's devices never appears on the other; all of it works
on a LAN.

**Root, convicted by instruments** (commit 6b46d4d on the branch):

    push #1:  laptop -> RELAY-A (the sender's own)   <- copy landed wrong
    later:    laptop -> RELAY-B, phone -> RELAY-B    <- routes learned,
                                                        nothing left to push

At push time the holder has not yet learned the recipient's route, so
`deliverSpace`'s legacy bootstrap fires — "nothing known → my own
relay, recorded" — the item lands on the sender's relay, `lastLen`
advances, and the ledger says delivered. A tick later the real route
arrives and the RouteBook reads healthy in every diagnostic, but the
re-offer machinery only re-offers what `lastLen` has not covered. Every
other undelivered branch of `deliverSpaceRouted` holds `lastLen` back
and says so out loud (`heldNoRoute`, `heldNoRecipient`); the legacy
bootstrap is the one path that claims success while delivering to
itself. The same wrong turn breaks want→answer in both directions, and
`answerWantsRouted` compounds it by returning **silently** when it has
no route for the wanter. A LAN masks everything: T6 pushes pending
media straight down the live link and never consults the RouteBook.

**Work, in order:**

1. Make the legacy bootstrap honest. Either HOLD like every other
   routeless branch, or record the assumption and **re-offer when a
   real route displaces it** (`routeRank` already knows legacy is the
   weakest word). Gate: `TestAPairedPhoneFetchesAcrossTwoRelays` goes
   green; `TestAPairedPhoneFetchesAFriendsPhotoOverTheRelay` (single
   relay, green today) stays green; full `./node/ -race` stays green.
2. The same honesty for wants and answers: a want that could not reach
   the holder and an answer that could not reach the wanter are held
   states, not silence. `answerWantsRouted`'s bare `return` becomes a
   recorded refusal the UI can speak about.
3. **Post-pairing space propagation** — the missing room of the MD
   wave: freight carries spaces only at pairing time; a space joined
   afterwards (pass or quick link, either direction) reaches the
   person's other devices by no mechanism at all. Confirmed by reading:
   nothing after `node/pairing.go`'s freight touches another own
   device's space set. This needs a small design note first (likely an
   ADR amendment to ADR-020/MD): the candidate shape is "my own devices
   are standing recipients of my joins", riding the same signed-cert
   trust that already names them.
4. Design frame for all three: the pre-T4/T5 sections of ADR-020 — this
   is their unbuilt room. Do not patch around them separately.

**Also known, adjacent, cheaper:** the UI's "did not answer" is the
node's honest timeout, but the node often *knows* more (no route for
the wanter — see 2). Once refusals are recorded, surface them.

## Stream 2 — composer and replies (UI session)

- **Reply + pasted image loses the reply.** Root found:
  `reply_to` exists only on `message.text.v1` (`api_blocks.go:455`) —
  blocks structurally cannot be replies — and the upload path neither
  sends nor clears `replyTarget` (`app.js`: only `say()` reads it).
  Decision to make in-session: caption travels as a reply text with the
  image as its sibling, or the reply bar is honestly cleared with a
  word. Either way the current silent divergence goes.
- **Grouped media** (send several as one) — requested by testers; same
  corner of the code.
- Already committed locally, rides with this stream: the reply-quote
  layout fix (`4a617c1` — quote pushed its bubble off a phone's screen;
  quote now names the thing, not its fetch-state weather). **This
  commit sits unpushed on local main** — push it with this stream's
  work.

## Stream 3 — reading polish (short UI session, after or with Stream 2)

- **Esc closes an open post.** The global Escape router exists
  (`app.js:2389`); add the branch.
- **The magnifier cursor over post images** is our own
  `cursor: zoom-in` (`styles.css:1664`); the image is click-to-open.
  Decide: keep the affordance, change the cursor, or both. One line.

Streams 2 and 3 share files (`app.js`, `styles.css`) — run them as one
session or strictly in sequence, never parallel to each other. Both are
safe alongside Stream 1 (different tree).

## Platform track (not scheduled, decisions recorded)

- **Android first run** still asks for a passphrase; PIN+name is
  desktop-only in 0.1.4 (the note says so honestly). Agreed direction:
  don't restyle the native screen — serve the existing web
  `lockscreen.html` through a lockgate inside the Android host, which
  brings PIN+name, the design, and future fixes to every platform at
  once. First step when picked up: stand lockgate up in the host and
  see the screen live.
- **iOS** — paused by owner. Research done: core and web UI port as-is
  via `gomobile -target=ios`; the Kotlin host (~6300 lines) is the
  rewrite; background model is the real design question; USB radio does
  not exist there. First experiment when resumed: bind the existing
  `android/quietcore` for iOS and list what breaks (needs no license).
- **macOS hardware-backed passcode** — blocked on a Developer ID
  decision, measured and recorded in `kernel/passcode/passcode.go`.
  The same signature would remove the Gatekeeper warning for every
  tester.

## Hygiene (cheap, background)

- `TestAPinToAnUnknownSpaceIsKept`: one failure in ~2400 runs, message
  never captured. Add `-v` to CI's test step so the next such flake
  names itself; hunt only if it recurs.
- Site says "macOS / universal" — a person on Apple Silicon cannot tell
  it is for them. One label, other repo (`quite.space` site).
- Owner's standing items: rotate the leaked keystore password; PTR
  record for 195.63.160.237; Developer ID / Play licenses when ready.

## Suggested order

1. **Stream 1** — now, in its own session, on its branch. It is the
   beta's trust. Release as **v0.1.5 on its own** the moment the gate
   is green; don't batch it with UI work.
2. Streams 2+3 as one UI session in parallel; they ship whenever ready
   (no release pressure — 4a617c1 already waits there).
3. Android lockgate screen next release after that.

## The lesson this week keeps teaching

Every real bug of the week — the relay that never hung up, the gateway
mailing itself, the legacy bootstrap delivering to itself,
`answerWantsRouted`'s bare return — is the same shape: **a path that
stays silent or reports success where every neighbouring path speaks.**
When in doubt in any stream: make the code say what happened; the UI
already knows how to repeat it honestly. And prove causes with
instruments, not by reading — seven plausible theories died to probes
this week, and zero survived them.

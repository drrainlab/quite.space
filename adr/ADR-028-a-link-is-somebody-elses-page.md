# ADR-028 — A link is somebody else's page: who fetches, and who watches

Status: accepted (2026-08-24)
Companion doctrine: ADR-013 (the source is readable; no executable payload),
ADR-009 (append-only schemas), SHARE-1 (a quotation is a claim, not proof)

## The invariant

> **Reading a message must never make a request to a third party.**
> A preview is made by the person who SENDS it, once, and travels as
> ordinary content. The only moment this feature reaches a stranger's
> server is a moment somebody deliberately asked for.

## Why this note exists

The product had the data type and none of the verb. `block.link.v1` has
carried `url / title / description / thumb` since the media wave, with the
rule already written above it — "the preview is attached by the SENDER —
no central unfurl service exists" — and nothing ever attached one. Links
rendered as bare addresses, and a message that was only a YouTube link was
a message with nothing in it.

Every ordinary way to fix that is wrong here, and each is wrong in its own
way:

- **A central unfurl service** is what Telegram does. There is no server in
  this system that could be one, and building one would mean a machine that
  learns every link anybody sends.
- **The reader's client fetches** is worse and looks harmless. An `<img>`
  pointing at a stranger's host is a beacon: opening a room reports this
  device, its address and the minute it read, to whoever wrote the link.
  Send somebody a link to a server you own and you have a read receipt they
  never agreed to, plus their IP. markdown.js already refuses `![](url)`
  for exactly this reason (ADR-013 §2); a link card that fetched on receipt
  would reintroduce it through a side door.
- **Fetching on arrival, in the background** is the same beacon with worse
  timing — it reports that the message reached this device at all.

## Decision

### 1. The sender's node fetches, once, at compose time

`POST /api/unfurl` is a verb the person invokes by pasting an address into
the box. What comes back is title, description, site and a thumbnail; what
travels is a signed `block.link.v1` in the log. Readers render it from
bytes that are already theirs.

The card is shown BEFORE the send, with a control that drops it. What a
stranger's page says about itself is worth looking at before it becomes
part of your signed message.

Nothing is ever unfurled for a message that ARRIVED. A URL somebody sent
before this existed stays a plain address forever.

### 2. What a card says has the standing of a quotation

Every field is the remote site's claim about itself, taken by one device at
one moment. It is drawn quieter than the message around it, and it is
clipped, entity-resolved and stripped of control characters before it can
be rendered — a card is a stranger's text, shown to somebody who has not
agreed to hear from that stranger.

The thumbnail is DECODED AND RE-ENCODED by the sender's node, never
forwarded. What travels is pixels this node produced, at a size it chose
(≤16 KiB), so nothing rides along inside a file somebody else wrote. Only
the formats the standard library decodes: a card without a picture is a
perfectly good card, and an image dependency is not worth one.

### 3. The fetch is refused at the socket, not at the URL

The address is arbitrary text, often typed by somebody else and pasted.
That makes the fetch a request-forgery surface, and unlike node/llm's
provider URL — which a person configured, and which legitimately points at
localhost — there is no honest reason for this one to reach inward.

The check runs as the dialer's `Control` hook, on the address the socket is
about to connect to. That is why it is a check and not a gesture: a name
that resolves inward, and a redirect that turns inward on the third hop,
both die at the same place, and there is no second resolution to disagree
with the first.

### 4. The play control is decided by the READER, from the address

Not by a flag in the event. A sender does not get to declare that their
link deserves a play button; the receiving client recognises the address
itself. This also means a plain text message that is a video address gets
the control — no picture, no fetch, just the ability to act on an address
already in hand.

### 5. Watching happens one frame further down

A video plays inside the conversation. The obvious way to do that is to
drop an iframe into the page, and the obvious way is wrong here: this
origin holds the session token and can drive every route on the node,
which is why its policy says `script-src 'self'` and `connect-src 'self'`
and why the comment above it calls script execution "the whole game". A
third party's player loaded into that document is a permission granted
forever so that one card can play.

So the frame is **nested**, and the nesting is the whole decision:

    the conversation frames  →  GET /player  (ours, same origin)
    /player frames           →  the embed    (youtube-nocookie.com)

The interface's `frame-src` is therefore `'self'` and must stay `'self'`:
it may frame a page of this node's that an injection could already have
opened, and nothing else. `/player` is where a third party is allowed to
exist — a document with no token, no application script, `script-src
'none'`, and a policy naming exactly one frame host and one image host.
It is unauthenticated on purpose: it holds nothing, and requiring the
token would only mean putting the token in a document whose whole job is
to talk to somebody else.

One frame of distance, bought for one word in one directive. That is the
difference between "youtube.com may run beside your keys" and "youtube.com
may run inside a blank page we serve".

Three consequences worth writing down, because each looked like a bug first:

- **The player sends a referrer, and it is the only thing in this tree
  that does.** `no-referrer` produced YouTube error 153 — an embed is a
  contract between a page and a host, and a host shown nothing refuses.
  The policy is `origin`, so what travels is `http://127.0.0.1:PORT` — a
  loopback address, true of every copy of this app, saying nothing the
  request for a specific video did not already say.
- **A shell can have no origin to lend.** The desktop serves the whole
  interface over a custom scheme — `cmd/wails-probe` measures
  `origin=wails://localhost` — and a custom scheme's identity cannot
  travel in a Referer header at all. So the node opens the smallest
  listener that can exist, one route and no token, and `/api/status`
  tells the page where it is. The interface's `frame-src` names that
  loopback form beside `'self'`: still our page, still token-free, still
  one frame from the embed. Every other shell reaches this node over
  http already and uses the plain relative path.

  The same fact cuts once more, from the other side: the player's
  `frame-ancestors` cannot be `'self'` alone, because on the desktop the
  ANCESTOR is `wails://localhost` and the player is the loopback
  listener — different origins by definition, so `'self'` refused the
  very nesting the listener exists for. The policy is `'self' wails:`,
  and X-Frame-Options is deliberately absent: it cannot express that
  pair, and browsers honour the stricter of the two.
- **A subframe's navigation is not the top frame leaving.** The phone's
  WebView client redirects non-loopback addresses to the system, and it
  fires for every frame — so the embed's own load looked like an escape
  attempt and was answered by launching a browser. What may be framed is
  decided by the policy the node serves, which is where that rule
  belongs; the lock on the TOP frame is untouched.

A video whose owner forbids embedding says so inside the frame, in
YouTube's own words. The card's title is still a link to it.

### 6. Pressing play asks first, once

Everything else here is local. Play is the one moment the person
deliberately reaches out, so the card says what it costs — "watching loads
the video from YouTube; they will see this device" — and remembers the
answer. A question re-asked forever is a question nobody reads.

## Consequences

- Accepted: the sender reveals to the site that somebody fetched its page.
  They had the address already; the alternative was every reader revealing
  themselves instead.
- Accepted: `frame-src` moved from `'none'` to `'self'`. What that buys
  an injection is the ability to frame a page of ours; what it buys the
  person is a video that plays where they are reading.
- Accepted: a card is ~16 KiB and will not ride a radio frame. The delivery
  ladder already says so in the concrete ("this is 18 KB and the radio
  carries 2.5"), and a card without a thumbnail is a few hundred bytes.
- Accepted: a card can be a lie, because a page can lie about itself. It is
  drawn as a quotation for that reason, and the address under it is the
  part that cannot.
- Not decided here: players for anything but YouTube and SoundCloud.
  Each provider is a recognisor plus one named host in the player's
  policy — deliberately a short list, grown one measured need at a time.
  Previews need no such list: the general OpenGraph path already covers
  most of what people paste.

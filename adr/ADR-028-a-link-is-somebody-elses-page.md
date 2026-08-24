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

### 5. Watching happens in a window of its own

The interface's policy says `frame-src 'none'`, and the comment above it
says why: that origin holds the session token and can drive every route on
the node, so the policy's job is to make an injection worth as little as
possible. Embedding a player INTO that document is survivable — a
cross-origin frame cannot read its parent — but "survivable" is exactly
what a security policy exists not to rely on, and it would mean relaxing
the app's policy for everyone, forever, so that one card can play.

So `GET /player` serves a document with **no token, no application script,
and a policy of its own** that permits one thing: a frame from
youtube-nocookie.com. It is unauthenticated on purpose — it holds nothing
and says nothing about this node, and requiring the token would only mean
putting the token in a URL bound for a window whose whole job is to talk to
somebody else.

Three shells open it three ways, and none of them is a workaround:

| shell | what happens | why |
|---|---|---|
| browser | `window.open('/player')` | a window is a window |
| phone | the ordinary video address, `target=_blank` | `LocalOnlyClient` hands non-loopback hosts to the system, so YouTube's own app takes it; `/player` would replace the conversation inside the app's single WebView |
| desktop | `POST /api/player/open` | the macOS webview implements no delegate for new windows, so `window.open` and `target=_blank` are silently inert there — a fact about the shell, not about this card |

That last seam takes a **video id**, never a URL, and builds the address
itself at this node's own origin. The widest thing a caller can ask for is
"show the player window"; an open-url endpoint would have been a way to
make the host machine open an address of somebody else's choosing.

### 6. Pressing play asks first, once

Everything else here is local. Play is the one moment the person
deliberately reaches out, so the card says what it costs — "watching loads
the video from YouTube; they will see this device" — and remembers the
answer. A question re-asked forever is a question nobody reads.

## Consequences

- Accepted: the sender reveals to the site that somebody fetched its page.
  They had the address already; the alternative was every reader revealing
  themselves instead.
- Accepted: a card is ~16 KiB and will not ride a radio frame. The delivery
  ladder already says so in the concrete ("this is 18 KB and the radio
  carries 2.5"), and a card without a thumbnail is a few hundred bytes.
- Accepted: a card can be a lie, because a page can lie about itself. It is
  drawn as a quotation for that reason, and the address under it is the
  part that cannot.
- Not decided here: previews for anything but pages and YouTube. Other
  providers would each be a recognisor of their own, and the general
  OpenGraph path already covers most of what people paste.

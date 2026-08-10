# Attach a radio and talk over it

*По-русски: [../USING.md](../USING.md)*

The whole path, from "a board is on the desk" to "we are talking and
nobody has any internet".

The board must already be flashed: [FLASHING.md](FLASHING.md). The same
thing without the interface, in flags and curl: [CLI.md](CLI.md).

---

## Step 1. Attach the radio

Open **Settings → Radio**. That tab IS the radio screen; there is nowhere
further to go.

1. Plug the board into USB.
2. Press **Scan for radios**.
3. Find the row that says "an RNode modem" and press **attach**.

The scan takes around ten seconds: every serial port is opened, given a
window to say what it is, and the quiet ones are asked a second time
before being called quiet. You never look up or memorise a device path —
that is the whole point of scanning, because the path changes by itself
after a reset.

### On a phone it works differently underneath

Android creates no device node for a USB peripheral: there are no serial
ports there and there never will be. The board is reachable only through the
system's USB service, so the steps are the same and the machinery under them
is not — three differences you can see:

- **You need an OTG cable.** The phone has to become the host, or the board
  does not exist as far as it is concerned.
- **The system asks permission** for that specific device, once. It is its
  dialog, not ours; a refusal is an ordinary answer and the app simply says
  the modem was not allowed.
- **The list holds USB devices rather than ports**, including the ones this
  build cannot drive, each with the reason. A list that quietly omits the
  modem in your hand leaves you unable to tell "not plugged in" from "plugged
  in and not supported", and those have different next steps.

One more difference is invisible but explains the behaviour: **the radio does
not come back by itself after the app restarts.** The device permission
belongs to the app, the cable may already be out, and re-attaching is a
person's move. Your segment phrase is not asked for again — the node
remembers it, and pulling a cable is not the same as detaching a radio.

Which boards work: this build drives **Silicon Labs CP210x** bridges, the
most common one on RNode boards. If your board is listed as unsupported, the
same row says which bridge it has.

The list will hold more than your board. Every port is named, including
the ones the node deliberately did not open:

| row | what it is |
|---|---|
| **an RNode modem** | the one you want |
| **Meshtastic node** | the other carrier — see [README.md](README.md#which-carrier-works) |
| **busy** | another program holds the port: a second node, a serial monitor, the Meshtastic app |
| **foreign** | the device answered in somebody else's protocol — none of our business |
| **silent** | opened, asked twice, no answer |
| **skipped** | not opened at all, with the reason beside it |

---

## Step 2. The segment phrase

When attaching, the node asks for the **segment phrase** — the words that
become your radio segment's key.

- **The same words on every radio of the segment.** Character for
  character. One character apart and both radios transmit while neither
  hears anything, with no error anywhere.
- **At least 16 characters.** The phrase is never trimmed or padded: being
  helpful here would produce a node confidently sitting on a segment
  nobody else is on.
- **The words themselves are not kept** — only the key derived from them.
  Nothing downstream needs the words again, and a phrase is exactly the
  form of this secret a person is most likely to have reused somewhere.

The same phrase yields the same segment however it was entered: on the
screen, through `--mesh-seed`, or inside an invitation. There is exactly
one derivation in the build, so "we typed the same thing and cannot hear
each other" is not a state this can reach.

### If you were not asked for a phrase

Then this device already knows a segment — it **arrived with an
invitation**.

That is not a detail, it is the main scenario. When you mint an invitation
link on a node with a radio attached, the segment descriptor (carrier,
profile, key) rides inside the sealed link. The person you invited knows
nothing about radio and does not need to: one day they plug a board in,
press attach, and are asked nothing at all.

This is what the whole exercise buys: **the configuration lands before it
is needed.** The moment the internet disappears is precisely the moment
nobody can send anybody anything.

The rules around it are strict:

- a segment this build cannot act on (a carrier it does not speak, an
  unknown profile, a different key-derivation version) — refused, with the
  reason named;
- a **different** segment when one is already configured — also refused.
  Silently re-keying somebody's radio would take a working link and point
  it elsewhere, and the only symptom would be silence. If that is what you
  want, detach the radio and attach it again — your decision, taken by you.

---

## Step 3. What happens now

**One radio per node.** Attaching a second is refused with "a radio is
already connected", and that is a runtime invariant rather than a limit of
the screen. For a different board, `detach this radio` first.

**The attachment survives a restart.** The node brings the same radio back
by itself next time. If the board is not there, that is not an error and
nothing is erased: the cable will come back. The status says honestly what
did not work.

**Detaching is the `detach this radio` button** on the same screen. It
does not merely put the radio down, it forgets it — otherwise the next
start would bring back a radio somebody had just switched off.

**Radio is the last path, not the only one.** While the internet or a
local network is alive, the node uses them. The switch is automatic and
asks nothing: a connection belongs to the space and the identity, not to
the network it first arose on.

---

## Step 4. Meet over the radio

This is the scenario radio exists for: two people, two boards, nobody has
internet, and they have never met.

**Settings → Radio → meet over the radio.**

1. **say who I am** — this device's name and public key go on the air.
   Everyone in range hears them; that is exactly what somebody needs in
   order to seal an invitation to you personally. LoRa is slow — give it
   half a minute.
2. The neighbouring radio appears under **heard on this segment**.
   Everything there is what a radio **claimed about itself**. An
   invitation is sealed to the key it announced, so somebody claiming
   another name receives an invitation they cannot open.
3. **check they can hear you** — one frame, asking whether they hear you.
   The answer appears in their row. It is cheap, which is why it comes
   first.
4. When the row says **Radio link established**, the button becomes
   **start a line with …**. That press is the invitation: six frames,
   about a minute of air, and it cannot be taken back.
5. On the other side it lands under **waiting for your answer**. Until
   they press **accept** it has cost them nothing: nobody is a member and
   no key has moved.
6. After accept there is a **line** — an ordinary two-person space, simply
   born without internet.

A separate link, **or invite them into “…”**, invites the neighbour into a
space you already have open instead of a new line.

### The states worth being able to read

The screen says only what it knows, and the differences are deliberate:

| line | meaning |
|---|---|
| **Radio link established** | they are reachable by radio |
| **Direct radio link** | the same, and observed to be direct — no intermediate node |
| **They heard it. Waiting for them to answer.** | the invitation was delivered; no answer yet |
| **Could not confirm delivery.** | the send was not confirmed. This is **not** "it did not arrive" — they may already hold it. A repeat will not create a second line |
| **They ARE a member here; their device does not know it yet.** | they asked, you accepted, and the radio went quiet before the answer reached them. Send it again when they are back |
| **Nobody answered.** | their radio is off or out of range |

The screen will never say "not delivered" when all it actually knows is
"not confirmed". Those are different things and we do not merge them.

---

## What radio carries, and what waits

One event has an **airtime budget of 30 seconds**. Over it, and it does
not go by radio. The number is not taste; it is arithmetic over the real
physics of the `long-fast-ru` profile:

| what | size | frames | air |
|---|---|---|---|
| a short message | 340 B | 1 | 4.6 s |
| a reaction | 387 B | 1 | 4.6 s |
| a long message | 857 B | 3 | 13.8 s |
| an image, 2 KiB preview | 2.4 KB | 6 | 27.6 s — at the limit |
| an image, 40 KiB preview | 41 KB | 99 | **7.5 minutes**, during which nothing else moves |

That is wall clock, not textbook arithmetic: each frame includes the
700 ms the modem spends beyond its modelled time on air. Until those were
counted the ceiling was predicted at 2586 bytes and a live board derived
2155 — the gap was found by hardware, not by reasoning.

Hence the rule: **text, reactions and small control traffic go by radio;
photographs, voice, audio and files wait for a wider path.** The threshold
is the same for every kind of media — it is about the size of an event,
not its type. On top of that, a node does not serve attachments over radio
at all: a file is assembled from chunks, and that is tens of minutes of
air for one picture.

The exact ceiling in bytes is computed by the node itself, by asking the
carrier what a frame costs — it comes out around two kilobytes. A faster
profile raises it with no code change.

**And a person is told.** A post or a message that did not fit does not
vanish and is not marked failed — a line appears under it:

> Waiting for a wider path — 41 KB, and the radio carries 2.1 KB.
> It goes as soon as the internet or the local network is back.
> Nothing is lost.

It really does go as soon as a wider path exists. Silence instead of that
line would be worse than the jam it replaces: a jam ends, while silence is
indistinguishable from a message that was never sent.

---

## Speed, honestly

LoRa is single-digit kilobits per second **shared between everyone**, and
that is physics rather than an unfinished feature. In practice:

- a summary for each space goes out once a minute, not every two seconds
  as it would over the internet;
- a 6.6 KB message is about a minute and a half and 17–18 frames, against
  a theoretical minimum of 16;
- two nodes getting introduced is tens of seconds, not an instant.

A minute and a half for a message is bad right up until the alternative
becomes zero messages.

---

## If nothing is heard

Top to bottom, by how often it is the cause:

| symptom | most likely |
|---|---|
| no "an RNode modem" row in the scan | the board is not flashed to RNode ([FLASHING.md](FLASHING.md)), or the cable carries no data |
| **busy** on the port you want | another program holds it — a second node, a monitor, the Meshtastic app |
| the radio attached, no neighbour appears | different segment phrases. There is nothing to inspect — simply type it again on both sides, character for character |
| the neighbour is visible, the invitation does not arrive | distance. Move the boards apart; radios lying against each other sometimes deafen their own receivers |
| everything attached, but messages do not go by radio | as designed, while the internet or the local network is alive. To test the air itself, switch the rest off — see below |
| a message sits with the "wider path" line | it is too large for the air and is waiting for the internet. Not an error |

### How to prove it was the radio

The main trap: two nodes on one machine find each other over the local
network in a second, the message arrives instantly, and you conclude the
radio works. It may not even be plugged in.

So, when testing radio: `--no-lan` on both nodes and no relay in settings
(a fresh data directory has none). Then the only path left is the air, and
the result means exactly what it means. A ready-made run:
[CLI.md](CLI.md#the-whole-live-run).

And the other half, without which the first proves nothing: **detach the
radio and confirm the messages stop arriving.**

# Radio in quite.space

*По-русски: [../README.md](../README.md)*

Radio is how you keep talking when there is no internet at all: two LoRa
boards, a few kilometres between them, and no server in the middle.

This section is four documents, and every fact has exactly one home. Where
something is said once, the others link to it rather than repeat it.

| Document | About | For whom |
|---|---|---|
| **[FLASHING.md](FLASHING.md)** | putting RNode firmware on a board | once per board |
| **[USING.md](USING.md)** | attaching a radio in the app and talking over it | everyone |
| **[CLI.md](CLI.md)** | the same actions and the measurements, from a shell | headless setups |
| this file | which carrier is proven and which is not, plus the vocabulary | read first |

---

## Which carrier works

**RNode is the proven carrier.** It is modem firmware: the board takes
bytes over USB and radiates them, deciding nothing on our behalf.
Everything quite.space can do over radio was measured on it.

**Meshtastic is an experimental feature and is not proven in this role.**
The driver is in the build and a node does attach to such a board, but the
product scenario — meeting over the air, an invitation, a conversation with
no internet — has **never been run on Meshtastic end to end**. Where it
goes from here is not decided. If you have a Meshtastic board and what you
want is radio in quite.space, flash it to RNode: [FLASHING.md](FLASHING.md).

### How RNode was checked, in short

Two Heltec WiFi LoRa 32 V3 boards on a desk, 868.95 MHz, SF11/250 kHz,
20 dBm. Three levels, each ending in a yes or a no:

| check | what was measured | result |
|---|---|---|
| a single frame | does a packet reach the application on the other board | **100% both ways**, p50 0.71 s, not one refusal from the modem |
| a whole message | does a message reassemble byte-exact from its fragments | **10 of 10** at sizes up to 6.6 KB |
| the live product | the entire path with no relay and no internet | **passed** |

The live run is worth the detail, because it is the product's promise. Two
nodes, both with `--no-lan`, no relay anywhere:

```
saw each other over the radio         21 s
probe -> link -> invitation           25 s
line established in both directions   42 s
text crossed both ways               199 s
```

The second half is about the internet dying. Two nodes already know each
other through a relay, the internet is cut, nothing is restarted: the next
message **crossed by radio in 85 seconds**, and when the internet came
back nothing was duplicated and nobody had to be invited again.

Against Meshtastic we compared only the first level — single frames — on
the same physics: RNode 100%/100% and p50 0.71 s, against 93.3%/100% and
p50 1.40 s one way, 9.30 s the other. Honest caveats: the boards were not
entirely the same two across both runs, the runs are hours apart, and 30
frames is a screening gate rather than a distribution. What is not
ambiguous: RNode's latency IS the time on air, and it is symmetric to the
millisecond.

To reproduce any of the three levels yourself: [CLI.md](CLI.md).

---

## Vocabulary

**Segment** — the group of radios that hear each other and count each
other as their own. Technically a segment is defined by exactly one thing:
its **segment phrase**.

**Segment phrase** — the words every radio derives the same key from. Same
words, same segment; one character apart and both radios transmit happily
while **neither hears anything**. No error appears anywhere: silence is
indistinguishable from "nobody is nearby". Almost every rule in
[USING.md](USING.md) follows from this.

The segment key protects the **mechanism**, not the conversation: it
proves a control frame was written by somebody on the segment. Message
content is already sealed under the space's own keys before it reaches the
radio layer, and radio neither adds to that nor weakens it. That an
exchange is happening at all is visible to everyone in range — that is
physics, and we do not pretend otherwise.

**Profile** — the physical parameters of the air: frequency, bandwidth,
spreading factor, power. Every radio on a segment must be on one profile.
The build ships one: `long-fast-ru` (868.95 MHz, 250 kHz, SF11, CR4/5,
20 dBm).

**Line** — the two-person space that appears when you meet somebody over
the radio. An ordinary quite.space space, simply born without internet.

---

## What radio does not replace

One radio per node, and it is the **last** path rather than the only one.
While the internet or a local network is alive, the node uses those: they
are wider and faster. Radio takes over when the rest has run out, and does
so by itself, asking nobody.

LoRa runs at single-digit kilobits per second **shared between everyone**.
Text moves fine; photographs, audio and files wait for the internet — why
exactly, and what a person sees meanwhile, is in
[USING.md](USING.md#what-radio-carries-and-what-waits).

---

## Neighbouring documents

- [../../RADIO_SETUP.md](../../RADIO_SETUP.md) — the Meshtastic path:
  firmware, region, segment channel, profile. A different carrier and a
  separate story; everything about RNode is here. *(Russian only.)*
- [../../RADIO_STATUS.md](../../RADIO_STATUS.md) — an engineering snapshot
  of a day with Meshtastic boards. A working document, not a guide.
  *(Russian only.)*

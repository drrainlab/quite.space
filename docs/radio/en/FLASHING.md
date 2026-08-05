# Flash a board to RNode

*По-русски: [../FLASHING.md](../FLASHING.md)*

One operation per board. After it the board simply works and you never
come back here.

What is here: hardware, firmware, the law. What is not: attaching it to
quite.space — that is [USING.md](USING.md).

---

## What we are actually doing

RNode is modem firmware. After it the board stops being a "network node"
and becomes an honest transmitter: it takes bytes over USB, radiates them,
hears the air, hands what it heard back. No routes, no channels, no
decisions of its own.

```
quite.space  ──USB──▶  RNode firmware  ──LoRa──▶  the other board
```

That is precisely why RNode is the proven carrier here: everything to do
with delivery — fragmentation, repairing losses, deduplication — lives in
quite.space, where we can measure it and fix it. A modem should not be
clever. It should be predictable.

> **RNode is not Reticulum.** The firmware comes from the Reticulum
> project and is installed with a tool from it, but quite.space does not
> use Reticulum as a network: its own identities, its own keys, its own
> log. We take the modem and nothing else.

---

## What you need

- A LoRa board on an ESP32 with an SX1262 chip — we worked on the
  **Heltec WiFi LoRa 32 V3**. RNode's support for Heltec v3 is officially
  marked experimental, and that is fair: it works for us, but we cannot
  promise it on the authors' behalf.
- **An antenna, screwed on before power.** A board with no antenna can be
  destroyed by its first transmission. This is the only place in the
  documentation where something breaks physically.
- A USB cable that carries data rather than only charge. Half of all
  "the board is not detected" is this.
- Python 3 and `pip`.

---

## Step 1. Install rnodeconf

`rnodeconf` is the official installer for RNode firmware. It arrives with
the Reticulum package:

```bash
pip install rns
```

Check that it is there:

```bash
rnodeconf --help
```

RNode firmware is GPLv3, and we neither store nor rebuild it: `rnodeconf`
downloads the official images itself. So what ends up on your board is
what its authors released, not what we decided to keep in our repository.

---

## Step 2. Find the board

Plug it in over USB and see which path appeared:

```bash
ls /dev/cu.usb*                # macOS
ls /dev/ttyUSB* /dev/ttyACM*   # Linux
```

Typical names: `/dev/cu.usbserial-0001` (a board with a CP2102 bridge) or
`/dev/cu.usbmodem…` (a board with native USB).

> **The path is not stable.** After a reset the same board can come back
> under a different name. You do not need to memorise it — in the app the
> path is found by scanning, see [USING.md](USING.md).

---

## Step 3. Flash

```bash
rnodeconf --autoinstall
```

What follows is a dialogue. It walks through three questions:

1. **What kind of device** — choose "a board to be turned into an RNode"
   (the wording differs between versions).
2. **Which board model** — the one place you must not be wrong: different
   boards on the same chip wire their radio differently, and firmware for
   somebody else's board gives you a silent radio with no error anywhere.
   For our case: **Heltec LoRa32 v3**.
3. **Which band** — 868 MHz for Europe and Russia, 433 or 915 elsewhere.
   Check what your board is built for; it is usually printed on the
   antenna or stated in the product listing.

> Menu numbering changes between versions, so it is not written down here:
> go by the text of the item, not by the digit. When in doubt,
> `rnodeconf --help`.

The board is erased and reflashed. Meshtastic, if it was there, goes with
every channel configured on it. Getting it back is the same process in
reverse — see [../../RADIO_SETUP.md](../../RADIO_SETUP.md) (Russian).

We flashed RNode 1.86; it reports itself as host-controlled, SX1262, and
820–1020 MHz.

---

## Step 4. Confirm the board answers

quite.space has a diagnostic of its own, and it deliberately **shares not
one line of code with the driver**: when a carrier delivers nothing, the
driver is a suspect, and a diagnostic built on the suspect can only
confirm its own assumptions.

```bash
go run ./cmd/rnode-probe -dev /dev/cu.usbserial-0001
```

It opens the port directly and prints every KISS frame it sees. A live
board names its firmware and its platform. The rest of the flags are in
[CLI.md](CLI.md#rnode-probe--one-board-up-close).

> **`terminal radio list` will not help here.** That command looks for
> Meshtastic nodes and knows nothing about RNode — it will not call a
> freshly flashed board a radio. That is not a fault. A modem is
> recognised either by `rnode-probe` or by the scan inside the app.

---

## About the law, in one paragraph

Frequency and power are not a matter of taste; they are a matter of your
country's law. The build transmits on the `long-fast-ru` profile: 868.95
MHz, up to 20 dBm — inside the range Meshtastic's own region table defines
for RU (868.7–869.2 MHz, up to 20 dBm).

And one property of RNode worth understanding before you take it
anywhere: **the modem does exactly what it is told.** It has no regional
table that will stop you leaving the band. Whether you are allowed to
operate there is on you.

---

## If it will not flash

| Symptom | Most likely |
|---|---|
| the board is not in `/dev` | a charge-only cable, or no driver for the USB bridge (CP2102 / CH340) |
| `rnodeconf` cannot see the port | another program holds it: a serial monitor, the Meshtastic app, a running quite.space node |
| flashed, but `rnode-probe` stays silent | the wrong board model at step 3 — flash it again |
| the board will not enter its bootloader | hold BOOT, tap RST, release BOOT, and retry |

Only one program can hold a board at a time. If you have a node running
with this radio, stop it while you flash.

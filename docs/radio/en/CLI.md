# Radio from the command line

*По-русски: [../CLI.md](../CLI.md)*

Everything the radio screen does, plus the instruments the carrier was
measured with. What the actions mean and why is in [USING.md](USING.md);
this is only how to invoke them.

---

## Run a node with a radio

```bash
go run ./cmd/terminal ui \
  --passphrase P --data ~/.quiet-places --name myname \
  --rnode /dev/cu.usbserial-0001 \
  --mesh-seed "one shared segment phrase"
```

| flag | what it does |
|---|---|
| `--rnode PATH` | attach an RNode modem at this path. **The path itself**, with no `serial:` prefix |
| `--mesh-seed "…"` | the segment phrase, at least 16 characters |
| `--no-lan` | do not look for anybody on the local network |
| `--no-browser` | do not open a browser (headless) |
| `--port N` | port for the local API and the interface |
| `--data DIR` | data directory; it is locked to one process |

Two rules the build checks and explains by refusing:

- **`--rnode` without `--mesh-seed` is refused.** The segment is what makes
  two radios one segment; without it no frame verifies.
- **`--rnode` together with `--mesh` is refused.** Those are two different
  radios, and a node attaches one.

An attached radio is remembered, so next time the flags are unnecessary:
the node brings it up itself.

---

## Through the local API

The node prints its token at startup (`token=…` in the log). After that it
is ordinary `curl` with a header:

```bash
T=<token>; U=http://127.0.0.1:8801
A(){ curl -s -H "X-QP-Token: $T" "$@"; }
```

| action | call |
|---|---|
| what is on the ports | `A -X POST $U/api/gateway/scan` |
| attach a radio | `A -X POST $U/api/radio/attach -d '{"port":"/dev/cu.usbserial-0001","phrase":"…"}'` |
| attach when the segment is already known | the same with `"phrase":""` |
| detach and forget | `A -X POST $U/api/radio/detach` |
| announce yourself on the air | `A -X POST $U/api/radio/announce` |
| who is heard | `A $U/api/radio/neighbours` |
| probe, then invite | `A -X POST $U/api/radio/meet -d '{"device":"<device id>"}'` |
| invite into a particular space | `A -X POST $U/api/radio/invite -d '{"space":"<hex>","device":"<hex>"}'` |
| what awaits my answer | `A $U/api/radio/invitations` |
| accept | `A -X POST $U/api/radio/invitations/accept -d '{"id":"<hex>"}'` |
| radio state | `A $U/api/status` → the `radio` field |
| the whole radio screen | `A $U/api/gateway` |

`/api/radio/meet` is one endpoint for two acts, and it decides which one
is appropriate: with no link yet it sends a one-frame probe (`state:
probing` in the response); with a link up it sends the invitation (`space`
in the response). Why they are separate:
[USING.md](USING.md#step-4-meet-over-the-radio).

---

## The measuring instruments

Three levels, exactly the ones the carrier was checked with
([README.md](README.md#how-rnode-was-checked-in-short)). Each answers its
own question and refuses to answer anybody else's.

### `rnode-probe` — one board up close

The only tool here that deliberately **shares not one line of code with
the driver**: when a carrier delivers nothing, the driver is a suspect.

```bash
go run ./cmd/rnode-probe -dev /dev/cu.usbserial-0001
```

It opens the port directly, configures the radio and prints every KISS
frame it sees, decoding the ones whose meaning is known. Useful flags:
`-send` to transmit a probe frame after configuring; `-watch 20s` for how
long to listen; `-freq/-bw/-sf/-cr/-txp` for the physics.

Why this matters: RNode has a state where the settings were accepted and
the radio did not power up. Without a look like this one it is
indistinguishable from "nobody is around" — and that was the single most
expensive mistake in all of the radio work.

### `rnode-baseline` — does one frame arrive

The carrier and nothing above it: one self-contained numbered packet at a
time, nothing fragmented, nothing reassembled.

```bash
go run ./cmd/rnode-baseline \
  -a /dev/cu.usbserial-0001 -b /dev/cu.usbserial-9 \
  -count 30 -interval 3s
```

It prints delivery ratio, p50/p95, refusals from the modem and the numbers
of the lost packets, and ends in **PASSES** or **DOES NOT PASS** — the
gate is 98% delivery with zero refusals. The threshold lives in the
program, because a criterion nobody wrote down ends in neither a yes nor
a no.

Why this level and the next are kept apart:

```
frames arrive but messages do not  -> the fault is ABOVE this:
                                      fragmentation, reassembly, sync
frames do not arrive               -> there is nothing to build
                                      reliability on
```

### `rnode-transfer` — does a message arrive

Whole transfers: fragmentation, the selective-repeat window, SACKs,
reassembly. It builds its endpoints exactly the way the node does, so what
it measures is production's shape with a different carrier underneath.

```bash
go run ./cmd/rnode-transfer \
  -a /dev/cu.usbserial-0001 -b /dev/cu.usbserial-9 \
  -reps 2 -trace /tmp/transfer.trace
```

The output is a table by size: how many frames, how many transfers arrived
intact, the median time. The headline is `complete_transfer_rate`, and
beside it "layer says" — what the transfer layer itself counted. The two
can disagree, and the disagreement is honest: the layer counts
**confirmed** completions, and a message can arrive byte-exact and stay
unconfirmed. The gate is strict: **any** transfer that arrives corrupt
fails the run outright, whatever the rate.

Useful flags: `-size N` for a single size; `-window`, `-ack`, `-gap` to
override the layer's parameters (production defaults otherwise);
`-between` for quiet time between transfers.

### The whole live run

```bash
scripts/radio/livegate.sh
```

It drives both halves of the product scenario through the API with no
clicking: six spaces, meeting over the radio, an invitation, text both
ways with no relay — and separately the internet-failure check, where a
connection established through a relay carries on over the air and nothing
is duplicated when the relay returns.

The board paths are near the top of the script — edit them for yours. The
run directory comes from `QUIET_GATE_DIR`.

### Tracing the transfer layer

```bash
QUIET_RADIO_TRACE=/tmp/alice.trace go run ./cmd/terminal ui …
```

A structured log of the radio layer on a live node: which fragments went
out, when each SACK arrived, what was repaired and why a transfer stopped.
Every diagnosis in this work came from here rather than from guessing.
While the variable is unset the tracing costs nothing.

---

## Tools for Meshtastic

All of these are about **the other carrier**. They will not work with a
board flashed to RNode: `terminal radio list` looks for Meshtastic nodes
and will not call a modem a radio.

| command | what it does |
|---|---|
| `terminal radio list` | what is on each serial port |
| `terminal radio flash` | install **Meshtastic** on a board (erases it) |
| `terminal radio region` | read or set region and preset |
| `cmd/quiet-radio` | read the configuration, check it against a profile, snapshot, restore |
| `terminal meshhub` | a fake Meshtastic mesh locally — debugging with no hardware |

The detail is in [../../RADIO_SETUP.md](../../RADIO_SETUP.md) (Russian).
They are listed here only so nobody hunts for them in this file.

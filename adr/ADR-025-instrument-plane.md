# ADR-025 — The Instrument Plane: a place becomes a subject of the space

Status: accepted (2026-08-21) · QI wave, v1 scope: panel + telemetry +
reference simulator (commands are QI-4, behind a grant-primitive design
note that does not exist yet — the house rule for forks)

## What this decides

A physical endpoint — a greenhouse sensor, a door contact, one day an
ESP32 on a windowsill — becomes a **participant** of a space: its own
device keys, its own terminal identity, the operator's principal as
controller, certified through the same admission gate as every other
device. It is a subject of the space **without being a person**, and
the whole design flows from keeping those two ideas separate:

- **Identity**: an instrument is admitted exactly like the agent and
  the gateway before it (three-key participant, `certifyOwnedDevice`,
  manifest revision chain). Nothing about admission is new.
- **Cryptography**: an instrument NEVER holds the conversation key.
  Its readings seal to a second epoch lineage — the instrument epoch.
- **Projection**: an instrument renders once, in «Инструменты», never
  in the member list. The registry model is shared; the projection is
  not, because the place is a subject but not a person.

## The access matrix (the wave's founding gate, proven by test)

                    conversation epoch      instrument epoch
    member                 ✓                       ✓
    instrument             ✗ cannot                ✓
    relay                  ✗ ciphertext            ✗ ciphertext

`TestTheInstrumentPlaneAccessMatrix` (terminals) holds every cell of
this table against real frames; it is the wave's contract, not an
illustration.

## The instrument epoch (`membership.instrument_epoch.v1`)

The same `WrapEpoch` primitives as the conversation epoch, a separate
numbering, a separate wrap list: **member devices ∪ instrument
devices**. Domain separation comes from sealing against
`instrPlaneID = sha256("qp.instrument-plane.v1" ‖ spaceID)`, and the
envelope names the lineage honestly: `PayloadInstrumentSealed = 3`
(a pre-instrument build absorbs such a frame tolerantly — it counts as
undecryptable, it never breaks the space).

**The invariant, stated in full (owner's amendment 5):**

> The instrument epoch isolates instruments from the human
> conversation; it does NOT isolate instruments from one another.

That second half is the deliberate price of v1's simplicity: every
instrument of a space can read every other instrument's readings. If a
space ever needs mutually-suspicious instruments, that is a third
lineage, not a patch on this one.

**Rotation** happens at every event that changes who may read the
plane — member added or removed, device revoked, instrument attached,
detached or revoked. All four conversation-epoch rotation sites in the
node couple both rotations. Detachment without rotation would let the
detached device keep reading forever; `TestADetachedInstrument-
StopsReadingAfterTheTurn` proves the key actually turns. Both sides
persist their epoch rings and survive a restart (restart gates on the
terminals layer and the node layer).

## The reading (`observation.value.v1`)

A reading is `{channel, value, observed_at, stale_after, simulated}` —
and nothing else. The value is a **tagged fixed point**: `{magnitude,
negative, decimals}` for numbers (214 with decimals=1 is 21.4), a
boolean, or a short enum word. No float ever enters the wire, and no
precision is cemented into the schema.

**Meaning lives only in the signed manifest** (owner's amendment 3).
`qp.instr=<channel>:<kind>[:<unit>][:<label>]` labels — percent-escaped
so «Температура: улица», «CO₂» and «%» survive the colon grammar —
declare what each channel IS; the frame carries no kind and no unit,
so a frame can never contradict the declaration it rides under. The
declaration has its own budget (`MaxInstrumentChannels`, refusal
`too_many_instrument_channels`) instead of dying mysteriously against
the manifest-wide label cap.

`stale_after` is mandatory: a reading that cannot say when it stops
being true is not accepted. `simulated` is declared, never inferred —
the observation doctrine of the seed sensor, unchanged.

## Telemetry is a volatile plane, not a feed

Readings ride **fleeting frames**: header expiry = observed_at +
stale_after, `maxForwards = 1`, no custody. They reduce into LWW slots
keyed `(instrument, channel)` — never feed entries — and they are
prunable from public projections. A greenhouse ticking all day leaves
the conversation exactly as quiet as it found it.

**Bearer hops are not Quiet forwards** (owner's amendment 7):
`maxForwards=1` is a Quiet forwarding class — origin → relay → member
is ONE forward however many TCP, LoRa or mesh hops the bearer path
takes underneath. The protocol hop budget must never be the thing that
kills the sensor plane over radio.

## The reference simulator is a driver, not a mock

The deterministic greenhouse (`node/instrument.go`) is the behavioral
contract every later driver must meet: same manifest shape, same
identity shape, same frames, same panel. It is seeded and clock-
injectable — a test moves time by hand and asserts exact values
(owner's amendment 8). The promised migration is: Virtual Greenhouse →
quiet-terminal on a Pi → ESP32+BME280 with **zero changes to the
browser or the protocol**.

## Naming

The protocol word is **instrument**. «Terminal» was already taken at
the seed — `id.TerminalID` names a *space* — and the relay taught us
what a name that means two things costs. No new API, type or JSON
field says `terminal` for anything physical; the wire and the store
say `instrument_id`, and only `instrumentID + channel` addresses a
reading. The UI label may say «Инструменты» or anything else a human
likes; the protocol vocabulary is fixed.

## Deliberately not in v1

- **Commands (QI-4)**: an actuator's channels render read-only until a
  grant primitive exists (`instrument.control_granted.v1`, root-signed,
  expiring; sealed idempotent commands; receipts). Design note first.
- **quiet-terminal daemon** (Pi/GPIO/I²C) and the **ESP32 SDK** — the
  drivers the simulator is the contract for.
- History rollups; appdef graduation of the panel.

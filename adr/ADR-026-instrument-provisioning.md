# ADR-026 — Instrument provisioning: keys that never leave the device

Status: accepted (2026-08-22) · QI-M wave (ESP32 SDK) · builds on ADR-025

## What this decides

A physical instrument mints its own keys and keeps them. The authority
node sees three PUBLIC halves and a signed manifest, certifies them, and
hands back exactly what the device needs to speak on one space's
instrument plane — and nothing it does not. This note records the four
decisions that make that safe, in the order they were found.

## 1. An enrollment is proven ownership, not a bundle of public keys

An instrument has three cryptographic identities: the device Ed25519 key
(signs frames), the device X25519 key (receives epoch wraps), the
terminal Ed25519 key (signs the manifest). Handing an authority three
public keys proves nothing about whether one party holds all three.

`instrument.enrollment.v1` = `{version, device_pub, x25519_pub,
terminal_pub, manifest_hash, label, nonce, manifest_frame,
device_signature, terminal_signature}`. Both the device key and the
terminal key sign ONE body (keys 1–8): the device proves it requested
this enrollment and this manifest; the terminal proves it consents to be
bound to this device; the manifest inside is itself signed by the
terminal. Decode verifies all three and refuses anything less. The
authority certifies a proven binding.

## 2. The manifest is carried on the device's behalf

A manifest frame in a private space is sealed with the CONVERSATION
epoch — a key an instrument never holds (ADR-025). The device therefore
does not publish its manifest; the owner's node carries it inside its
own envelope (`PublishManifestFrameOnBehalf`). Two levels of authorship,
said in words so nobody later "repairs" them apart:

    content author      = the instrument (its terminal key signed the manifest)
    transport publisher = the owner     (its device key signed the envelope)

The registry verifies a manifest by `manifest.Terminal` and never asks
who carried it. **`envelope.Device == manifest device` is not a rule and
must never become one.** A manifest revision is a new enrollment.

## 3. Detachment is a boundary of authority, not only of secrecy

Members keep old epoch keys for delayed delivery. A detached device that
still holds epoch N can therefore produce a perfectly valid, perfectly
sealed observation under N. Revocation cannot rest on "it does not know
the new secret".

The receiver's rule: an instrument-sealed frame is accepted only if its
device is addressed by the CURRENT instrument epoch — the recipient list
the owner signed into the latest `membership.instrument_epoch.v1`. A
frame that fails this is counted as `UnauthorizedInstrument`, apart from
`Undecryptable`: "you are not an instrument here any more" is a
different fact from "we lack a key". The authorization set moves only
forward with the epoch number (a late-arriving lower epoch keeps its key
usable but never re-authorizes a dropped device), and it is keyed apart
from the restored key ring — a restart replays the log before the ring
raised `current`, and keying the two together silently refused every
reading after a reboot until the next rotation. The node restart test
found that; it is a regression test now.

## 4. The chain cannot have a hole, so the device keeps an outbox

`kernel/eventlog` buffers a frame whose predecessor it has never seen —
forever. There is no exemption for fleeting frames, and this ADR forbids
adding one: leniency in the log would turn every power cut into silent
data loss. The device's duty instead: build the frame, persist ONE
record holding the new sequence, the new tip and the complete pending
frame (crash-consistently — an A/B slot journal with a CRC), and only
then let the frame leave; after a reboot the owed frame is sent first.
`qi_instrument_emit` / `qi_instrument_pending` / `qi_instrument_ack_sent`
are that contract; the host test simulates the power loss.

## The provision

`instrument.provision.v1` = `{space, principal, certificate_frame,
[instrument_epoch_frame…], manifest_ack}`. Whole signed epoch frames, so
the device absorbs them as a replica would and learns the Lamport clock
— but chain-free: a device replicates nobody's log, so it verifies the
envelope's signature, the space, the schema and the principal it was
provisioned to (`AbsorbInstrumentEpochFrame`), and nothing about the
sender's chain. The sender's device certificate is not verified on the
device: it holds no certificate store, and the bearer that will one day
deliver rotations is where that trust is decided.

Enrollment is idempotent for the same `(device, terminal, manifest)` —
a provision comes back, nothing else happens — and a conflict (HTTP 409)
for a known identity with a different binding. A dropped serial link and
a second button press are normal life.

## Honesty rules the device lives by (fail closed)

- no unix time source → no emission (`QI_ERR_NO_TIME`); observed_at is
  never guessed;
- no current epoch key → no emission (`QI_ERR_NO_EPOCH`);
- `stale_after == 0` → refused;
- a sensor with nothing honest to say returns NaN and is not published;
- `simulated` is declared by the driver, never inferred.

## What the MCU does not do

It holds no chat, no journal, no conversation epoch, no certificate
store, no root. It provisions, absorbs epochs, publishes readings, and
(QI-4, not yet) receives sealed commands and acks them. Everything else
is a node's job.

## The dev stand is not a bearer

`--dev-ingest` opens one door on the node: frames from ONE enrolled
instrument, bound to its certified device and its terminal id, over
loopback. `cmd/instrument-serial` feeds it from USB serial. How
instruments will really reach a space — LAN, relay, BLE, LoRa — is the
next decision, taken after this one is proven on a board.

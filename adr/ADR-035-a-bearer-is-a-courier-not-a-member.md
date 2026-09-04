# ADR-035 — A bearer is a courier, not a member

Status: accepted (2026-09-04) · QI-B1 wave (Wi-Fi bearer) · builds on ADR-015, ADR-025, ADR-026

## What this decides

A physical instrument reaches its node over the network the way a
courier reaches a house: it knows one address, carries one bag, and the
door checks who it is before anything else happens. This note records
the laws of that door — how a board joins the LAN listener without
becoming a sync peer, how epochs and time reach it, and what a broadcast
space changes — plus the options weighed and refused.

## 1. The link scopes to exactly its space

The LAN listener adopts a sync peer into every space at once; frames for
other terminals are simply not matched. That is correct for a peer — it
IS a replica of whatever both sides share — and wrong for an instrument:
a summary names its terminal in plaintext, and terminal ids on the air
are exactly the metadata the hint scheme (§4) exists to withhold. An
instrument's conn is adopted through `adoptLinkFiltered` with an allow
that names one terminal. Never `adoptLink`. The filter is consulted
fresh every pump tick, so a space created later stays off this wire too.

## 2. The first message is the dispatch

Peers and instruments arrive through the same TLS accept, and nothing in
the transport says which is which — the cert is ephemeral and claims
nothing (ADR-015 lineage). The FIRST MESSAGE decides:

- hello, summary, frames, anything a peer opens with → the conn is
  adopted exactly as before the door existed, and every packet the
  classifier consumed is re-fed bit-for-bit through a prefix wrapper.
  The engines never learn anybody peeked; the peers' own tests run
  through the seam unchanged.
- `msgEpochReq` → the door (§3).

Both sides say hello BEFORE reading — that is what lets two classifying
nodes converge instead of deadlocking, and a board simply ignores the
hello. A conn silent past the classify window is adopted as a peer: the
pre-door behavior, kept for peers that connect and listen.

Wire numbers: `msgEpochReq = 8`, `msgEpochs = 9`, payload keys 11/12.
The plan reserved 5/6 and 7–10; by the time this wave landed, the TN
wave had lawfully spent all of them (custody, beacon, hello). Append-
only means append — the collision is recorded here so nobody "fixes" it.

## 3. The knock is session-bound; the door is not an oracle

`msgEpochReq` carries `{space, device, sig}` where sig is Ed25519 over
`"qp-instr-door-v0:" ‖ certfp ‖ space` — certfp being sha256 of the leaf
certificate DER the node presented on this very session (RFC 5929
tls-server-end-point binding). The lanhello binds through exported
keying material; the door cannot, and the reason is a spike finding, not
a preference: arduino-esp32's TLS stack keeps the peer certificate and
fingerprints it (`getFingerprintSHA256`) but exposes no RFC-5705
exporter, and the `mbedtls_ssl_context` behind it is private. The
fingerprint spike matched the board's 32 bytes to the node's own, byte
for byte.

The binding holds because the node's cert is ephemeral and its private
key never leaves the process: a person in the middle must present a
DIFFERENT cert, so the board signs the wrong fingerprint and opens
nothing. Replay is not a separate worry — the knock travels inside TLS,
so recording one already requires the MITM the binding defeats. What the
fingerprint does NOT give is per-connection uniqueness within one node
lifetime; nothing here needs it.

The door opens only for an ENROLLED EXTERNAL INSTRUMENT of that space —
the same keystore record the serial stand's ingest binds to. A member's
device does not pass; a certificate alone does not pass.

Every failure is the same failure: bad signature, unknown space, a
stranger's device — one silent close, indistinguishable. The door must
not be an oracle of which spaces exist here; that is the privacy bar the
Hint scheme sets, and an error message would spend it. (The hello that
went out first is link-scoped and names no space: it tells a prober only
that a quiet node lives here, which the announcer already broadcasts.)

## 4. Epochs ride down the same pipe frames ride up

The reply to a knock is `msgEpochs {space, frames[], unix}` on the same
conn, and every later rotation is pushed to it live — no side channel,
no open HTTP, no session. `unix` rides every push and the device treats
it as a floor, never a setback: time only forward (ADR-025). The node
watches its OWN log for the change — a local replay on a timer costs
nothing the wire can see; the conn carries bytes only when the epoch
actually turned.

Weighed and refused:

- **Full sync for instruments** — hands a courier other people's
  freight: every member's frames, the whole log, to a device that needs
  one key and one clock.
- **An open HTTP epoch endpoint** — an oracle of space existence with a
  URL, and a second door to guard forever.
- **A session/subscription protocol** — ADR-011 already decided the
  node's doors are stateless verifiers; the door keeps that: verify the
  knock, scope the link, done.

## 5. Discovery: the device computes the hint itself

A node announces `Hint(space, bucket)` — sha256 over
`"qp-lan-hint-v0:" ‖ terminal ‖ be64(bucket)` truncated to 16 bytes,
buckets of 6 hours, previous bucket honoured for skew. The device holds
its space id from provision (ADR-026) and computes the same hint from
its own clock: `qi_lan_hint` in the C core, pinned byte-for-byte against
vectors Go computed once — the two-implementation law every core
primitive lives under.

Residual, said out loud: a device whose clock lags more than a bucket
window computes hints nobody announces and goes DEAF until a bearer
with time (serial in the chain) feeds it. That is the fail-closed choice
(no time → no emission) extended to discovery, and the chain is the
rescue, not a hidden clock exception.

## 6. A broadcast space changes the freight, not the door

The external enrollment path learned the fork the simulated path always
had: a public space is plaintext — no epoch membership, no instrument
plane, no key to turn. The sensor's device gets an ATTESTED WRITER
BINDING revised into the manifest before the manifest frame it rides in;
detach takes the binding back. The provision — and the door's epochs
answer — carry an EMPTY epoch list for such a space, honestly.

**The named limit:** the C core as shipped emits sealed frames only and
refuses an epochless provision. Enrollment into a broadcast space works
end-to-end today; plaintext EMISSION from a real board is QI-B2's slice,
with its own ctest and interop fixtures. Until then, real-board
telemetry lives in sealed spaces, and no demo claims otherwise.

## 7. The transport verdict (Ф0 spike)

The listener USED to require TLS 1.3. The spike —
`WiFiClientSecure.setInsecure()` against the REAL `lan.NewNode().Listen`
on a Heltec V3 (arduino-espressif32, mbedTLS) — found two walls, and
each moved a decision into this note rather than a hack into the sketch.

**Wall 1 — the board speaks only TLS 1.2.** Its client hello offered
`[1.2 1.1 1.0]`; a 1.3 floor answered with a fatal `protocol_version`
alert (board err −30592). Decision: the listener's floor drops to TLS
1.2. This does NOT downgrade peer-to-peer — crypto/tls negotiates the
HIGHEST both offer, so two nodes still settle on 1.3 (pinned by a test),
and only an instrument lands on 1.2. The version was never the identity
story: the cert is ephemeral and trusted by nobody (InsecureSkipVerify),
identity lives in event signatures (ADR-015), and both 1.2 and 1.3 give
ECDHE forward secrecy plus RFC-5705 exported keying material — which is
all the door's knock needs.

**Wall 2 — the node's cert was too minimal to parse.** With the version
settled, the board's mbedTLS quit with "ASN1 out of data" on the
certificate: the old template carried only a serial and validity dates,
an empty Subject that crypto/tls accepts and a strict embedded parser
rejects — before setInsecure could even skip the trust check. Decision:
the ephemeral cert gains a Subject CN, key usage, basic constraints and
a SAN. Well-formed, not trusted — the trust model is untouched.

With both: the board completed the handshake in ~1.2 s, the server
logged `version=0x0303 cipher=0xc02b` (ECDHE-ECDSA-AES128-GCM-SHA256),
and BOTH ends exported matching keying material. Verdict: the platform
speaks the door's TLS with these two changes to transports/lan, no
downgrade listener and no second port. The handshake cost is paid once
per connection; the chain's 60 s probe cadence keeps a flapping network
from turning it into a treadmill.

## 8. What the bearer is NOT

- Not a member: it holds no conversation key, appears on no member
  card, and its link grants nothing beyond carriage (ADR-025 §2).
- Not the serial stand's replacement: the stand stays in the chain as
  the road that also feeds time when the network cannot.
- Not a router: it never forwards, never gossips addresses, never
  becomes a route candidate for anything but its own space's frames.

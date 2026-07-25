# ADR-015 — Terminal Network: one signed event, many carriers

Status: accepted · Wave: TN (rev 2.1)

## Invariant

> **Space — транспортно-независимый реплицируемый журнал событий с
> membership, policy и encryption. Радиоканалы и сетевые группы являются
> только carriers и никогда не определяют состав Space.**

One Meshtastic channel is one **QUIET carrier** (PortPrivateApp 256)
multiplexing envelopes for MANY spaces. Never `1 space = 1 channel`;
never a Reticulum GROUP or mesh room as a membership source.

## Decisions

### 1. The envelope is the unit; transports carry it verbatim

The signed canonical frame (`protocol/signal`) crosses every carrier
byte-identical. Transport layers may wrap, fragment, or reversibly
re-encode it (TN-2 compact profile) — they may NEVER mutate the signed
bytes. Headers are cleartext (only Payload is epoch-encrypted), so blind
forwarding elements can read priority / expiry / destination / dedup id
without holding any key.

### 2. ExpiresAt = custody expiry, never ingest rejection

Chains are contiguous (`ContiguousUntil`); a hole punched by skipping an
expired frame would stall a recipient forever. Therefore:

- ingest always accepts history (expiry is not deletion — ADR-010);
- CUSTODY HOLDERS enforce expiry: an item's custody TTL is
  `min(operator TTL, ExpiresAt − now)`; expired items are refused or
  swept, so airtime is never spent on them;
- a full-fidelity link (LAN, relay re-push from a source) heals any gap:
  the recipient's summary keeps advertising its pre-gap head and a
  source re-pushes the whole range.

### 3. MaxForwards is a forwarding-scope class, never a hop counter

The field lives inside the signed map — decrementing it would break the
signature by construction. Reinterpretation (API:
`Header.ForwardingScope()`):

- `0` — `CustodyAllowed` (default): may be stored and forwarded;
- `1` — `NoCustody`: custody holders (bridge, relay push) REFUSE custody;
  fit for presence and ephemeral telemetry;
- `≥2` — reserved, unenforced.

A true mutable per-hop budget, if ever needed, belongs in the OUTER
transport frame (unsigned); a header key is reserved in the TN-2 compact
framing for it. Loop prevention does not come from hop counts (below).

### 4. Loop prevention = seen-cache + split-horizon by LinkID/LoopDomain

- Every forwarding element keeps a bounded seen-cache of 16-byte
  EventID prefixes (LRU, TTL ≥ custody TTL, periodic snapshot in the
  `length‖crc32c` segment format). Losing the snapshot costs airtime,
  never correctness — the full content-hash dedup at ingest is the
  final authority.
- Split-horizon is by **LinkID / LoopDomainID**, NOT endpoint class:
  same LinkID → always suppress; same LoopDomain (e.g.
  `meshtastic-quiet@device-1`, `relay-x`) → suppress; same class →
  ALLOWED (radio-A → bridge → radio-B is a legal, useful topology).
- Ingress origin (LinkID + LoopDomain) is persisted in the custody
  record so split-horizon survives a restart.

### 5. Priority lanes get consumers

- `sync.pushMissing`: order INSIDE a device chain is sacred; ACROSS
  chains, ranges are scheduled by the lane of the pending head with
  fairness (max burst per chain/lane, weighted round-robin) so a
  high-priority chain cannot starve the rest forever. Frames always
  precede blobs.
- Router/bridge queues: strict priority with aging, lane read from the
  cleartext header.

### 6. The bridge is blind and space-ignorant

`quiet-bridge` is a standalone daemon that:

- holds NO space/terminal identity, NO epoch keys, NEVER reads payloads —
  expressed as a TYPE (`routing.RoutedOpaqueEnvelope{Route, Ciphertext}`;
  the bridge reads only `Route`) and *enforced* by an import-boundary test
  (no identity/epoch/terminals imports). The distinction matters: the type
  makes the line legible, the test is what actually holds it. Until RB-0B
  the type did not exist at all and blindness rested entirely on the test
  plus discipline;
- does not know the product concept "Space": queues, quotas, bundles and
  subscriptions are **per-destination-hint** (the opaque 32-byte
  destination id / relay hint already visible on the wire);
- DOES hold a local operational **custodian keypair** — not an identity,
  not a member, cannot author events. It signs transport receipts
  `{frame_id, custody_store_id, accepted_at, expires_at,
  bridge_instance}` and ACKs only AFTER durable append + fsync.

**Amendment (RB-0A, D1 — mailbox rendezvous).** Being space-ignorant is not
enough to close the loop: a per-destination hint says WHAT a frame belongs
to, never WHERE its recipients can be reached from. The bridge and the nodes
therefore never met in the same relay mailbox, and the boundary was open in
both directions while every component looked correct in isolation.

A subscription is now an **operator-provisioned routing capability**:
`{network_id, terminal, radio_devices[], internet_devices[]}`. The ids stay
opaque bytes to the bridge — what the operator adds is the one fact a blind
element cannot derive, which side of the boundary each mailbox is reachable
from. With it:

- **uplink** (radio → internet) writes into the ordinary per-recipient inbox
  `HintFor(terminal, internet_device, bucket)`. Nothing changes for the
  internet-side node: it collects its own mail and cannot tell the copy came
  off a radio;
- **downlink** (internet → radio) reads `HintFor(terminal, radio_device,
  bucket)` with a NON-destructive `Fetch`. The mailbox belongs to a node
  that is unreachable, not absent — draining it would mean a node that
  later found internet discovered its own mail already eaten.

A shared per-terminal mailbox was rejected: it would have weakened the
per-device addressing the relay already has and made "who drained this"
ambiguous. Consequences accepted honestly: radio delivery is
**at-least-once** (a crash between broadcast and the durable seen-record
repeats; the reverse order would lose), while APPLICATION of an event is
effectively exactly-once through the stable `EventID`. `LoopDomain` names
the SEGMENT (`meshtastic-quiet@<network_id>`) and `LinkID` the individual
adapter, so two gateways on one carrier still recognise a shared forwarding
domain. Exactly one active downlink gateway per `network_id` in the beta;
a second one costs duplicate airtime, not a loop.

A destination with no internet mailbox is **refused at admission** rather
than taken into custody — holding frames that can never be delivered would
be a promise the bridge cannot keep.

**Amendment (RB-0B — a promise that binds).** The receipt format, the
custodian key and the node-side pin check all shipped in TN-B, and nothing
ever sent one: `AcceptUplink` had no callers outside its own test and
`EncodeCustodyMessage` had none anywhere. A node handing frames to a gateway
heard silence and could not tell carried from lost, so the ladder was stuck
at `handed_to_transport` no matter what happened.

The ACK now goes out on the carrier, and three rules make it worth
believing:

- **After fsync, never before.** Custody is claimed only once `Enqueue` has
  returned, which fsyncs. Not after the first fragment, not after a
  Meshtastic ACK, not after a write to memory.
- **Idempotent.** A repeat of an already-accepted frame is answered again
  with the SAME acceptance time. A fresh timestamp would be a new promise
  for an old frame; silence would make a single lost ACK unrepairable.
- **Ahead of the data queue.** ACKs are sent before the queue drains, so a
  backed-up gateway can still tell a sender it has the message.

**Durable deferrals and the honest guarantee.** Retry scheduling lives in
the custody record and is written to disk, so an ordinary restart does not
replay what was just sent. That is not exactly-once and must not be
described as such: a crash *after* the bytes leave the radio but *before*
the deferral is durable will repeat the send, and so will a crash between a
sent ACK and its record. Both are deliberate — recording first would turn a
crash into a message nobody ever hears. **Radio delivery is at-least-once;
application is effectively exactly-once through the stable `EventID`.**

**A signed ACK outranks the eviction policy.** The queue may drop the oldest
lowest-lane record to make room — but not one whose custody was
acknowledged, because that sender was told it could stop retrying. Capacity
is therefore checked BEFORE the promise is made (`EnqueueGuaranteed`), and
when the only way to fit a new frame is to break an existing promise the new
frame is refused (`ErrNoRoom`): responsibility stays with the sender, who
retries.

When custody genuinely ends — expiry, or a frame that can never cross this
carrier — the gateway sends a signed **withdrawal**
(`CustodyReceipt.Lapsed`). It is an explicit flag, because **"expired" and
"custody withdrawn early" must not be conflated by encoding both as a date
in the past.** Every receipt, withdrawal included, still carries
`ExpiresAt`, and it always reports the horizon the ORIGINAL claim promised.
The two are layered on purpose:

```
Lapsed     → makes the sender retry SOONER
ExpiresAt  → guarantees responsibility cannot hang forever if it is lost
```

A withdrawal is durable and repeated on a bounded schedule: it is the one
message whose loss leaves a sender believing something false. It is
deliberately NOT bounded by the horizon — nearly every withdrawal is raised
*at* expiry, so "repeat only while the horizon is ahead" would silence it
in exactly the case it exists for. It is also the clock-independent half of
the pair: evaluating `ExpiresAt` means trusting a clock, and the sender is
an off-grid radio node — the very device whose clock RB-2 already refuses
to trust for beacon freshness.

A withdrawal does not un-record the earlier receipt: the delivery ladder is
closed and has no rung for "carried, then not", and a receipt that was true
when issued stays true. What changes is what the reader is shown.

**Three size limits, because there were three questions.** `RadioDecodeCap`
(8 KiB) bounds what a parser will look at — a safety limit against hostile
input. `BetaOutboundCap` (~1536 B) bounds what this bridge will put on the
air in one message; at 2000 bytes a minute an 8 KiB message is four minutes
of one node holding the channel. `MaxRadioFragments` (12) bounds how many
packets a message may become, since losing one fragment loses the message.
The outbound cap is measured before the compact profile runs, which only
ever shrinks. All three are calibrated against a real modem preset in RB-3.

**Known limitation (RB-0B): sync summaries disclose an activity graph.**
Checked while hardening the wake-up, and recorded here rather than left as
folklore. A summary — from a node or from a bridge, the wire is identical —
carries the raw 32-byte terminal id and the raw device public key of every
author chain it announces. Content stays encrypted, but a listener on the
segment learns which space is active, which devices author in it, and how
fast each chain grows.

Three things about this are worth stating precisely:

- It is **not new** and not the bridge's doing. Node summaries have carried
  raw ids on the air since M1.7. The bridge's ledger announcement uses the
  same fields, and a test pins that it announces no MORE than a node would:
  exactly the terminal it was provisioned for, exactly the authors it
  actually carried.
- The bridge does make the exposure **more regular** — it announces on a
  schedule where previously only nodes did.
- **TN-2B does not cover it.** The id-table interns ids found inside signed
  frames; `scanIDs` returns nothing for a summary, so summaries take the
  stateless path and the ids go out raw even under the table profile. This
  was assumed to be the mitigation and is not.

Fixing it means changing the summary message itself — keyed or table-scoped
hints in place of raw ids — for every peer on the carrier. That is a
protocol decision for a later gate, not something a gateway can do alone: a
summary a node cannot parse wakes nobody. For the beta the mitigation stays
operational, as in the threat model above — a private channel with its own
PSK, defence in depth, and no claim that the radio segment is unobservable.

**Amendment (RB-0A — the bridge speaks).** A node hands over frames only
when asked: its sync engine answers a summary, it does not volunteer. A
bridge that only listened therefore left every radio-only node mute. The
bridge now announces a summary of **what it has actually carried** (a
ledger of author device + sequence, both cleartext header fields), rate
limited per destination, jittered by custodian key, one outstanding
question at a time, and **never as a reply to a summary** — otherwise two
elements volley until the batteries are flat. This is a transport message
on an existing type, not a new event schema (§8 holds).

### 7. Custody receipts are authenticated by pinned custodian keys

A node records `claims.DeliveryAcceptedByRelay` (existing transport
receipt path; the ladder is closed and NOT extended — the display string
becomes "in custody of relay/bridge") only when:

```
signature valid
AND signer pinned for the ingress LinkID/LoopDomain
AND frame_id matches
AND expires_at valid
```

Pins live in local node config (`custodians: [{link_domain,
public_key}]`). TOFU is forbidden by default; key rotation is an
explicit pin update. Unsigned or unpinned ACKs are local unconfirmed
observations, never `accepted_by_relay`. Raw radio (AckNone) proves
nothing above `handed_to_transport` — honest.

### 8. No new event schemas

- transport routes/reachability are LOCAL PROJECTIONS
  (`kernel/routing/routes.go`) — replicating topology into the space log
  would leak gateways and routes to every member;
- `transport.receipt.v1` as an in-log event is structurally dead: the
  bridge has no membership, its frames would fail verification at every
  ingest — receipts ride the transport ACK path instead;
- the contract registry (LR-0a) is untouched this wave;
- new WIRE formats (compact framing, bridge uplink protocol, custody
  store records) are versioned transport formats outside ADR-009's
  event-schema space.

### 9. Delivery classes and airtime

Radio carriers admit: membership/keys, receipts, presence, text and
block messages, reactions/resonance, observations, asset MANIFESTS.
They refuse: blob request/data sync messages and any frame above the
radio custody cap (8 KiB). Airtime is a per-link token bucket with
aging across priority lanes. Media crosses radio as a reference; bytes
arrive later via a fast path (LAN/relay/bundle).

Membership DoD: until a chunked control package exists, the radio
profile OFFICIALLY supports membership epochs up to the largest group
size that passes the ≤8 KiB test (2/8/32/128 measured); exceeding it is
a documented boundary, not a release blocker.

### 10. Compact profile is reversible and split (TN-2A/TN-2B)

- TN-2A (stateless): DEFLATE-when-smaller + stateless reversible
  framing; magic prefix + version distinguishes compact from raw CBOR
  fragments; real radios default to RAW, `--compact` is operator opt-in
  (a raw-only peer cannot parse compact — fail-closed, no silent
  auto-fallback in 2A).
- TN-2B (stateful id table 32B→2B): table epoch/generation, define ids,
  TABLE_ACK before short indexes (redundant defines on pure-AckNone
  links), receiver-reset detection, periodic redefine; plus the narrow
  link-local codec-version negotiation (`mode=auto`). This is NOT a
  general capability-discovery protocol.
- The signature constraint gates both: byte-exact round-trip →
  `VerifyFrame` passes, or the gate does not ship.

## Out of scope (this wave)

Reticulum (seam reserved: routing scheme name + transports/reticulum),
MeshCore, legacy Meshtastic text-bridge (trusted translator), data-mule
UX (bundle files already cover manual carry), sensor SDK, general
terminal capability discovery/negotiation, BPv7 compliance, full node
migration onto the router (the node keeps adoptLink + an optional
`FrameMeta` filter), new event schemas, pass `routes` field.

## Consequences

One signed event crosses internet, LAN and LoRa without changing its
author, meaning or cryptographic integrity. Bridges know the necessary
minimum and can prove custody without ever owning a space. Airtime is
spent only on live, admitted, deduplicated traffic.

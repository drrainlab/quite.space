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

- holds NO space/terminal identity, NO epoch keys, NEVER reads payloads
  — enforced by TYPES (`OpaqueEnvelope{SignedPublicHeader,
  EncryptedBytes}`; no decrypt capability exists in its API) plus an
  import-boundary test (no identity/epoch/terminals imports);
- does not know the product concept "Space": queues, quotas, bundles and
  subscriptions are **per-destination-hint** (the opaque 32-byte
  destination id / relay hint already visible on the wire);
- DOES hold a local operational **custodian keypair** — not an identity,
  not a member, cannot author events. It signs transport receipts
  `{frame_id, custody_store_id, accepted_at, expires_at,
  bridge_instance}` and ACKs only AFTER durable append + fsync.

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

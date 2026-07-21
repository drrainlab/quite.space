# ADR-006: Local storage

- Status: accepted
- Date: 2026-07-22
- Relates to: engineering plan §16, §21; vision §3.1

## Context

Every device is a full node (local-first). Storage must survive restarts and
crashes, keep the event log authoritative, make materialized state disposable,
and hold keys safely — on laptops and on a Raspberry Pi.

## Decision

### Layout

One data root per Node:

```
<data-root>/
├── events/<terminal_id>/NNNNNN.seg   # append-only segments: source of truth
├── state.db                          # SQLite: indexes + materialized state
├── blobs/sha256/<xx>/<hex>           # content-addressed attachments
├── keys/keystore.enc                 # encrypted private keys
└── outbox/                           # queued frames per transport, TTL'd
```

### Event segments — the source of truth

- Segments store **received canonical frames verbatim** (ADR-003), each
  record: `length ‖ crc32c ‖ frame-bytes`. Append is the only mutation;
  fsync after each append batch.
- A torn tail record (crash mid-write) is detected by length/CRC and
  truncated on open; anything before it is intact.
- Segments roll at 64 MiB. Pruning (ADR-010) rewrites a segment once,
  atomically (write new + rename).

### SQLite — always derived

- `state.db` holds chain indexes, dedup sets, materialized Block state, sync
  summaries, and claim/trust tables — **all rebuildable** by replaying
  segments through the reducers. Deleting `state.db` must reproduce identical
  state (M0.4 acceptance); a `terminal-node --rebuild` path exercises this in
  CI.
- WAL mode, foreign keys on, one writer (the kernel), readers via snapshots.

### Blobs

- Attachments are content-addressed by SHA-256 and stored once; envelopes
  reference blobs by hash. Blob presence is per-node; missing blobs are a
  normal state the UI must show honestly (invariant §2.4).

### Keys and encryption at rest

- Private keys live only in `keys/keystore.enc`: scrypt passphrase KDF +
  XChaCha20-Poly1305. Where an OS keychain exists (macOS Keychain, Secret
  Service), the keystore wrapping key is stored there and the passphrase is
  optional; headless nodes use passphrase or an explicit unattended-key file
  with a stated risk.
- Event payloads are already E2E-ciphertext on disk (ADR-005). Full
  database-at-rest encryption is deferred; sensitive *cleartext* fields
  (profile, petnames, local labels) are application-encrypted with the
  keystore key before hitting SQLite.

### Retention

The Retention Engine enforces per-Terminal policies by pruning payloads
(ADR-010) and expiring `outbox/` and relay items by TTL. Retention never
touches chain metadata needed for sync correctness.

## Consequences

- Crash safety reduces to "segments are append-only + atomic rename"; SQLite
  corruption is never data loss, just a rebuild.
- Storing frames verbatim costs some disk versus columnar storage, but keeps
  signatures re-verifiable and sync trivially correct — the right trade for a
  protocol whose core claim is a verifiable log.
- SQLite and segment files work identically on desktop and Raspberry Pi
  (plan §21); no server database exists anywhere.

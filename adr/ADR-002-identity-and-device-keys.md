# ADR-002: Identity and device keys

- Status: accepted
- Date: 2026-07-22
- Relates to: engineering plan §3, §14; vision §4.1–4.2

## Context

Identity must be created locally, work offline, require no registration, and
support multiple devices per Principal with revocation of a lost device
(vision §4.1–4.2). There is no central account to recover from, so key custody
and recovery must be explicit.

## Decision

### Algorithms and identifiers

- All identity keys are **Ed25519** (RFC 8032, "pure" Ed25519, no pre-hash).
- `PrincipalID`, `DeviceID`, `TerminalID` are each the raw 32-byte Ed25519
  public key of the corresponding keypair. No usernames, no registries.
- **Fingerprint** = SHA-256 of the public key, displayed as ten groups of four
  hex characters (40 hex chars shown). Manual fingerprint comparison is the
  MVP trust ceremony.

### Key hierarchy

```
Principal root key  (rare use: certify / revoke / recover)
  └── certifies → Device keys   (daily use: sign every Signal)
  └── countersigns → Terminal genesis manifests (ADR-001)
```

- The Principal root key signs a **device certificate**: a record binding
  `device_public_key` (Ed25519), the device's **X25519 public key** (for key
  wrapping, ADR-005), `principal_id`, `issued_at` (logical time), and an
  optional expiry. Certificates travel in the event log so peers can verify
  any device's authority offline.
- Every Signal is signed by a **device key** (never by the root key). The
  envelope carries both `principal_id` and `device_id`; verification requires
  a valid, unrevoked certificate chain of exactly depth 1 (root → device).
- The root key is used only for: certifying devices, revoking devices,
  countersigning terminal genesis manifests, and producing recovery bundles.
  Clients keep it encrypted at rest and never load it for routine signing.

### Revocation

- A **revocation record** is signed by the root key: `{principal_id,
  device_id, revoked_at_logical}`. It propagates through the event log like
  any Signal, in the highest priority lane (plan §17.4).
- On seeing a revocation, a node refuses every Signal that ARRIVES from
  that device thereafter — at any `logical_clock` the frame claims.
  History is still not retroactively broken: everything a log already
  holds stands, and replays as valid forever. The distinction is
  arrival, not the stamp.

  *(Amended 2026-08-24, v0.1.6.)* The original rule compared the frame's
  `logical_clock` against `revoked_at_logical` and admitted the earlier
  ones as history. But the clock is written by the frame's author, and a
  revoked device is precisely the author whose claims stopped being
  trustworthy: a stolen device's clock naturally lags the authority's,
  so its next message — stamped with its own honest-looking low clock —
  filed itself under history and was admitted. A genuinely old unheld
  frame and that forgery are the same bytes; no rule can tell them
  apart, so admission errs on the side decision 3 already chose: the
  device stops speaking at once. The accepted cost is that a
  pre-revocation frame a replica never held cannot be backfilled to it
  after the revocation is known.
- Revocation is eventually consistent by nature; UI must present device trust
  as a claim with an origin, never as global truth (ADR-008).

### Recovery

- **Recovery bundle** = encrypted export of the principal root key plus the
  device certificate list: scrypt passphrase KDF + XChaCha20-Poly1305.
- Losing all devices and the recovery bundle means the identity is
  unrecoverable. This is stated honestly in onboarding (vision §13); no
  backdoor exists. Social recovery is out of scope until post-MVP.

### Profiles

Display name, avatar, and bio are a local, optional profile shared only with
chosen peers (vision §4.1). Peers keep local petnames; nothing about naming is
global or unique.

## Consequences

- Two independent implementations must verify the same golden test vectors
  (M0.2 acceptance): key generation, certificates, signatures, revocations,
  recovery bundles all get vectors in `testvectors/`.
- Compromise of a device key is contained by revocation; compromise of the
  root key is fatal for the identity — mitigated by rare use and encrypted
  storage, and stated in the threat model.
- Depth-1 certification keeps verification trivial; delegated sub-keys or
  threshold custody would need a new ADR.

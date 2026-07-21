# ADR-005: Group encryption

- Status: accepted
- Date: 2026-07-22
- Relates to: engineering plan §14; vision §13 (group crypto risk)

## Context

Private Terminal payloads must be unreadable to relays, transports, and the
operator. Membership changes (especially removals) require key rotation.
Full MLS is too heavy for MVP, but the design must not paint us out of
adopting it later (plan §14).

## Decision

### MVP profile (epoch keys)

- Each private Terminal has a symmetric **epoch key**: 32 random bytes.
  Epochs are numbered from 1 and recorded in the event log.
- Envelope payloads are encrypted with **XChaCha20-Poly1305** using the
  current epoch key and a random 24-byte nonce. The AEAD associated data
  binds `terminal_id`, `epoch`, and the payload schema id, so ciphertexts
  cannot be replayed across terminals, epochs, or schemas.
- Envelope *header* fields (authorship, sequence, routing) remain cleartext
  to participants and relays — this metadata exposure is documented in the
  threat model, not hidden.

### Key distribution

- On every membership change (join, leave, removal, device revocation) the
  controller emits a **new epoch**: a key-distribution event in priority
  lane 1 (plan §17.4) containing the new epoch key wrapped separately for
  every member device.
- Wrapping uses **HPKE** (RFC 9180), base mode,
  `DHKEM(X25519, HKDF-SHA256) + HKDF-SHA256 + ChaCha20-Poly1305`, to each
  device's X25519 key (derived key, distributed in the device certificate,
  ADR-002).
- Removed members do not receive the new epoch and cannot read subsequent
  events. They retain everything already decrypted — the UI says so
  (invariant §2.4).

### Boundaries, honestly stated

- No per-message ratchet in MVP: compromise of an epoch key exposes that
  epoch's messages (bounded forward secrecy at epoch granularity, no
  post-compromise security until rotation). This is written into the threat
  model and never marketed away.
- MVP group size cap: **32 devices** per private Terminal — keeps every
  rotation event one small frame and defers fan-out optimizations.

### Implementation rules

- No custom cryptographic constructions (plan §14). Primitives come from
  established Go libraries (`golang.org/x/crypto`, `cloudflare/circl` for
  HPKE); all operations sit behind the `CryptoProvider` interface.
- Test vectors for encrypt/decrypt/wrap/rotate live in `testvectors/`.

### Post-MVP path

`CryptoProvider` exposes `WrapGroupKey` / `RotateEpoch` abstractly so an MLS
provider can replace the epoch scheme per Terminal without touching the event
log model. Adopting MLS will be its own ADR superseding this section.

## Consequences

- Blind relays and transports see only headers + ciphertext (Demo D holds by
  construction).
- Every membership change costs one rotation event of O(members) wrapped
  keys; acceptable under the 32-device cap.
- Epoch state is reducible from the log, so a restored-from-log node can
  decrypt exactly what its device keys entitle it to.

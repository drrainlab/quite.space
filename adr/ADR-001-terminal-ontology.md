# ADR-001: Terminal ontology

- Status: accepted
- Date: 2026-07-22
- Relates to: engineering plan §1, §3, §5; vision §4

## Context

The protocol needs one addressing and interaction model that covers people,
shared spaces, bots, AI agents, sensors, actuators, gateways, relays, and
archives. Building separate architectures for "users", "groups", and "devices"
would triple the protocol surface and leak assumptions (e.g. "a sender is a
human account") into every layer.

## Decision

At the protocol level, a **Terminal** is any cryptographically addressable
entity that can participate in Signal exchange. A shared space is not a
special case — it is one kind of Terminal.

### Canonical glossary

This table is the single source of definitions. Other ADRs and specs reference
these terms and MUST NOT redefine them.

| Entity | Definition |
|---|---|
| **Principal** | The subject that controls keys: a person, organization, device owner, software agent, local group, or anonymous identity. Identified by its root public key. |
| **Device** | One concrete hardware or application instance controlled by a Principal. Holds its own key, certified by the Principal. The only entity that signs Signals. |
| **Terminal** | An addressable interface controlled by a Principal (or group policy). Identified by a dedicated keypair. Publishes a signed Manifest describing its interaction contract. |
| **Space Terminal** | A Terminal with multiple participants, a shared event log, membership epochs, policies, and Blocks. |
| **Node** | A running instance of the Terminal Mesh Kernel. One Node may host many Terminals. Nodes are infrastructure, not identity. |
| **Signal** | The universal signed event (envelope v0, plan §12). Everything that happens is a Signal. |
| **Manifest** | A Terminal's signed, versioned declaration of kind, labels, io, agency, storage, and security properties (plan §4). |
| **Capability** | An atomic, explicitly granted permission (`signal.publish`, `command.execute`, …). The only source of "what is allowed". |
| **Claim** | A statement with an explicit origin and confidence level (plan §8). Claims are not facts. |
| **Block** | An interactive projection of Terminal data in a client (chat, objects, telemetry, …). Blocks live above the protocol; they never appear on the wire. |

### Terminal kinds

`kind.primary` is a closed enum in v0:

```
human | space | bot | agent | sensor | actuator | gateway | relay | archive
```

`kind.primary` is a **UI projection hint only**. It grants nothing, proves
nothing, and is never consulted by the Capability Engine (invariant §2.2:
capabilities before assumptions).

### Identity relationships

- A Terminal has its own Ed25519 keypair; **TerminalID = the terminal public
  key**. Possession of the terminal private key is what "control" means.
- The genesis Manifest binds the Terminal to its controller Principal: it is
  signed by the terminal key and countersigned by the controller (via a
  certified device key, ADR-002).
- One Principal may control many Terminals; one Node may host Terminals of
  many Principals; these axes are independent.

## Consequences

- No "user account" concept exists anywhere in the protocol. Humans, sensors,
  and relays traverse identical code paths; differences are expressed only
  through manifests and capabilities.
- A source-only sensor cannot be messaged and an actuator cannot be commanded
  without an explicit capability — enforced structurally, not by UI.
- Group-policy control of a Space Terminal (multi-signature custody of the
  terminal key) is deferred; in MVP the creating Principal is the controller.
- Every later ADR that mentions Principal/Device/Terminal/Node refers to this
  glossary; a conflicting definition is a defect (M0.0 acceptance).

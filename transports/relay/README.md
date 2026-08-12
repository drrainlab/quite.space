# transports/relay — Apache-2.0

T3 — Blind Relay transport: the wire protocol and the client half.

`wire.go` is the protocol: message types, hint and capability derivation.
`client.go` is `Client` and the refusal vocabulary both sides share.

Apache-2.0 deliberately, and the message-type constants are exported for the
same reason: a client needs this, and so does anyone writing an independent
relay.

**The server lives next door in `transports/relayserver`, under AGPL-3.0-only.**
A Go package compiles as a unit, so they cannot share one — see
[`/LICENSING.md`](../../LICENSING.md).

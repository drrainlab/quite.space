# transports/relayserver — AGPL-3.0-only

The relay SERVER: `Store`, `Server`, `ServerLimits`, the connection handler.
A process an operator stands up so that other people's devices can leave
sealed items for each other.

Free to run, to modify, and to charge for hosting. Offer a modified version to
users over a network and those users can have that version's source.

The wire protocol and the client are Apache-2.0, next door in
`transports/relay`. The reasoning is in [`/LICENSING.md`](../../LICENSING.md);
the short version is that a Go package compiles as a unit, so a package
holding both halves would make one of the two licences a fiction.

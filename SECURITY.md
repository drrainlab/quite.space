# Security

## Reporting a vulnerability

**Please do not open a public issue.**

Report privately through GitHub's *Report a vulnerability* (Security →
Advisories) on this repository, which creates a private thread.

Please include what you need to make it real: what an attacker can do,
what they need in order to do it, and — ideally — the smallest thing that
demonstrates it. A failing test in the shape of the ones in this tree is
the most useful form a report can take.

We will acknowledge, tell you honestly whether it reproduces, and keep
you in the loop while it is fixed. If you would like credit, say so; if
you would rather not be named, that is fine too.

## What is in scope

The protocol and everything that carries it: the event log and its
verification, the envelope and signature paths, group encryption and
epoch handling, the pass and quicklink flows, relay and bridge behaviour,
the radio transfer layer, the local HTTP API, and the client.

Some things we already know and would rather hear about only if you can
show they are worse than we think — see below.

## Known limits, stated up front

This is a pre-beta project. A few properties are deliberate, and are not
vulnerabilities:

- **A relay is blind, not trusted.** It cannot read private content, but
  it does see timing, sizes and mailbox activity. Traffic analysis
  against a relay is a real capability and not a bug report.
- **Public spaces are signed plaintext by design.** Confidentiality is a
  property of private spaces.
- **Display names and forwarded attributions are claims, not facts.**
  Where the interface says somebody *says* a message came from a person,
  that wording is the security property, not sloppy copy.
- **Radio delivery is unconfirmed, not guaranteed.** "Could not confirm"
  means exactly that: the peer may already hold the message.

If any of those turns out to leak more than stated, that *is* a report we
want.

## Our own audit

`docs/SECURITY_AUDIT_BETA.md` records a read-only review carried out
before the beta: what was examined, what was found, and — importantly —
which areas were **not** covered, because an absent finding in an
unaudited area is not a clean result. If you are looking for somewhere to
start, the unaudited list at the top of that document is an honest map of
where the thin ice is.

## Supported versions

Until the first release there are no supported versions and no backports:
the fix goes to `main`. This section will be replaced when releases
begin.

## Cryptography

The primitives are standard and are not rolled by hand — Ed25519,
X25519/HPKE, XChaCha20-Poly1305, SHA-256, scrypt for passphrase
derivation. If you find a place where the project has invented its own
construction, that is worth reporting on its own.

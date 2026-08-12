# Licensing

> **The protocol belongs to everyone. Improvements to shared network
> infrastructure come back to the people using it. The official brand and
> the trusted network stay with quite.space.**

Two licences, on purpose, with a line between them that is about *what a
piece of code does*, not about who wrote it.

| | licence | what it covers |
|---|---|---|
| **The protocol and everything a client needs** | Apache-2.0 | specification, event formats and schemas, crypto and transport interfaces, the Terminal interface, SDKs, reference implementations, interop tooling, examples, the local-first kernel, the clients |
| **Network services somebody runs for other people** | AGPL-3.0-only | the relay server, and any future registry, catalog, moderation or control-plane service |
| **Names, logos and official-network marks** | neither — see [TRADEMARK_POLICY.md](TRADEMARK_POLICY.md) | quite.space, Quiet Spaces, Terminal Network, the logos, Official Relay / Verified Space marks |

`LICENSE` at the repository root is Apache-2.0, and that is the default
for every file. A directory that is AGPL says so with its own `LICENSE`
file and is listed below. Full texts of both live in `licenses/`.

## Why the line is drawn here

Apache-2.0 on the protocol is the point, not a loophole. A company
running a relay for its staff, a hardware vendor putting Terminal Network
on a device, a university deploying its own infrastructure without
negotiating with anybody, an independent client — each of those *grows the
network*, and none of them owes us anything for it. The explicit patent
grant matters more for a protocol than for an application.

The problem was never a corporation using the code. It is a corporation
taking shared infrastructure, improving it behind closed doors, and
selling it back as a network service while returning nothing. AGPL-3.0
§13 answers exactly that and nothing more: modify the relay, offer it to
users over a network, and those users are entitled to *that version's*
source. Running it unmodified, running it internally, charging to host
it, modifying it for yourself — all still free.

AGPL reaches the covered program and its derivatives. It does not reach a
company's Kubernetes, billing or CRM. That boundedness is why it is used
here and SSPL is not; SSPL reaches for a far wider service estate and is
not OSI-approved.

Neither licence discriminates by field of endeavour, so both sides remain
open source in the OSI sense. Refusing business use would have ended that.

## The map

**Apache-2.0** — everything not listed as AGPL below, notably:

```
protocol/     the wire: codec, signal envelope, manifests, publications,
              schemas, quicklink, projections, capability, claims
kernel/       event log, storage, sync engine, reducers, assets, routing, trust
terminals/    participants, policy, projection, human/agent/space templates
transports/   lan, meshtastic, rnode, radiotransfer, compact, bundle, bridge
              relay/  the relay WIRE PROTOCOL and CLIENT (see the split below)
node/         the local-first node runtime
clients/      the web UI
attention/ blocks/ specs/ testvectors/ examples/ android/
cmd/          terminal, terminal-node, terminal-inspect, quiet-radio,
              and the measurement harnesses
```

**AGPL-3.0-only** — a service you stand up so that *other people* can use
it:

```
transports/relayserver/ the relay SERVER — Store, Server, ServerLimits, the
                        connection handler
cmd/terminal-relay/     the blind relay server's main
cmd/quiet-bridge/       the operator-provisioned custody bridge
```

Two categories from the intended design have **no code yet**, and are
listed so nobody has to re-derive the answer when they appear: a relay
bootstrap registry (today a compiled-in list on the client, not a
service) and the public catalog (today an ordinary broadcast space, not a
server). When either becomes something an operator runs, it is AGPL.

## The one prerequisite — done before the first push

**The relay server and the relay client used to be the same Go package, and
the licence split could not hold until they were separated.** They are now
separated, and this section records what was done and why, because the
reasoning is the part worth keeping.

A Go package compiles as a unit, so whichever licence covers a directory
covers everything a consumer links. While `transports/relay` held both
halves, either choice was wrong:

- mark it **Apache** and the relay server is Apache, so a modified closed
  public relay is permitted — the AGPL side would be a fiction;
- mark it **AGPL** and `node/` becomes an AGPL derivative, which makes the
  desktop and Android clients AGPL too — the opposite of the intent.

The seam was clean, so the fix was a package split rather than a redesign:

```
transports/relay        Apache-2.0
  wire.go               the wire protocol, hint and capability derivation
  client.go             Client · DialClient · DialClientPinned · Probe
                        and the refusal vocabulary both sides share

transports/relayserver  AGPL-3.0-only
  store.go              Store, the in-memory item store
  server.go             Server · StartServer · ServerLimits · connState
                        · serve · handle
```

Nothing about the wire format, the trust model or the behaviour changed.
**Exactly two non-test files** changed an import — `cmd/terminal-relay/main.go`
and `cmd/terminal/demo.go` — which is what "the seam is clean" meant. The
message-type constants became exported in the same commit, and that is not
incidental: they are the first thing an independent relay implementer needs,
and Apache-2.0 on the wire package is precisely the promise that they may
have them.

**One consequence, stated rather than discovered later.** Test fixtures in
Apache-licensed packages — `node/`, `terminals/`, `transports/bridge` — stand
up a real relay, so they import the AGPL server. Running this repository's own
test suite is not distribution of a modified relay, and a downstream that
imports the library never links the server at all: `go build` of an Apache
package pulls no test dependency. Anybody redistributing a modified
`transports/relayserver` is in AGPL territory, which is the intent.

## Nothing has been granted yet

This repository has **no git remote and has never been pushed**. That is
the best possible position to be making this decision from: no rights
have been granted to anyone, and every option is still open.

It will not stay that way. Rights granted under Apache-2.0 are perpetual
and irrevocable while its terms are met. A future version can be released
differently if we hold the necessary copyright — but whatever is
published under Apache-2.0 stays available under Apache-2.0, to fork, for
good. So the file that goes out in the first push is the decision, and
the split above has to happen before it, not after.

## Contributions and the second door

The AGPL components are intended to carry a commercial licence alongside
them, for a company that would rather keep its modifications closed than
publish them. Offering that second door requires holding the rights to
relicense every contribution, and a DCO does not provide it: DCO 1.1 is
the contributor's assertion about the *origin* and *provenance* of what
they wrote, not a grant letting the project ship it under other terms.
That is what [CLA.md](CLA.md) is for, and why it applies to the AGPL
components in particular.

## Third-party code

Every dependency is permissive; nothing copyleft is linked in, and both
sides of the split are clean:

| dependency | licence |
|---|---|
| github.com/cloudflare/circl | BSD-3-Clause |
| github.com/skip2/go-qrcode | MIT |
| go.bug.st/serial | BSD-3-Clause |
| golang.org/x/crypto, x/text, x/sys | BSD-3-Clause |

Attribution is in [NOTICE](NOTICE).

## Status of this document

This is the architecture of the decision and the map of the repository.
The licence texts are the canonical ones, fetched from the Apache
Software Foundation and the SPDX license list rather than reproduced from
memory. **The licensing texts, the CLA and the trademark policy should be
read once by a lawyer who works on open source and IP before the first
public release.** Nothing here is legal advice.

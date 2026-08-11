# Contributor License Agreement

**Draft. Not yet in force, and not yet reviewed by a lawyer — see the end
of this file.**

## Why this exists, in plain words

The network-service components of Quiet Spaces are AGPL-3.0-only. The
intent is that a company may run them, modify them and charge to host
them — and that if it offers a *modified* version to users over a
network, those users can get that version's source.

Alongside that, the project intends to offer a **commercial licence** to
an organisation that would rather keep its modifications closed. That
second door is what makes the AGPL choice fair rather than merely
restrictive: contribute your network improvements back, or pay to keep
them private.

Offering that door requires the project to hold the right to license the
code under terms other than AGPL. A DCO does not provide it. The
[Developer Certificate of Origin 1.1](https://developercertificate.org/)
is your assertion about the **origin** of what you wrote — that it is
yours to submit — and nothing more. It grants the project no right to
relicense your work.

So: **DCO on everything, and this CLA additionally on contributions to
the AGPL components.** The Apache-2.0 parts need only the DCO, because
Apache-2.0 already grants what redistribution requires.

## What you would be agreeing to

Stated as intent, in advance of the reviewed text:

1. **You keep your copyright.** This is a licence to the project, not an
   assignment. Your work stays yours and you may use it however you like,
   including in other projects.
2. **You grant the project the right to license your contribution under
   the project's licences, including commercial terms.** This is the
   clause that makes the second door possible.
3. **You grant a patent licence** on the same terms as Apache-2.0 §3, so
   that a contribution cannot become a patent trap later.
4. **You confirm you have the right to do so** — that the work is yours,
   or that you have your employer's permission. If you contribute in the
   course of employment, your employer may need to sign as well.
5. **No warranty is implied**, and you are not on the hook for how the
   project uses the work.

Nothing in the agreement lets the project take the AGPL components
closed. The published AGPL releases stay AGPL and remain forkable —
rights already granted are not retractable. The CLA is what allows an
*additional* licence to be offered, not a way to withdraw the first.

## Which contributions need it

Determined by where the code lands, per [LICENSING.md](LICENSING.md):

| | DCO | CLA |
|---|---|---|
| Apache-2.0 components — protocol, kernel, transports, node, clients, SDKs | required | not required |
| AGPL-3.0-only components — relay server, bridge, future network services | required | required |
| Documentation, tests, tooling in the Apache tree | required | not required |

If you are unsure which side something is on, ask in the pull request.
Nobody is expected to have read the map before writing a patch.

## How it will work

Not yet decided. The likely shape is a bot that checks for a signature on
first contribution and records it, so it happens once rather than per
pull request. Until that exists, **the CLA is not being enforced** and no
one is being asked to sign anything.

## Status

This document is a statement of intent so that anyone deciding whether to
contribute can see the terms in advance rather than meeting them at merge
time. It is **not the agreement itself**. The binding text, and the
question of whether a CLA or a lighter instrument is the right tool at
all, should be settled with a lawyer who works on open source and IP
before the first public release — together with the licence texts and the
[trademark policy](TRADEMARK_POLICY.md).

This is not legal advice.

# Contributing

Thank you for being here. A few things worth knowing before you spend
your time.

## The shape of the project

Quiet Spaces is local-first and decentralized: there is no server that
owns anybody's data, and most of the difficulty lives in what happens
when devices disagree, go offline, or meet each other over a radio. If
you are looking for where the interesting problems are, they are in
`kernel/sync`, `transports/` and `node/`.

Two documents are worth reading first:

- [LICENSING.md](LICENSING.md) — which parts are Apache-2.0, which are
  AGPL-3.0-only, and why the line is where it is
- `adr/` — the architecture decision records. Most "why on earth is it
  done like this" questions are answered there, usually with the
  measurement that settled it.

## Licensing your contribution

Which licence applies depends on **where** the code lands, not on who
wrote it. See [LICENSING.md](LICENSING.md) for the map.

Every contribution needs a **DCO sign-off**: add a `Signed-off-by` line
to each commit, which `git commit -s` does for you.

```
Signed-off-by: Your Name <your.email@example.com>
```

That line is the [Developer Certificate of Origin 1.1](https://developercertificate.org/):
you are asserting that you wrote the code, or otherwise have the right to
submit it under the project's licence.

Contributions to the **AGPL components** additionally need the
[CLA](CLA.md). The DCO is about where code came from; the CLA is about
whether the project may offer that code under a second, commercial
licence. A DCO alone does not carry that right, and the commercial door
for network operators only exists if every line behind it can go through
it. This is stated up front rather than discovered later.

## How the work is done here

The tree has strong conventions. Matching them matters more than being
clever.

**Comments explain why, not what.** The codebase is unusually heavily
commented, and almost all of it is reasoning: the alternative that was
tried and lost, the measurement that decided a constant, the bug a guard
exists to prevent. If you change something that a comment explains, the
comment is part of the change.

**Numbers come from measurement, not from taste.** A timeout, a budget or
a window that is not backed by a run should say so where it is defined.
`cmd/relay-load` and the `*-baseline` harnesses exist for exactly this,
and their own rule is worth repeating: *target numbers are fixed after
running this on the real hardware, not invented in a plan.*

**Tests go red first.** For a bug fix, write the test that fails against
the current code and say so in the commit. Several tests in this tree
carry a note that they were red-proofed by stashing the fix, because a
test that never failed proves nothing.

**Honesty in the interface is a hard rule.** The UI may never claim more
than the code knows. "Accepted by relay" is not "delivered"; "presence
unknown" is not "offline"; a message waiting for a wider path has not
failed. If you find a place where the interface overstates what happened,
that is a bug worth reporting even without a fix.

## Before you open a pull request

```bash
go build ./... && go vet ./... && go test ./...
go test -race ./node/ ./transports/... ./kernel/...
node scripts/i18n/catalogs.cjs          # if you touched user-facing strings
node --check clients/web-ui/assets/*.js # if you touched the client
```

`./node/` under `-race` needs a generous timeout — `-timeout 25m`.

If you added or changed a user-facing string, it goes through `t()` and
needs an entry in **both** catalogs; the i18n check will tell you if it
does not.

## Commit messages

Say what changed and why it is right, in prose. The git history here is
used as an engineering record — several commits are the only place a
particular trade-off is written down — so a message that explains the
reasoning is worth more than one that lists files.

## Reporting a security issue

Do not open a public issue. See [SECURITY.md](SECURITY.md).

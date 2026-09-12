# Known flakes

A test that failed once and was not written down is a test that failed
for nothing. `scripts/flake-hunt.sh` exists to catch the next occurrence
with its output; this file is where the ones we have already seen live,
so the next person to meet one starts from evidence instead of a memory.

An entry stays until the cause is understood — not until the test goes
green again, which it does on its own by definition.

---

## `node.TestArrivalNeverMintsAPeerRoute` — under `-race`, under load

**Seen:** 2026-08-28, preparing 1.0.0-beta.1. Twice within an hour, both
times while the machine was also running a full suite and a container.
Again the same evening, in the SP-3.2 `-race` run of ./node — same
assertion, same 3-provenance route, again under full-suite load.

**Output, verbatim:**

```
--- FAIL: TestArrivalNeverMintsAPeerRoute (2.33s)
    routes_test.go:106: an arrival minted a 3-provenance route:
    {Endpoint:127.0.0.1:56729 Transport:relay Provenance:3
     LearnedAt:1787916480 LastSeen:1787916480}
```

No `WARNING: DATA RACE` anywhere in the run — this is a plain assertion
failure that `-race`'s slowdown makes reachable.

**What the test guards.** Alice must learn nothing about where Bob
listens merely because his frame arrived. Provenance 3 is
`RouteAdvertised`: a route Bob himself STATED (bundle key 8, recorded by
`recordStatedReturnRoutes`). So the question the flake asks is a real
one — did an arrival teach it, which would be the invariant broken, or
did Bob legitimately state his ingress a moment later than the assertion
expected, which would make the test's window too tight?

**What is known:**

- Reproduced alone under `-race`, so it is not suite interaction.
- 10 consecutive runs pass on both `v0.1.7` and current `main` when the
  machine is idle. It is load-sensitive, and its rate is low enough that
  ten green runs prove nothing.
- `git bisect` over the 1.0 waves named a commit touching only
  `release.yml` and a gradle file — a commit that cannot affect a Go
  test. That is bisect doing what bisect does with a flaky predicate,
  and it is recorded here so nobody repeats the search believing it.

**What has NOT been done:** deciding which of the two readings above is
true. That needs the emit path traced with timestamps, not more runs.

**Why it did not block 1.0.0-beta.1:** the invariant it guards is about
what a node BELIEVES, and the release's CI gate was green on the same
code. If the answer turns out to be "an arrival taught it", that is a
correctness bug in the routing trust model and should be treated as one
the day it is confirmed — this note exists so that day starts with the
output above rather than with somebody's recollection.

---

## `node.TestTheWaveEndToEnd` — DATA RACE on the keystore maps, seen once

**Seen:** 2026-08-29, CI `race` job on 54025b7 (a commit touching only
web-ui JavaScript — the race is in code that predates it by many waves,
and beta.1 shipped with it).

**The captured half, verbatim (annotations truncate the rest):**

```
WARNING: DATA RACE
Write at 0x00c000d0e8a0 by goroutine 26658:
  runtime.mapassign()
  github.com/drrainlab/quiet_places/node.(*Runtime).persistEpochsLocked()
      node/node.go:1014
--- FAIL: TestTheWaveEndToEnd (5.23s)
```

Line 1014 is `r.ks.Spaces[tid] = meta`. The OTHER goroutine's stack —
the half that would name the bug — was lost: check-run annotations carry
single lines, and the full log needs admin auth the day someone has it.

**What was audited, so nobody re-walks it:** every persistEpochsLocked
caller holds r.mu (three via defer, one — the sync engine's OnApplied —
fires only under eng.Handle, which lan.go calls locked). By eye, the
likelier suspects were each checked and hold the lock too:
recordStatedReturnRoutes (its saveKeystore is inside the lock despite
sitting next to a "runs without r.mu" comment about a DIFFERENT block),
EnsureAISpace, the join-saga commits, the knock paths. The unlocked
accessor is therefore somewhere in the ~84 `r.ks.*` touchpoints not
exercised by inspection — or in a reader that iterates the maps during
SaveKeystore.

**Reproduction attempts:** 15 runs GOMAXPROCS=2 local + 30 runs under
`docker --cpus=2` with -race — all green. The window needs the CI
runner's exact starvation.

**What has NOT been done:** capturing the second stack. The next
occurrence should be harvested from the FULL job log immediately
(annotations will truncate it again). A data race in the keystore maps
is at worst a concurrent-map panic — rare, but real when confirmed, and
this entry exists so that day starts from the stack above.

---

## `node.TestInspectingADirectoryListsItsCardsAndPersistsNothing` — stale projection at the relay, under `-race` — UNDERSTOOD AND FIXED

**Seen:** 2026-09-12, CI `race` job on 3e4d115 (a commit touching only
`tools/typeface`, web-ui fonts/CSS and docs; `node/` unchanged since
v1.0.9, whose race job was green). The non-race `test` job passed on the
same commit. The package took 867 s under `-race` on the runner — the
whole suite runs packages in parallel on a shared 4-vCPU box, so a
goroutine there can lose the scheduler for tens of milliseconds at a
time. Locally `-race -count=3` passes in ~7 s, and 20 runs with
`-cpu=1` never reproduced it.

**Output, verbatim (what the annotation kept):**

```
--- FAIL: TestInspectingADirectoryListsItsCardsAndPersistsNothing (1.11s)
```

The assertion line was not captured. From the mechanism below it can
only have been the listing check — `the listing is wrong: 1 cards,
total 1, truncated false` — since every other assertion in the test is
about state the failing path never touches.

**What the test guards.** Bob inspects a directory that alice published
with two space-cards, and his node keeps nothing. The listing must carry
both cards.

**Cause — found by forcing the interleaving, not by re-running.** There
is no fixed deadline, single poll or heartbeat cadence in the test; the
publishes are synchronous and the read is synchronous. The timing lived
in the PUBLISHER. `PublishDocument` in an owned public space fires a
background republish of the projection (the per-post nudge, RR-4), and
`publishPublicProjectionForce` held `r.mu` only to BUILD the projection,
then wrote it to the relay unlocked. The relay's `Replace` is atomic (I5)
and content-blind — last write wins, whatever its seq. So in the fixture:

1. `addCard` #1 emits and spawns nudge N1.
2. N1 wins `r.mu`, builds seq 1 with ONE card, releases the lock — and
   is starved before it reaches the relay.
3. `addCard` #2, then the fixture's explicit publish, builds seq 2 with
   two cards and writes it.
4. N1 wakes and writes its seq-1 bytes over seq 2. The relay now serves
   one card, and nothing republishes for `publicHeartbeat` (5 min).
5. Bob reads seq 1.

Reproduced deterministically by inserting a 150 ms sleep between N1's
build and its relay write (plus 20 ms after the first card so N1 builds
before the second): the log then shows `REPLACE seq=2` followed by
`REPLACE seq=1`, and only a second nudge's later rewrite of seq 2 saved
the read. On the runner that second rewrite lost too.

This was not a test artefact. A person adding two cards in quick
succession on a slow device left strangers looking at the old listing
for up to five minutes.

**Fix (same day):** a per-space `publishLane` mutex on `spaceState`,
held by `publishPublicProjectionForce` from build to relay write, so
projections land in the order they were built and a queued publisher
builds from the newer log. The fixture in `cat0b_inspect_test.go` says
why its explicit publishes are now reliable. No wait was added to the
test: nothing in it polls, and a wait could not have bounded a stale
write from a goroutine nobody can join.

**Verified:** the forced interleaving above no longer reorders the
writes with the lane in place; `-race -count=10` on the test and one
`-race` pass of `./node` green locally.

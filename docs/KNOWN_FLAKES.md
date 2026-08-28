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

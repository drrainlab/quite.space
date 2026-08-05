# android-baseline — does the core LIVE on Android, or merely compile for it?

AR-0's record. The harness itself is `main.go`; this file is the durable
transcript, in the shape `cmd/wails-probe/README.md` established: a dated
platform heading and a `| gate | verdict | detail |` table.

Two rules govern every number below, and they are the same two that
`cmd/rnode-baseline` and `cmd/relay-load` already impose on themselves:

- **The phone column is the one the gate reads.** The emulator runs
  arm64-native on Apple Silicon and is therefore *faster* than the physical
  device. An emulator-only result is an optimistic number wearing a
  measurement's clothes.
- **Targets are fixed AFTER running this on real hardware, not invented in a
  plan** — `cmd/relay-load/main.go:4-6`.

## Two harnesses, and they do not stand in for each other

    raw binary harness    cross-compile · adb push+exec from /data/local/tmp ·
                          scrypt/replay microbench · flock.
                          CANNOT carry the lifecycle gate.

    package-shaped host   a debug APK: real Context.getFilesDir(), core inside
                          the app UID and process tree, start|stop|status.
                          The ONLY thing lifecycle and live gates run on.

The split is not fastidiousness. `filesDir` is a package-private directory,
`am force-stop` acts on a package, and App Standby, background restrictions,
the App Freezer and LMK exit reasons all key on an app UID and a package
process tree. A shell process launched from `/data/local/tmp` lives in the
wrong lifecycle model, so a KILL test run there would prove something other
than what it claimed.

---

## 2026-08-05 — AR-0a, emulator lane

Rehearsal only. Nothing here is a phone number.

### Environment, as built

`cmdline-tools` was absent from this SDK and was installed
(`commandlinetools-mac-13114758_latest.zip`, sha256
`5673201e6f3869f418eeed3b5cb6c4be7401502bd0aae1b12a29d164d647a54e`) so the AVDs
are created by `avdmanager` from a named device profile rather than by
hand-editing a cloned `config.ini` — a gate has to be reproducible by somebody
who was not here. It needs JDK 17+; this machine had 11, so 21.0.5-librca was
added through the user's own sdkman **without changing their default java**.

Both AVDs are `-d pixel_7` on `system-images;android-35;google_apis_playstore;arm64-v8a`.
`pixel_7` is 1080×2400 at density 420 — which is Nothing Phone (1)'s panel, so
the rehearsal and the target share a viewport by construction rather than by
adjustment.

    Nothing_1_shaped   pixel_7 · 1080×2400 @420 · 8192 MB   the main lane
    Low_memory_2G      pixel_7 · 1080×2400 @420 · 2048 MB   screening lane

Both carry `fastboot.forceColdBoot=yes` and have their `snapshots/` removed: a
restored snapshot is not a cold start.

**A trap worth recording**: `avdmanager` gave the `pixel_7` profile a default
`hw.ramSize=2G`. Left alone, the main lane and the low-memory lane would have
had identical memory and the screening lane would have measured nothing. The
main lane is set to 8192 to match the physical target.

### Hardware manifest — `Nothing_1_shaped`, captured per run

`getprop` has no portable RAM key, so physical memory comes from
`/proc/meminfo`.

    model / device       sdk_gphone64_arm64 / emu64a
    fingerprint          google/sdk_gphone64_arm64/emu64a:15/AP31.240617.003/
                           12088229:user/release-keys
    release / API        15 / 35
    ABI                  arm64-v8a
    kernel               6.6.30-android15-7-gbb616d66d8a9-ab11968886-4k
    MemTotal             8129904 kB
    cores                4
    wm size / density    1080x2400 / 420   → 411 dp
    boot                 27.1 s cold

### Gates

| gate | verdict | detail |
|---|---|---|
| `CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build ./...` | pass | exit 0; 59 packages, the same set as the host |
| `cmd/terminal` links and runs for android/arm64 | pass | 16.7 MB ELF aarch64 PIE, interpreter `/system/bin/linker64` |
| **H5** — push to `/data/local/tmp` and exec on a `google_apis_playstore` image | **pass** | the whole tree, not a hello-world: `terminal --help` renders |
| `node.Open` on Android | pass | identity minted, LAN + API listeners up, `keys/keystore.enc`, `keys/salt`, `node.lock`, `delivery/delivery.ledger` written |
| file modes under Android | pass | dirs `0700`, files `0600` — as `kernel/storage/lock.go:60,64` intends |
| **H4** — `flock` on the `/data` volume | pass (raw lane) | `node.lock` taken; the second process is refused. NOT yet the `filesDir` answer — that needs the package host |
| identity survives reopen | pass | principal `7ba0 ecd1 f0b9 1cd3 372e 8970 8f43 f2a1 223b 8e20` across three opens |
| wrong passphrase fails closed | pass | `storage: wrong passphrase or corrupted keystore` — a named failure, and NO new identity |
| two processes, one data dir | pass | `storage: this data directory is already open in another Quiet Spaces process` |

### What this does and does not establish

**Does**: the Go runtime, the storage layer, `flock`, the KDF path and the
listeners all work on Android arm64, exercised through the real `cmd/terminal`
rather than a probe. H5 holds, so the microbenchmark route is available and no
`gomobile`/NDK fallback is needed for AR-0b.

**Does not**: any of it under an **app UID**. The lock, the data directory and
every lifecycle claim were exercised as the `shell` user on `/data/local/tmp`.
`getFilesDir()` semantics, `am force-stop`, App Standby, the App Freezer and
LMK are all untested until the package host exists. **H6 remains open** — that
running the core under an app UID needs no source change beyond an explicit
`dataDir` — and it is the hypothesis most likely to be wrong.

Also untested: the physical Nothing Phone (1), which needs developer options
and USB debugging enabled — the owner's step, and the only one this wave cannot
do for itself.

---

## 2026-08-05 — AR-0a.2, the package-shaped host

The structural gate. Everything above ran as the `shell` user out of
`/data/local/tmp`, which is the wrong lifecycle model for any claim about
process death; this is the rig that is not.

### The Android integration cost — AR-0's second deliverable

The core now runs **inside an Android application process**. What that cost, in
full, so nobody has to rediscover it:

| item | size | note |
|---|---|---|
| `cmdline-tools` 13114758 | 137 MB | absent; needed for `avdmanager`/`sdkmanager` |
| JDK 21 (via the user's own sdkman) | ~190 MB | `cmdline-tools` refuses JDK &lt; 17 |
| JDK 17 (via sdkman) | ~180 MB | AGP 8.2's `JdkImageTransform` fails on JDK 21 |
| NDK r27c (`ndk;27.2.12479018`) | 2.4 GB on disk | required by anything in-process |
| `gomobile` + `gobind` | small | plus a `tool` directive in the nested go.mod |
| `android/quietcore` (the binding) | ~200 lines Go | `Start`/`Stop`/`Status`, nothing else |
| `android/host` (the rig) | ~330 lines Java | 2 activities, no product interface |

Gradle 8.4 and AGP 8.2.0 were already in `~/.gradle` and were reused; a machine
without them adds a few hundred MB more. **Product source changes: none.** The
only Go written was the binding, and it lives in a nested module for exactly
the reason `cmd/wails-probe` does — ADR-011 mandates `CGO_ENABLED=0` for the
main binary, and `./...` never sees a directory with its own `go.mod`. Verified
after the fact: the root still lists 0 packages under `quietcore`, and
`CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build ./...` is still green.

### In-process, not a subprocess — and why that was not optional

The cheap route is to ship the static binary as a `.so` and spawn it with
`ProcessBuilder`, which needs no NDK at all. It was rejected. Android's
low-memory killer works from `oom_adj` scores that ActivityManager assigns to
**application** processes, and Doze throttles an **app's** network. A forked
child inherits its parent's score at fork time and is never updated, so it can
be killed at a different moment than the app — or outlive it. That would weaken
precisely two of AR-0c's gates, background → return and Doze, which are two of
the gates the rig exists to run. A rig that is lower-fidelity exactly where it
measures is not a rig.

The proof that it is genuinely in-process is in the status output below:
`core_pid` and `host_pid` are the same number.

### Gates

| gate | verdict | detail |
|---|---|---|
| CGO cross-compile, `-buildmode=c-shared` for android/arm64 via NDK clang | pass | the untested part of the toolchain, now tested |
| `gomobile bind` over the whole core | pass | 8.1 MB AAR, `jni/arm64-v8a/libgojni.so` 17 MB |
| debug APK builds | pass | 16 MB, arm64-v8a only |
| **H6** — the core runs under an app UID with no product source change | **pass** | `uid 10199`, `core_pid == host_pid == 5148` |
| real `Context.getFilesDir()` | pass | `/data/user/0/space.quiet.arprobe/files/node` |
| **H4** — `flock` on **`filesDir`** | **pass** | a genuinely separate process (`:contender`, pid 5311, same uid, confirmed distinct in `ps`) got the named refusal: `storage: this data directory is already open in another Quiet Spaces process` |
| the local HTTP API is reachable | pass | `adb forward` + `X-QP-Token` → `/api/status` answers |
| `am force-stop` stops the core | pass | both processes gone; nothing resumed on its own after a wait |
| explicit restart after force-stop | pass | new pid 5437 |
| identity survives force-stop | pass | fingerprint `d8dd 5e16 … c865 41d4` before and after |
| `ApplicationExitInfo` joins on pid | pass | both pids reported `USER_REQUESTED` / `[FORCE STOP]`, so a later LMK kill cannot be attributed to the wrong operation |

### The number that matters most so far

    memory_class_mb        192
    large_memory_class_mb  576
    system_total_mem       8325021696   (~7.75 GiB)

On an **8 GB** device the framework's standard memory class is **192 MB**. The
quick-link seal is `N=1<<17` — a **128 MiB** transient
(`protocol/quicklink/seal.go:25`), on the redeem path, which is the first thing
a new user ever does.

Stated carefully, because over-reading this would be the easy mistake: the
memory class governs the **Java heap**, and Go's allocations are native, so it
is not a hard ceiling on the KDF. What it is, is the framework's own statement
of what device class this is and how much headroom an app is expected to want —
and 128 MiB against a 192 MB expectation is the shape of H3's risk, now with a
number attached on the emulator. The real reading is the phone's, and the
low-memory lane's.

### Still open after this gate

`core_pid` was compared across a force-stop, which is the easy case. The hard
one is **background → return**, where the same comparison must show the pid
*unchanged* — and where a scenario satisfied by an unnoticed restart has to be
reported as a restart rather than passed. That is AR-0c.

---

## 2026-08-05 — AR-0b, THE PHONE

    Nothing Phone (1) · A063 / Spacewar · Android 15 (API 35) · arm64-v8a
    Snapdragon 778G+ (lahaina) · 8 cores (Go sees 7) · MemTotal 7432272 kB
    kernel 5.4.289-qgki · 1080×2400 @ 420 → 411 dp
    fingerprint Nothing/SpacewarEEA/Spacewar:15/AQ3A.240929.001/
                2602061016:user/release-keys

The AVD was built from the `pixel_7` profile, which is 1080×2400 at density
420 — so the rehearsal and the target come out at the **same 411 dp**, and at
the same API level. No skew to correct for.

### Desktop and phone, same harness commit, same corpora

Corpus A, controlled, one space. `fresh-process`, **OS cache uncontrolled** —
a fresh directory and a fresh process clear application state, never the
kernel's page cache, and this report does not use the word "cold".

**Two phone columns, because the phone has two states.** The early column was
measured on a phone that had just been connected; the frozen column is the
instrumented run below, taken while the kernel reported
`thermal-cpufreq-4 5/9, thermal-cpufreq-7 5/9`. The early figures are recorded
as an *observation that the phone was faster before it warmed up*, **not** as a
rested measurement — the thermal probe did not exist yet, so their state was
never captured. The instrument arrived after the observation that motivated it,
and a number without its state is not a measurement.

| | desktop (darwin/arm64, 10 cores) | phone, early (state unrecorded) | phone, FROZEN (throttled 5/9) | ratio to desktop |
|---|---|---|---|---|
| `Inspect` (stat only) | p50 149 µs | p50 363 µs | p50 **338–360 µs** | 2.4× |
| `VerifyPassphrase` (scrypt N=2^15 / 32 MiB) | p50 73.7 ms | p50 128–141 ms | p50 **181–203 ms** | **2.6×** |
| replay, paired `Open−Verify` | 70 µs/event | 111 µs/event | **155 µs/event** (fit) | 2.2× |
| `Open`, 16 000 events | — | 1.83 s | **2.61 s** | — |
| quicklink `seal` | p50 301 ms | p50 433 ms | p50 **659 ms** | 2.2× |
| quicklink `open` — the redeem path | p50 304 ms | p50 421 ms | p50 **646 ms** | 2.1× |
| quicklink `open`, Δ over baseline | n/a (no procfs) | 137.2 MB peak | **Δ 129.9 MB** on a 7.4 MB baseline | — |

**Budgets should be set from the throttled column.** It is not the pessimistic
reading, it is the realistic one: a phone that has been in use is a phone whose
big cores are capped.

**H2 can be retired.** The reconnaissance modelled a 3–6× ARM penalty on a
memory-hard KDF and budgeted 250–500 ms for keystore unlock; the throttled
measurement is 2.6× and ~190 ms — inside the budget even in the worse state.
**H3's timing likewise**: 1.5–3 s was modelled for the quick-link KDF and it is
646 ms throttled. H3's *memory* half is answered by the low-memory lane below,
not by this table.

    REPLAY SHAPE: LINEAR — slope grew 0.94× (criterion ≤ 1.25×)
                  fit: 155 µs/event

### Corpus B — the metric is portable, and per EVENT rather than per byte

The beta-realistic corpus exists to check whether `ms/event` survives a
workload that is not a column of identical rows: 6 private spaces, mixed event
sizes from 20 bytes to 900, unicode.

| corpus | shape | replay p50 | per event | MB/s |
|---|---|---|---|---|
| A (fit over 4K/8K/16K) | 1 space, uniform | — | **155 µs** | 3.1–3.4 |
| B | 6 spaces, mixed, unicode | 373.2 ms | **155 µs** | 4.6 |

Identical per-event cost across two quite different workloads, while the
throughput in MB/s differs by 40%. So the cost is **per event — one
`ed25519.Verify`, one AEAD open, one reducer apply — and not per byte**. That
is what makes `ms/event` worth quoting a year from now, which is the question
corpus B was added to answer.

### Two methodology defects this run found, both mine

Recorded because in both cases the wrong number would have gone into the table
looking perfectly reasonable.

**1. The quick-link memory figure was doubled by its own fixture.** The
`quicklink-open` probe seals first (untimed) and then opens, and the first
phone run reported a peak of **264.7 MB** — both 128 MiB scrypt allocations
live at once. That is not the cost of opening an invitation: a person redeeming
a link in a fresh app does the open and never the seal. The probe now collects
and resets `VmHWM` via `/proc/self/clear_refs` between the two, and the honest
figure is **137.2 MB — one KDF**.

**2. The first replay-shape verdict was an artefact of small N.** At
1K/2K/4K the phone reported `NONLINEAR — slope grew 5.02×`, while the
per-event cost *fell* from 137 µs to 78 µs — which no superlinear process
does. The two-slope test silently assumes a small constant, and opening a node
costs a fixed amount before a single event is replayed; at N=1000 that constant
dominated the difference being divided. Re-measured at **4K/8K/16K**, where it
amortizes: `LINEAR`, slope grew 1.02×, per-event 93 → 101 → 106 µs.

The harness now reports a least-squares fit beside the classification and
**refuses to classify** when the fixed cost exceeds a third of the smallest
measurement, saying so in the report rather than in somebody's head. The
criterion itself was not touched — it was written before the data and moving it
afterwards is precisely what the plan forbids.

### Correctness — every check, on the phone

| check | verdict | detail |
|---|---|---|
| `flock` internal | pass | taken and released |
| `flock` external/emulated | pass (informational) | never auto-selected |
| keystore create | pass | — |
| reopen keeps identity | pass | same fingerprint |
| wrong passphrase fails closed | pass | `storage: wrong passphrase or corrupted keystore` |
| file modes | pass | dirs 0700, files 0600 |
| truncated keystore → named failure, not a new identity | pass | named error; no second identity |
| two processes, one data dir | pass | measured by the rig's `:contender`, not here |

### The rig on the phone

    core_pid == host_pid == 26372     uid 10393
    files_dir  /data/user/0/space.quiet.arprobe/files
    memory_class  256 MB   (large 512 MB)
    system        2.2 GB available of 7.1 GB — a real device with real apps

| gate | verdict | detail |
|---|---|---|
| core runs under the app UID | pass | in-process, not a subprocess |
| `flock` on the real `filesDir` | pass | `:contender` pid 26092, same uid, distinct in `ps`, named refusal |
| local HTTP API reachable | pass | `adb forward` + token |
| `am force-stop` stops the core | pass | 0 processes; nothing resumed after a wait |
| explicit restart | pass | pid 26372 → 26785 |
| **identity survives force-stop** | **pass** | fingerprint `58fb ab86 d002 8d35 e51f 7fac …` unchanged |
| `runtime_epoch` changes on reopen | pass | `1de0…` → `4193…` — the process dying and the node being reopened are reported as different facts |
| `ApplicationExitInfo` joined on pid | pass | `pid=26372 USER_REQUESTED rss=157292 kB` |

Two rig defects fixed by running it: a failed command dropped the `core` block
from its answer, so a `start` against an already-running node lost the pid and
fingerprint of the node that was running perfectly well; and three exit reasons
rendered as bare `code_14/15/16`. Code 16 was `PACKAGE_UPDATED` — this rig's own
reinstall. The freezer one matters most for what comes next: AR-0c's background
gate has to tell *Android froze it* from *Android killed it*.

### Where H3 actually stands now

    quicklink open, peak RSS      137.2 MB
    app memory class              256 MB   (192 MB on the emulator)

One KDF against a 256 MB class is 53%. Stated carefully, because over-reading
it is the easy mistake: the memory class governs the **Java heap**, and Go
allocates natively, so this is not a hard ceiling — it is the framework's own
statement of what device class this is. The number to watch is the low-memory
lane's, and that lane's verdict is a finding rather than a gate failure.

### Still open

The low-memory screening lane, the beta-realistic corpus B on the phone, the
topology axis (1×4K vs 4×1K vs 20×200), the probe-only staleness diagnostics —
and all of AR-0c, which is the half that decides whether the core *lives* here
rather than merely running.

**AR-0b regenerated and FROZEN 2026-08-05.** The artefact now carries
`REASON_PACKAGE_UPDATED` rather than `code_16`, a status block on a failed
`start`, the isolated quicklink-open probe, the confound-aware shape
classification, and the thermal state of every run. The numbers in the frozen
column below are the ones to quote.

    AR-0a     GREEN
    AR-0a.2   GREEN — proven on the physical phone
    AR-0b     GREEN — frozen
    AR-0c     GREEN — one direction not exercised (no cellular data on the SIM)
    AR-0d     GREEN — floor passes; one blocking defect spun off to RS-0

## THE VERDICT — the core lives on Android

AR-0 was chartered to answer one question and produce two deliverables. Both
are here.

**The question.** The core does not merely compile for Android: it runs inside
an application process under the app UID, keeps its identity across SIGKILL and
force-stop, is *kept* across background → return, catches up after Doze, and
survives a network change without a restart. `flock` holds on the app's own
volume against a genuinely separate process. A stranger's phone joined a space
over the internet and every event carries one EventID on both devices.

**Deliverable 1 — the numbers**, from the phone, throttled (the realistic
state):

    keystore unlock, scrypt N=2^15      181–203 ms      2.6× the desktop
    replay                              155 µs/event    LINEAR, and per EVENT
                                                        rather than per byte
    Open, 16 000 events                 2.61 s
    quicklink open (the redeem path)    646 ms · Δ 129.9 MB over a 7.4 MB base
    app process baseline                ~105 MB (ART + JNI + libgojni + Go)
    low-memory screen, 2 GB             5/5 survived, no LMK kill

**Deliverable 2 — the Android integration cost.** ~2.9 GB of toolchain (NDK
r27c, two JDKs, cmdline-tools), ~200 lines of Go binding in a nested module and
~330 lines of Java rig. **Zero product source changes were needed to run it.**

**What AR-0 changed in the product** — one real defect, found by the gate and
fixed at the cause: a dead relay connection was never classified as fatal, so
the pool served a corpse for the life of the process and the node pushed while
pulling nothing. See "THE DEFECT THE GATE FOUND".

**What AR-0 hands to AR-1**, measured rather than assumed:

- a screen-off phone does not sync — so a wake plane and a foreground service
  are required, not optional, and *catch-up on return* is the honest promise
  until they exist;
- an expedited job has room to work: unlock + replay of a 16 000-event log is
  2.8 s, well inside the window;
- `force-idle` does not suspend the device, so real suspend-clock skew
  (finding 5) is still unmeasured and belongs to AR-1;
- Wi-Fi → cellular is untested and needs a SIM with data;
- the RS-0 debt list, measured at 360 dp, with one loud prediction retracted.

---

## 2026-08-05 — `android-lifecycle-staging-1`, the relay AR-0c needs

    91.201.114.71:7411 · Ubuntu 24.04 · x86_64 · 1 core · 2 GB
    SPKI pin  A63rjukjUJkPVU98l0XPdKjRiDNXTVs1xCm9Xs7jyI4=
    binary    cmd/terminal-relay @ 973b19b · sha256 44b9ef3a…

A relay on the Mac would have tested the home NAT rather than the client's
reconnect: it shares the phone's network, the same NAT, and a laptop that
sleeps. So the network gate gets an endpoint that shares none of those.

The name is deliberate. It is **not** an official relay and not a RU relay —
it is a staging node, so it cannot quietly become production infrastructure.
Dedicated `quietrelay` system user, hardened unit (`ProtectSystem=strict`,
`NoNewPrivileges`, `RestrictAddressFamilies`, `MemoryMax=768M`),
`Restart=always`, store in memory and zero-retention as the relay has always
been.

| gate | verdict | detail |
|---|---|---|
| service comes up | pass | `active`, 0 restarts |
| reachable from the Mac | pass | tcp/7411 |
| **persistent identity survives a restart** | **pass** | same SPKI pin before and after — the point of `--data`, and what makes pinning meaningful at all |
| identity key at rest | pass | `relay-identity.pem` 0600, owned by `quietrelay` |
| **the PHONE reaches it over the internet** | **pass** | the phone fetched the relay's identity and reported the same pin the server printed — a stronger proof than a ping, because it exercises the TLS identity path end to end |
| TOFU pins it | pass | `{"status":"pinned"}`; the sync loop's failure cooldown cleared |

One thing learned by getting it wrong: `POST /api/relay/trust` takes
`fingerprint`, not `pin`. Sending the wrong field name makes the node compare
the presented identity against an empty string and refuse with *"presented an
unexpected identity (…)"* — quoting back the very pin that was sent, which
reads like a mismatch in the relay and is in fact a mismatch in the request.
Accurate, and briefly baffling.

---

## 2026-08-05 — AR-0c step 1, and the finding that arrived unbidden

### A locked screen suppresses the app's network. Measured, not inferred.

Setting the gate up kept failing, and chasing why produced the cleanest result
of the wave. The same call, from the same process, at two screen states:

    screen locked / dozing    31.6 s  → dial: connection timed out
    screen awake, unlocked     0.3 s  → success, relay pin verified

Not specific to the relay: with the screen locked the app could not dial
`1.1.1.1:443` or `8.8.8.8:53` either — every external dial timed out at
31.6 s, while `nc` from `adb shell` connected instantly at the same moment.
Same routes for both UIDs (`ip route get … uid 10393` and `uid 2000` are
identical), `INTERNET granted=true`, Wi-Fi `VALIDATED`, standby bucket 10
(ACTIVE), no appops or netpolicy restriction. The shell is not an app, and
that is the whole difference.

**This confirms decision 4 of the plan empirically.** Without a foreground
service, a phone with its screen off does not sync — so AR-0c's closer
promising *catch-up on return* rather than live receipt is the correct promise,
and live screen-off delivery genuinely belongs to AR-1 with its notification.

**A CORRECTION, and it is the reason this section is written carefully.**
I first attributed the recovery to a battery-optimisation exemption I had added
(`dumpsys deviceidle whitelist +pkg`), having seen 8/8 successful dials right
after applying it. The owner pointed out that they had unlocked the phone at
about the same moment. Two variables had changed and I had credited one of them
on no evidence. The discriminating test — **remove the whitelist, keep the
screen unlocked** — gave 8/8 successes at 0.3 s, so the exemption had done
nothing and the screen state was the whole cause. The whitelist was removed and
the environment is stock. Recorded because it is exactly the kind of convincing
untruth this wave exists to catch, and it was one edit away from the report.

### The gate's first step

    android-lifecycle-staging-1 · both nodes · no LAN on either side

| step | verdict | detail |
|---|---|---|
| Mac node points at the staging relay, TOFU-pinned | pass | — |
| Mac mints a personal quicklink; the pass parks on the relay | pass | relay `items=1 bytes=598` |
| phone resolves the words over the internet | pass | after the screen finding above |
| phone joins | pass | `waiting_for_owner` → member; 2 spaces |
| message Mac → phone, and phone → Mac | pass | converged in under 5 s |
| **the same EventIDs on both devices** | **pass** | `65a1d8d4…` and `b9dd30da…` on both |
| the relay kept nothing | pass | `items=0 bytes=0 conns=2` after delivery |

Two diagnosability notes from getting here, both worth fixing later:

- `transports/lan/lan.go:109` returns a generic `lan: connection closed` on
  every call after the first failure, while the real cause sits unused in
  `c.err`. The reason a link died is therefore invisible from the moment it
  matters most.
- `ResolveQuickLink` reports **the last candidate's** error, so a fan-out that
  really failed on the configured relay reads as
  `dial tcp 127.0.0.1:7411: connection refused` — the built-in local-dev
  relay, which the operator never configured and is not the problem.

### The gate, run in full — `scripts/android/ar0c-lifecycle.sh`

| step | verdict | detail |
|---|---|---|
| 1 — a message each way | pass | converged in 3 s, one EventID per event |
| **D1 — SIGKILL → reopen** | **pass** | process gone, new pid, **identity survived** |
| **D2 — force-stop → explicit restart** | **pass** | stopped everything; **nothing resumed on its own**; identity survived |
| **background → return** | **pass** | **PROCESS KEPT** — same `core_pid` AND same `runtime_epoch`; caught up in 31 s |
| C — Doze, documented sequence | pass | `ACTIVE → IDLE` confirmed; caught up in 1 s |
| A — cellular → Wi-Fi | pass | caught up in 2 s |
| **B — recovery WITHOUT a restart** | **pass** | pid unchanged across the switch |
| A — Wi-Fi → cellular | **NOT EXERCISED** | see below |
| 7 — both ends restart | pass | identity survived the whole gate; converged in 1 s |

**Wi-Fi → cellular is untested, not failed.** With Wi-Fi off, the *shell* — which
is subject to no app restriction — cannot reach the relay either: the phone
registers LTE but has no working data path. Reporting "did not catch up" would
have blamed the client for a network that was never there, so the gate now
probes the precondition and says so. It needs a SIM with an active data plan.

### THE DEFECT THE GATE FOUND — a dead connection that was never fatal

This is what AR-0c was for.

After being backgrounded, the phone came back with `pushed 11 pulled 0` and a
permanent `lan: connection closed`. It never recovered: **its own messages
reached the Mac and the Mac's never reached it**, for as long as the process
lived. Only a restart cleared it — which is why every *fresh-start* scenario
passed and only background → return failed.

The mechanism, confirmed by a test rather than by reading:

    node/relaypool.go   isConnFatal() decided a connection was dead by
                        looking for the substring "use of closed"
                          ← the NET package's wording
    transports/lan      returns "lan: connection closed"
                          ← this transport's wording

The two never met. `isConnFatal` returned false, so the pool never retired the
corpse and handed it back to every caller forever.

Fixed at the cause rather than by adding a third substring: `lan.ErrConnClosed`
is now an exported **sentinel** and `isConnFatal` matches it with `errors.Is`.
Matching a sibling package's errors by text is what made this possible — a
string is not a contract, and nothing fails when it drifts. Pinned by
`node/relaypool_fatal_test.go`, which covers both wordings, a wrapped
sentinel, and the cases that must NOT be fatal (a relay refusal is an answer,
not a broken connection).

**Validated by the failure it came from**: background → return went from
*never catches up* to *31 s*, and step 1 from *did not converge in 60 s* to
*3 s*.

### `force-idle` does not suspend the device — an instrument being honest

The Doze step reports both clocks, and it reported:

    CLOCK_MONOTONIC advanced 110.7s
    CLOCK_BOOTTIME  advanced 110.7s
    suspended         -0.0s

Identical. So `dumpsys deviceidle force-idle` applies Doze **policy** without a
hardware suspend, and **finding 5's hazard cannot be reproduced this way**. The
instrument reported zero rather than inventing a plausible number, which is the
behaviour that makes it worth having. Measuring real suspend-related clock skew
needs a genuinely idle, unplugged, screen-off device over a long period, and
that is an AR-1 measurement.

### Two harness defects of mine, recorded because both would have lied

- **The identity assertions were comparing a quarter of an identity.** A
  fingerprint renders in groups (`58fb ab86 d002 …`) and the snapshot was read
  positionally with `read -r PID EPOCH FP MONO BOOT` — so `FP` held `58fb` and
  the clock fields held hex. It announced itself only as
  `ValueError: invalid literal for int() with base 10: 'ab86'`. Now tab-
  separated. A gate comparing the first word of a fingerprint would pass a
  changed one.
- **A shell function named `head` shadowed `/usr/bin/head`** inside every
  pipeline in the script, so the transport diagnostics in the A/B block printed
  separators and `-1` instead of the network state — which is why the first
  Wi-Fi→cellular result could not be interpreted at all. Renamed to `section`.

---

## 2026-08-05 — AR-0d, the narrow-screen launch

Run against the phone's OWN node — the core serves the web UI it already
embeds — first in the phone's browser at 411 dp, then through a desktop
browser pane forwarded to the same node so the DOM could be inspected rather
than guessed at from screenshots.

### The minimal launch floor — PASSES

Deliberately narrow, so that "the UI is rough on a phone" (which everyone
already knows) never gets confused with "the UI does not launch on a phone".

| floor item | verdict | evidence |
|---|---|---|
| the page loads | pass | at 411 dp in the phone's browser |
| authentication succeeds | pass | tokened URL, real content |
| the space list appears | pass | 8 rows; the sidebar starts folded, which is the drawer behaving correctly |
| one conversation opens | pass | `convTitle` = "ar0c lifecycle", messages rendered |
| the composer is reachable with the keyboard open | pass | composer 412×47 at the bottom of the viewport; the owner typed and sent a message from the phone |

**And no horizontal overflow at either width**: `body.scrollWidth` equals the
viewport at 412 *and* at 360, and the layout still holds at `font_scale 1.5`.
The reconnaissance's praise of the text-overflow discipline is confirmed in the
running app.

### THE DEFECT: a first-run modal with no way out, on a device with no Esc

Found by trying to use the app, and confirmed by the owner independently —
they could only escape it by opening *another* dialog and cancelling that.

    <dialog id="dlgWelcome">   open=true   modal=true   closedby=(none)
    buttons: "Create a space" · "Enter with a pass" · "Continue" · "Continue"
    → not one of them dismisses it

Two separate faults, and they compound:

1. **`needs_name` is not the same question as "is this a first run".**
   `NeedsOnboarding()` is `r.ks.DisplayName == ""` (`node/node.go:841`). A node
   that already holds an identity, eight spaces and a live conversation — but
   was opened without a display name ever being set — gets the first-run flow
   thrown over the top of it.
2. **A modal `<dialog>` with no dismiss control is escapable only by Esc, and a
   phone has no Esc.** On a desktop this is a papercut; on a phone it is a
   trap. This is the reconnaissance's Tier-2 item ("no backdrop tap-to-dismiss;
   several dialogs have no visible close control at all") arriving with a
   consequence nobody predicted.

The fix belongs to RS-0, not here — but the shape is clear: the welcome step
needs a dismiss, and its trigger needs to be "no identity" rather than "no
display name".

### The debt list, measured rather than read

Everything below was taken from the running app at 360 dp.

| item | measured | recon said |
|---|---|---|
| `--tap` token | **34px** | 34px — confirmed |
| interactive controls under 44 px | **15 of 21**; smallest 16 px (`+`), then 18 px (`⋯`, `⋯`), then five nav actions at 30 px | confirmed |
| `env(safe-area-*)` anywhere in the stylesheets | **none** | confirmed |
| `body` height | resolves to `100vh` (800 px) — no `dvh`, no `visualViewport` | confirmed |
| `.msg .mk` — the message actions | **`opacity: 0`, 200×16, present in the DOM and unreachable on touch** | confirmed, and it is the most severe item |
| `@media (hover: none)` coverage | **`.nav-more, .nav-grip` only** — while eight selectors reveal on hover | confirmed |
| width breakpoints | 520 px, 560 px, 600 px (+ `hover: none`, two `prefers-*`) | confirmed |

**One prediction NOT confirmed, and it was the loudest one.** The
reconnaissance called `#convbar` "the single most certain break at 375 px" on
the grounds that it is `display:flex` with no `flex-wrap` and up to ten
controls. Measured at 360 dp: `flexWrap: nowrap` is real, but five visible
children total **300 px inside a 360 px bar** and the row simply grows to 82 px
tall. **No overflow.** Reading the CSS predicted a break that measuring did not
find — the controls shrink and the title wraps before anything spills. `#convbar`
stays on the debt list as *crowded and two rows tall*, which is a different and
much smaller problem than *broken*.

That correction is the whole reason AR-0d exists as a measurement rather than a
review.

---

## 2026-08-05 — A PHONE IS NOT A SERVER

The single most important methodological finding of this wave, and it was found
by the numbers moving when nothing in the software had.

The same three corpora, the same binary, the same phone measured **128–141 ms**
of scrypt in one session and **177–292 ms** an hour later. Two things had
changed, and separating them mattered:

1. **A node was running in the background.** The rig's core had been left
   started and was syncing to the relay every two seconds. Stopping it did not
   move the median much — but the *variance* collapsed: 180/292/180 ms became
   178/177/183 ms. A background node is a noise source, not a bias.
2. **The phone was throttled.** After an hour of memory-hard KDFs it sat at
   52 °C with `scaling_max_freq` at **1766 MHz against a `cpuinfo_max_freq` of
   2515 MHz — the ceiling lowered to 70%**. That is the governor acting, not a
   core idling, and it is the remaining ~1.4×.

So the harness now samples **thermal and power state before and after every
run** and prints both lines in the report header, beside the
"OS cache uncontrolled" note and for the same reason: it will not claim a
condition it did not control, but it will always say what the condition was.
A phone's clock rate is a function of what it was doing a minute ago, and a
number quoted without that state is a number nobody can reproduce.

    thermal in   cpu 50.2°C · fastest core 1766/2515 MHz (70%)  ← CEILING LOWERED
    thermal out  cpu 53.7°C · fastest core 1766/2515 MHz (70%)  ← CEILING LOWERED

Battery sysfs is SELinux-restricted for the shell user on this device, so those
fields are absent from the report rather than guessed at. The CPU zone and the
frequency ceiling carry the signal on their own.

**Consequence for every number above:** the desktop-vs-phone ratios are honest
about a *warm, throttled* phone. A rested phone is faster. Both states are real
and a budget should be set from the throttled one, because that is the state a
person's phone is in when they have been using it.

### The topology axis — 4000 events, three shapes, quiet phone

| corpus | shape | verify p50 | replay p50 | per event |
|---|---|---|---|---|
| A4000 | 1 space × 4000 | 178.8 ms | 656 ms | 164 µs |
| T4x1000 | 4 spaces × 1000 | 176.7 ms | 623.7 ms | 156 µs |
| T20x200 | 20 spaces × 200 | 183.0 ms | 613.3 ms | 153 µs |

**Per-space overhead is not detectable at this scale.** Twenty spaces replay
marginally *faster* than one, which is within the run-to-run spread. `Open`
walking spaces serially was a plausible cost and it is not one here — the cost
is per event, not per space. The harness correctly answered
`REPLAY SHAPE: NOT CLASSIFIED — the corpora do not form a scaling axis`,
because they do not: this axis holds the event count fixed on purpose.

### The low-memory screening lane — 2 GB, KDF under the app UID

Run on `Low_memory_2G` through the package host, **a fresh process every
time** (force-stop, then invoke), because a transient measured in a process
that already peaked reads as no transient at all.

| run | pid | seal | open | baseline | seal peak | open peak |
|---|---|---|---|---|---|---|
| 1 | 3699 | 1286 ms | 516 ms | 109.8 MB | 262.2 MB | 266.8 MB |
| 2 | 3827 | 943 ms | 638 ms | 104.4 MB | 256.6 MB | 261.1 MB |
| 3 | 3996 | 1226 ms | 465 ms | 108.8 MB | 259.3 MB | 261.8 MB |
| 4 | 4117 | 1293 ms | 557 ms | 104.2 MB | 256.9 MB | 262.2 MB |
| 5 | 4219 | 1042 ms | 472 ms | 104.7 MB | 257.6 MB | 262.2 MB |

    memory class 192 MB · MemTotal 2022512 kB · 4 cores

**5 of 5 survived.** No `REASON_LOW_MEMORY`, no `SIGNALED`: every entry in
`ApplicationExitInfo` is one of this harness's own force-stops. The process
reached ~262 MB RSS against a 192 MB memory class and was not killed —
consistent with the memory class governing the Java heap while Go allocates
natively.

**The number nobody was looking for: the baseline is ~105 MB.** An app process
hosting this core costs about 105 MB resident *before any work happens* — ART,
the JNI bridge, a 17 MB `libgojni.so`, and the Go runtime. That is what the
baseline/peak/delta split was added to expose, and in a single "peak RSS"
figure it was invisible. It is also the number that matters most for a genuinely
low-end device: the KDF's ~130 MB lands on top of 105 MB, not on top of zero.

**What this lane does and does not establish.** A kill here would have been
strong evidence of a problem. A clean pass is **not** evidence that 2 GB
devices are fine: one idle AVD does not reproduce the memory pressure of a real
low-end phone with a dozen apps resident. It is a screen, it is enough for
AR-0, and the honest verdict on low-end hardware needs low-end hardware.

One caveat on the in-app peaks, stated rather than smoothed: the `open` figure
here is not cleanly isolated from the `seal` that preceded it, because Go does
not always return freed pages to the OS, so resetting `VmHWM` between them can
latch a still-high resident size. The clean isolated figure is the raw lane's
**Δ 129.8 MB**, measured in a process that did nothing else.

### The instrument AR-0c needs: two clocks

`android/quietcore` now reports `CLOCK_MONOTONIC` and `CLOCK_BOOTTIME` side by
side, and the rig reports `uptimeMillis` and `elapsedRealtime` beside them.

This is not symmetry for its own sake. Go's `time.Since` reads
`CLOCK_MONOTONIC`, which **stops while the device is suspended** — which is
exactly finding 5, the three places in the tree that age things with
`time.Since` and will believe an overnight doze was minutes. Sampling both
clocks before and after a Doze gives the suspended time as
`(boot_delta − mono_delta)`: measured directly, rather than inferred from a
clock that was asleep for the part being measured.

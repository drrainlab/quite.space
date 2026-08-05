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

| | desktop (darwin/arm64, 10 cores) | phone | ratio |
|---|---|---|---|
| `Inspect` (stat only) | p50 149 µs | p50 363 µs | 2.4× |
| `VerifyPassphrase` (scrypt N=2^15 / 32 MiB) | p50 73.7 ms | p50 **128–141 ms** | **1.8×** |
| replay, paired `Open−Verify` | 70 µs/event | **111 µs/event** (fit) | 1.6× |
| `Open`, 16 000 events | — | **1.83 s** | — |
| quicklink `seal` | p50 301 ms | p50 433 ms | 1.4× |
| quicklink `open` — the redeem path | p50 304 ms | p50 **421 ms** | 1.4× |
| quicklink `open` peak RSS | n/a (no procfs) | **137.2 MB** | — |

**H2 was pessimistic and can be retired.** The reconnaissance modelled a
3–6× ARM penalty on a memory-hard KDF and budgeted 250–500 ms; the measured
figure is 1.8× and ~130 ms. **H3 likewise**: 1.5–3 s was modelled for the
quick-link KDF, and it is 421 ms.

    REPLAY SHAPE: LINEAR — slope grew 1.02× (criterion ≤ 1.25×)
                  fit: 111 µs/event

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

**AR-0b is not archived until the fixed rig re-runs it once**, so the frozen
artefact carries `REASON_PACKAGE_UPDATED` rather than `code_16`, a status block
on a failed `start`, the isolated quicklink-open probe, and the confound-aware
shape classification. The numbers are not in doubt; the artefact should simply
be the one a reader can trust without this paragraph.

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

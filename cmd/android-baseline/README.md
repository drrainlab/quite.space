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
reported as a restart rather than passed. That is AR-0c, and it is next.

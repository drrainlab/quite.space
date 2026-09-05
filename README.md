<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/brand/orbit-dark.svg">
  <img src="docs/brand/orbit-light.svg" alt="quite.space — a planet on a quiet orbit" width="640">
</picture>

**A quiet place for the people you choose.**

`local-first` · `end-to-end` · `serverless history` · `internet / LAN / LoRa`

[**Download**](#-download) · [What it does](#-what-it-does) ·
[What's new](#-whats-new) · [From source](#-running-it-from-source) ·
[Docs](#-documents) · [Status](#-status)

</div>

---

Your conversations live on your own devices, and they travel over whatever
path is available — the internet, a local network, or a LoRa radio. There is
no server holding your history, because there is nowhere for one to sit.

> **Install the client — become a node.**

Under it is the **Terminal Mesh Kernel**: the basic entity is not a chat, a
channel or a profile but a **Terminal** — a cryptographically addressable
thing that can be a person, a shared space, a bot, an AI agent, a sensor, a
gateway, a relay or an archive. A space is one entity whether two people or
twenty are in it; a direct message is a presentation mode, not a data type.

The rule everything else is measured against:

> A Terminal must never appear smarter, more reliable, more human, safer or
> more available than it can prove.

## 📦 Download

**Public beta. The desktop builds are EXPERIMENTAL and are signed by no
authority** — there is no Apple Developer membership and no Debian repository
behind this — so each platform asks you to let the app through once. That is a
deliberate trade, and it is said here rather than hidden.

| | |
|---|---|
| 🍎 **macOS** | [`quite-space-macos-universal.dmg`](../../releases/latest/download/quite-space-macos-universal.dmg) — Apple Silicon and Intel, macOS 13.3+ |
| 🐧 **Linux** | [`quite-space-linux-amd64.deb`](../../releases/latest/download/quite-space-linux-amd64.deb) — Debian 12+ / Ubuntu 22.04+, x86-64 |
| 🤖 **Android** | [`quite-space-android.apk`](../../releases/latest/download/quite-space-android.apk) — Android 7.0+, arm64 |

**macOS.** Drag **Quite Space** to Applications, launch it once — macOS will
refuse and say it cannot verify the developer, which is true — then
**System Settings → Privacy & Security → Open Anyway**. Or:

```sh
xattr -dr com.apple.quarantine "/Applications/Quite Space.app"
```

**Linux.** Install with `apt`, not `dpkg -i` — apt resolves the GTK and WebKit
runtimes:

```sh
sudo apt install ./quite-space-linux-amd64.deb
```

**Android.** Direct install; there is no Play Store listing. Your device will
ask you to allow installing from this source. Every build is signed by the same
key — if an update is ever refused because the signature changed, do not work
around it, tell us instead.

Verify a download against `SHA256SUMS` on the release:

```sh
shasum -a 256 -c SHA256SUMS
```

## ⚡ What it does

- 🏠 **Spaces** — private by default, or public and readable by anyone with
  the link. One entity behind a two-person line, a group and a project room.
- 🗝 **Invitations** that are five spoken words or a link, with an optional
  approval step. No account, no phone number, no directory of people.
- 📜 **Posts** — long-form documents with media, and an optional generative
  **atmosphere** that plays behind the article.
- 🔭 **Discover** — catalogues are ordinary public spaces, so anybody can run
  one. Looking inside a space never subscribes you to it.
- 🧭 **A field that never lies** — places, markers, check-ins, and positions
  as signed claims that age honestly in front of you: live, then stale, then
  unknown. The map draws what somebody *stated*, never what it guesses.
- 📻 **Any transport.** An internet relay, a direct LAN link, or a LoRa
  radio, chosen automatically. A relay may introduce people; a LAN may speed
  them up; a radio may keep them talking when the internet is gone. None of
  those transitions creates a new contact or changes what is happening.
- 🌱 **Instruments** — a sensor is a first-class participant with its own
  keys, admitted like anyone else, its readings sealed to their own key
  lineage. An ESP32 SDK speaks the wire byte-for-byte with the Go kernel.
- 🤖 **A local AI terminal** that never leaves the device, when you configure
  a provider for it.

Encryption is end-to-end per space, with epoch keys rotated on membership
change. Relays are blind and hold nothing: no accounts, no retention beyond a
short TTL, and — for private spaces — no ability to read what passes through.
The exact scope of that claim, including where it does **not** hold, is in
[ADR-016](adr/ADR-016-public-access.md).

## 🛰 What's new

```
$ quite log --releases
  1.0.1-rc.2     delivered means acknowledged
  1.0.1-rc.1     the board found the door by itself
  1.0.0-beta.3   delivered is about machines, read is about you
  1.0.0-beta.2   where you actually went
```

**1.0.1-rc.2** is a fix on top: the USB stand acknowledges every frame it
applied, so an instrument's chain can no longer be holed by a cable pulled
mid-sentence; the Wi-Fi courier is no longer struck out for not having
dialed yet; the door keeps a diary — [release notes](docs/releases/1.0.1-rc.2.md).

**1.0.1-rc.1** is the second episode of Quiet Instruments: a sensor on a
shelf, a laptop in another room, **no cable between them** —
[release notes](docs/releases/1.0.1-rc.1.md).

- 📡 **Wi-Fi courier.** A provisioned board joins your network, hears the
  node announce itself, recognises its own space from the hint, dials over
  TLS and knocks with a signature bound to the node's certificate. Readings
  ride up that connection; key rotations and the clock ride down. USB
  becomes the rescue road — unplug it and the readings keep arriving.
- 🔌 **Plug in, click.** The USB stand lives in the app: open a space, plug a
  board in, *Connect USB instrument*. The choice survives restarts.
- ✦ **Something new, said quietly.** A space with moments you have not seen
  earns one small breathing star. Opening it quiets the star. No badges.
- 🧊 **Relief.** The verbs that carry intent tilt toward you and breathe;
  kept things, posts and objects wear a spine and lean toward the cursor.
- 👻 **A ghost can be ended.** An instrument is a member while it speaks
  under the current epoch — cards appear with the first reading and leave
  with the key, on every replica at once.
- 🕳 **Held, not lost.** The panel says when moments arrived ahead of what
  came before them and wait for it — the line that found this release's
  deepest bug.

What the first board taught us is in the notes, so nobody rediscovers it:
a board that never restored its identity, and a stand that claimed
delivery to a cable nobody was reading. Both fixed, both pinned.

## 🖥 Running it from source

```sh
go run ./cmd/terminal ui --passphrase "a passphrase of your own"
```

That is the whole thing: one CGO-free binary that serves the interface on
127.0.0.1 and opens a browser. `terminal node` is the same runtime headless —
on a Raspberry Pi, say. The desktop application is the same node with a window
in front of it:

```sh
cd cmd/desktop && go run .
```

Standing up your own relay, so that your people depend on nobody else's:

```sh
go run ./cmd/terminal-relay --listen :7411
```

## 📚 Documents

| | |
|---|---|
| [docs/guide/](docs/guide/README.md) | the user guide: spaces, invitations, conversation, posts, atmosphere, signals, the Navigator, networking, self-hosting. Russian; an English version follows. |
| [docs/radio/en/](docs/radio/en/README.md) | talking over LoRa: which carrier is proven, flashing an RNode board, attaching one, and the radio tools. [По-русски](docs/radio/README.md). |
| [docs/instruments/](docs/instruments/ESP32.md) | an ESP32 as a citizen: the C core, enrollment, the dev stand. |
| [adr/](adr/README.md) | 34 architecture decision records. The reasoning is there rather than in commit messages. |
| [VISION_AND_ROADMAP.md](VISION_AND_ROADMAP.md) | the original concept and the first engineering plan, kept as written — with [ENGINEERING_PLAN_M0_M1.md](ENGINEERING_PLAN_M0_M1.md). |

## 🧾 Status

**1.0.0-beta.** The number is a promise about formats, not about polish:
every log, backup, pass, bundle and device certificate written by this
build opens in every later 1.x. [ADR-033](adr/ADR-033-what-1-0-promises.md)
says exactly what is frozen — and, just as deliberately, what is not (the
loopback HTTP API, the interface, local projections). The beta suffix
marks the reach of our evidence, not a reservation on the formats.

Honest about the edges:

```
  works, in daily use ──── spaces · invitations · conversation · media
                           voice · posts · atmosphere · public spaces
                           catalogues · relays with failover · LAN
                           Android · field sessions
  proven, still young ──── LoRa radio (two boards, no internet, text
                           both ways) · the doorbell for a sleeping
                           phone · the ESP32 instrument SDK
  experimental ─────────── the desktop shell (and both desktop
                           packages are unsigned)
  not built yet ────────── a general multi-hop mesh · group calls
```

- **A limit worth knowing in the field:** a shared position travels one hop.
  Two people on opposite sides of a relay-less segment will see each other
  age to "unknown" rather than appear — honest, but a real edge if you are
  planning around it. The measurement that would widen it is the next
  hardware step ([ADR-031](adr/ADR-031-the-field-is-a-map-of-claims.md)).

## ⚖️ Licence

Two licences, split by what a piece of code *does*:

- **Apache-2.0** — the protocol, schemas, kernel, transports, the relay's wire
  protocol and client, the SDKs and the clients. Everything we want to see
  everywhere: run it, embed it, ship a closed product on top of it, put this on
  a device. The patent grant is explicit.
- **AGPL-3.0-only** — the components an operator stands up so that *other
  people* can use them: `transports/relayserver`, `cmd/terminal-relay` and
  `cmd/quiet-bridge`. Free to run, to modify and to charge for hosting — but
  offer a modified version to users over a network and those users can have
  that version's source.

The relay's server and client live in separate Go packages precisely so that
line can hold; the reasoning, and the directory-by-directory map, is in
[LICENSING.md](LICENSING.md).

Names, logos and the Official Relay / Verified Space marks are granted by
neither licence — see [TRADEMARK_POLICY.md](TRADEMARK_POLICY.md). Fork freely,
under your own name.

Contributing: [CONTRIBUTING.md](CONTRIBUTING.md) ·
Reporting a vulnerability: [SECURITY.md](SECURITY.md)

---

<div align="center">

*the space between us belongs to us*

</div>

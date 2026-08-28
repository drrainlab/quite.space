# Quite Space

**A quiet place for the people you choose.** Your conversations live on your
own devices, and they travel over whatever path is available — the internet, a
local network, or a LoRa radio. There is no server holding your history,
because there is nowhere for one to sit.

> Install the client — become a node.

Under it is the **Terminal Mesh Kernel**: the basic entity is not a chat, a
channel or a profile but a **Terminal** — a cryptographically addressable
thing that can be a person, a shared space, a bot, an AI agent, a sensor, a
gateway, a relay or an archive. A space is one entity whether two people or
twenty are in it; a direct message is a presentation mode, not a data type.

The rule everything else is measured against:

> A Terminal must never appear smarter, more reliable, more human, safer or
> more available than it can prove.

## Download

**Public beta. The desktop builds are EXPERIMENTAL and are signed by no
authority** — there is no Apple Developer membership and no Debian repository
behind this — so each platform asks you to let the app through once. That is a
deliberate trade, and it is said here rather than hidden.

| | |
|---|---|
| **macOS** | [`quite-space-macos-universal.dmg`](../../releases/latest/download/quite-space-macos-universal.dmg) — Apple Silicon and Intel, macOS 13.3+ |
| **Linux** | [`quite-space-linux-amd64.deb`](../../releases/latest/download/quite-space-linux-amd64.deb) — Debian 12+ / Ubuntu 22.04+, x86-64 |
| **Android** | [`quite-space-android.apk`](../../releases/latest/download/quite-space-android.apk) — Android 7.0+, arm64 |

**macOS.** Drag **Quite Space** to Applications, launch it once — macOS will
refuse and say it cannot verify the developer, which is true — then
**System Settings → Privacy & Security → Open Anyway**. Or
`xattr -dr com.apple.quarantine "/Applications/Quite Space.app"`.

**Linux.** Install with `apt`, not `dpkg -i` — apt resolves the GTK and WebKit
runtimes: `sudo apt install ./quite-space-linux-amd64.deb`

**Android.** Direct install; there is no Play Store listing. Your device will
ask you to allow installing from this source. Every build is signed by the same
key — if an update is ever refused because the signature changed, do not work
around it, tell us instead.

Verify a download against `SHA256SUMS` on the release:
`shasum -a 256 -c SHA256SUMS`

## What it does

- **Spaces** — private by default, or public and readable by anyone with the
  link. One entity behind a two-person line, a group and a project room.
- **Invitations** that are five spoken words or a link, with an optional
  approval step. No account, no phone number, no directory of people.
- **Posts** — long-form documents with media, and an optional generative
  **atmosphere** that plays behind the article.
- **Discover** — catalogues are ordinary public spaces, so anybody can run one.
  Looking inside a space never subscribes you to it.
- **Any transport.** An internet relay, a direct LAN link, or a LoRa radio,
  chosen automatically. A relay may introduce people; a LAN may speed them up;
  a radio may keep them talking when the internet is gone. None of those
  transitions creates a new contact or changes what is happening.
- **A local AI terminal** that never leaves the device, when you configure a
  provider for it.

Encryption is end-to-end per space, with epoch keys rotated on membership
change. Relays are blind and hold nothing: no accounts, no retention beyond a
short TTL, and — for private spaces — no ability to read what passes through.
The exact scope of that claim, including where it does **not** hold, is in
[ADR-016](adr/ADR-016-public-access.md).

## Running it from source

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

## Documents

- [docs/guide/](docs/guide/README.md) — the user guide: spaces, invitations,
  conversation, posts, atmosphere, signals, the Navigator, networking,
  self-hosting. Russian; an English version follows.
- [docs/radio/en/](docs/radio/en/README.md) — talking over LoRa: which carrier
  is proven, flashing an RNode board, attaching one, and the radio tools.
  [По-русски](docs/radio/README.md).
- [adr/](adr/README.md) — 22 architecture decision records. The reasoning is
  there rather than in commit messages.
- [VISION_AND_ROADMAP.md](VISION_AND_ROADMAP.md) and
  [ENGINEERING_PLAN_M0_M1.md](ENGINEERING_PLAN_M0_M1.md) — the original
  concept and the first engineering plan, kept as written.

## Status

**1.0.0-beta.** The number is a promise about formats, not about polish:
every log, backup, pass, bundle and device certificate written by this
build opens in every later 1.x. [ADR-033](adr/ADR-033-what-1-0-promises.md)
says exactly what is frozen — and, just as deliberately, what is not (the
loopback HTTP API, the interface, local projections). The beta suffix
marks the reach of our evidence, not a reservation on the formats.

Honest about the edges:

- **Works, in daily use:** spaces, invitations, conversation, media, voice,
  posts and atmosphere, public spaces and catalogues, relays with automatic
  selection and failover, LAN, Android.
- **Proven on hardware, still young:** LoRa radio. Two boards, no relay and no
  internet: an introduction, a joined space, and text both ways. Media over
  radio is not promised.
- **New in this release, and young:** a doorbell for a sleeping phone. A
  relay you have parked with can ring an opt-in push endpoint when mail
  arrives — a contentless ping, never the message. It is off until you
  turn it on, because the endpoint is a stable identifier handed to a
  relay, and that is a trade worth making deliberately rather than by
  default. Proven on one phone so far.
- **Experimental:** the desktop shell, and both desktop packages are unsigned.
- **A limit worth knowing in the field:** a shared position travels one hop.
  Two people on opposite sides of a relay-less segment will see each other
  age to "unknown" rather than appear — honest, but a real edge if you are
  planning around it. The measurement that would widen it is the next
  hardware step ([ADR-031](adr/ADR-031-the-field-is-a-map-of-claims.md)).
- **Not built yet:** a general multi-hop mesh, group calls.

## Licence

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

Contributing: [CONTRIBUTING.md](CONTRIBUTING.md).
Reporting a vulnerability: [SECURITY.md](SECURITY.md).

---

*the space between us belongs to us*

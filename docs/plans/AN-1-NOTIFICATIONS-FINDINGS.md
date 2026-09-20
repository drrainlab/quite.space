# AN-1 — "notifications do not work": what the code says, and what changed

Status: findings from the code, then **measured on the owner's phone the
same day** (Nothing Phone (1), Android 15, 1.0.21 → 1.0.22-rc1…rc3 over adb)
— see "On the device" below. The owner and several users report that nothing
arrives on Android.

There is no push service, by design. For a message to become a system
notification on a dark phone, FIVE things must all be true — and on a normal
install several were not, with no screen saying so.

## Where it breaks, most likely first

1. **Nothing reopened the node after Android killed the process.** Swiping
   the app from recents kills it on most OEM builds. `START_STICKY` brought
   `AvailabilityService` back — and it never opened the core, so the permanent
   "Stay connected" card sat over a closed node. Nothing synced until the
   person opened the app, which is when they would have seen the message
   anyway. *(Fixed here: the service takes the doorbell's posture — nudge an
   open node, open a closed one if the passphrase is remembered.)*
2. **POST_NOTIFICATIONS never granted disarms the core.** The ask is a native
   strip shown after unlock; "Not now" leaves it un-granted, and while it is,
   `Quietcore.disarmNotifications()` means no candidate is produced at all. No
   screen in the interface said so. *(Visible now — see below.)*
3. **Doze cuts the network and the app held no exemption.** No
   `REQUEST_IGNORE_BATTERY_OPTIMIZATIONS`, no wake lock, no alarm. A parked
   listener hears nothing in Doze; the 3-minute net poll does not run either.
   *(The exemption can now be asked for — by the person, on the system's own
   dialog.)*
4. **The doorbell (UnifiedPush) — the one mechanism built for a sleeping
   phone — is off by default and needs a distributor app (ntfy).** Unchanged.
5. **A passphrase that is not remembered** means a killed node cannot come
   back by itself, by design. *(Said now.)*
6. **Best-case latency is 60 s (no listener) to 3 min (parked listener's
   net).** Somebody testing with a 30-second glance will report "nothing".
7. **A start-order race:** the controller was constructed before the channels,
   and its init posts from a worker; a `notify()` to a channel that does not
   exist yet is dropped silently. First post after a crash. *(Fixed: order
   swapped.)*
8. Arming redelivers on the caller's goroutine; it was called from the UI
   thread on every resume. A stall, not a loss. *(Moved to the worker.)*

Channels were audited against every `Notification.Builder` site: the v2
migration left no orphan id.

## On the device (2026-09-20)

- 1.0.21 as installed: POST_NOTIFICATIONS granted, all five channels present
  (no orphan), `AvailabilityService` foreground, standby bucket 10, NOT on
  the Doze whitelist, a message notification standing in the shade, seven
  ESTABLISHED connections to the three relays. The basic path works here.
- `am crash quite.space` (a system kill): the service was restarted by
  Android in ~9 s — over a CLOSED core. Card: "Waiting to be unlocked".
  Relay connections: **0**. It stayed that way. This is finding 1,
  reproduced exactly.
- The owner's passphrase is sealed behind a code, so the plain vault is
  empty and nothing may reopen the node without them. rc2 therefore says so:
  "Quiet was closed by Android — open it to keep receiving" appeared in the
  shade after the same kill. (With a remembered passphrase the node reopens
  instead; that branch is the doorbell's existing, shipped posture.)
- The reopen/nudge runs ONLY on a system restart (`intent == null`): an
  ordinary start comes from the Activity while the person is at the unlock
  screen, where the node is also "not alive" for a second.
- **The permanent card had never told the truth.** It read
  `core.relay.{primary,backup,seconds_since_pull}` and the core's status
  never contained a `relay` object, so it said "No relay · nothing yet" for
  as long as it ran — on a phone holding seven relay connections. The core
  writes that block now, and the card is re-read once a minute and re-posted
  only when its sentence changes (LOW channel: no sound).
- An app UPDATE also kills the process and the sticky service does not
  survive a package replace: after every update the phone is deaf until the
  app is opened. Not fixed here (`MY_PACKAGE_REPLACED` belongs with the boot
  receiver).

## What changed in this commit

- `AvailabilityService.onStartCommand` → `controller.wakeForDoorbell()`.
- `QuietApp.onCreate`: channels first, controller second.
- `RuntimeController`: the ordinary re-arm runs on the worker.
- Bridge: `notificationsBlocked`, `openNotificationSettings`,
  `batteryRestricted`, `askBatteryExemption` (booleans and requests to show a
  SYSTEM screen — the `stayRefused` family; nothing a stolen token could carry
  away). Manifest: `REQUEST_IGNORE_BATTERY_OPTIMIZATIONS`.
- Settings → This device opens with **"Why nothing may arrive"**: up to three
  lines, each shown only while true, two of them with the button that fixes
  it. Absent on a host that cannot answer.

## Not done, on purpose

- **Start on boot** (`RECEIVE_BOOT_COMPLETED`). It is the right next step now
  that the mode defaults on, but Android 14/15 restrict which foreground
  service types may start from boot and this needs a device to prove.
- **Doorbell on by default** — needs a distributor the app cannot install.
- Anything that would make the app exempt from Doze without the person
  pressing a button.

## Checklist with a phone on a cable

```sh
PKG=quite.space
adb shell dumpsys package $PKG | grep -A2 POST_NOTIFICATIONS          # granted?
adb shell dumpsys notification --noredact | grep -A6 "$PKG"           # channels: quite.messages.v2 …
adb shell dumpsys activity services $PKG | grep -E "AvailabilityService|isForeground"
adb shell dumpsys deviceidle whitelist | grep -i quite                # exempt after the button?
adb shell am get-standby-bucket $PKG
adb logcat -s quiet-notify:V quiet-runtime:V quiet-bridge:V quiet-availability:V quiet-up:V
# then: swipe the app away, screen off, send from another device, wait 3 min
SER=<serial> SOAK=1 scripts/android/ar1c-availability-gate.sh        # the honest gate: no Doze exemption bought
```
`/api/status` carries the core's own answer: `notify_armed=false` is (2);
`armed=true, delivered=0` is (1) or (3); `dropped>0` is the host not keeping up.

## AN-2 — the keyless watch (2026-09-20, same day)

The owner's question after the measurements above: *is there a secure way to
learn only the FACT of new mail, revealing no content and copying no
passphrase for the background?* Yes — and his review shaped it (addresses are
a watch-capability, not harmless hashes; never park the week at once; check
for mail already waiting at every reconnect; one general line; a swappable
transport; an explicit expiry; no latency promises).

- `transports/relay` + `relayserver`: MsgListenOK carries one bit, "something
  is already there", decided under the listener lock after registration.
- `node/watch.go`: BuildWatchPlan (hints for 7 days + endpoints with pins, no
  capability), RunWatch (no key; current/previous epoch, next only 10 min
  before a rollover; 5 s → 15 min backoff with jitter; Expired).
- `android/quietcore/watch.go`, `WakeTransport.kt` (DirectWatch is the first
  transport), `RestartReceiver.kt` (BOOT_COMPLETED, MY_PACKAGE_REPLACED).
- Observed on the owner's phone: update → receiver → service → "watch parked"
  with the node closed → "Something is waiting — open to see it".

### Acceptance still to run on a device

| case | how | expect |
|---|---|---|
| reboot | restart the phone, unlock the PHONE only, do not open Quiet | service card appears; `adb logcat -s quiet-watch` shows "parked"; a message from another device raises the line |
| network loss | airplane mode 10 min with the app closed, send a message meanwhile, airplane mode off | the line appears soon after the network returns (the waiting bit), without opening the app |
| night in Doze | leave the phone untouched overnight, app closed, send at 03:00 | note WHEN the line appears; repeat with the battery exemption on |
| epoch rollover | keep the app closed across 00:00 / 06:00 / 12:00 / 18:00 UTC | "parked" is logged again after the rollover; a message after it still rings |
| a week unopened | (or edit expires_at in a debug build) | "Open Quiet to keep background notifications working" |

When reading notifications over adb, filter to the package — a bare
`dumpsys notification --noredact` prints every app's texts:

    adb shell dumpsys notification --noredact | awk '/NotificationRecord\(/{k=($0 ~ /pkg=quite\.space /)} k && /android\.(title|text)=String/'

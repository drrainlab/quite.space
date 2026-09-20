# AN-1 — "notifications do not work": what the code says, and what changed

Status: findings from the code, 2026-09-20. **No phone was attached**, so
nothing below is a measurement on a device; the checklist at the end is what
turns each line into one. The owner and several users report that nothing
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

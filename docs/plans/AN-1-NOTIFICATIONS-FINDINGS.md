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

## AN-3 — the conventional flow, by the owner's decision (2026-09-20, evening)

AN-2 rang a bell that named nothing, and the owner's brother still missed two
messages: a contentless line is one people learn to ignore, and nothing was
*delivered* until the app was opened. The owner's words: "представь спас
операция — важное уведомление будет пропущено", then, asked directly, "давай
как в Signal" and "по умолчанию давай включим".

What changed (1.0.24):

| piece | before | now |
|---|---|---|
| node in a pocket | opens only on an unlocked phone; never behind a code/face | opens by itself (second keystore key without `setUnlockedDeviceRequired`), setting default ON; OFF = wait for `USER_PRESENT`, then open |
| code / face | guard the node's opening — asked only when the node is closed | guard the SCREEN — asked after 60 s away, node open or not (`uiLocked`) |
| 10 wrong codes | code erased, remembered passphrase kept → next launch opens unasked | remembered passphrase erased with the code |
| notification default | HIDDEN | SENDER: who + where always, words only while unlocked (enforced by us: `KeyguardManager.isDeviceLocked`, silent re-render on `USER_PRESENT` / `SCREEN_OFF`+6 s) |
| ring → notification | full cycle, mail collected last: 26 s measured | mail pass first: 1–3 s measured |
| invite door | polled (3 min dark) | parked on (`ReqHint` in the listen set), admission 0.3 s in the test |

Measured on the owner's phone (A063, locked, process killed with `am crash`):
node back in seconds, 7 relay connections; message → notification 0.9–3.4 s;
1.76 MB photo (27 chunks) handed to a peer with the screen dark.

The keyless watch (AN-2) stays: it is what runs when the setting is off and
the phone is locked, and when nothing is remembered at all.

Tried and withdrawn: (1) a `notificationPolicy()` getter on the bridge — the
bridge hands back booleans only, and its test says so by name; the page's
echo lives in the node's ui-state instead. (2) the delivery receipt sent straight after the doorbell's pull — it put
receipts on the relay for peers sitting on the same LAN
(`t6_lan_offload_test`); the receipt stays with the cycle, so "delivered" can
trail the other phone's notification by a cycle. (3) the mail pull moved onto
the sync loop's own goroutine, on the suspicion that the separate lane was
what upset that same test: measured on the phone it was 18–30 s again — a
ring also earns a full cycle, the cycle takes ~20 s there, and the next
message of a conversation rings into a busy loop. The separate lane is back.
What had actually upset the test was the test: `stopRelay` does not recall a
cycle in flight, and bob's last one landed 185 ms before the baseline count
(traced with timestamps). It lost that race 0 times in 39 before and 1 in 8
once rings caused extra activity; the baseline is now taken after deposits
stop landing — 40 of 40.

Still not run on a device: a night in Doze, network loss and return, reboot,
epoch rollover — the AN-2 table above stands.

## EN-4 — the doorbell carried by Google (2026-09-22)

**The report.** A tester on 1.0.24: "после блокировки уведомления не идут";
"запускаю приложуху и все приходит, уже со звуком". Every measurement in
AN-3 was taken over USB — on a charger — and a charging phone never enters
Doze. His phone lay on a table: light Doze cuts an app's network minutes
after the screen goes dark and closes nothing. The node was alive and deaf.

**Two holes, not one.**

1. *No carrier.* The platform's push lane is the one channel Android keeps
   open through Doze. EN-3 built the doorbell for a UnifiedPush distributor,
   which almost nobody has installed.
2. *The relay believed the park.* `ring` fired only when NO connection was
   parked at the hint. A dozing phone's socket looks parked for up to
   `listenIdle` (45 min) — so even a registered doorbell stayed silent.

**What changed.**

| piece | where | rule |
|---|---|---|
| relay | `transports/relayserver/push.go` | a park is trusted only while it proves itself: notify, then if nobody COLLECTS at that hint within `pushGrace` (12 s) the doorbell rings anyway; a collect cancels it. Tests: a collecting park stays quiet; a dozing park rings after the grace and not before; unregistered hints grow no timers |
| gateway | `cmd/quiet-push` (new) | `POST /fcm/{token}` → one FCM HTTP v1 message: `android.priority=high`, `ttl=120s`, `data={qp:1}`, no notification block. Service-account OAuth2 in stdlib (RS256 JWT → token endpoint, cached, renewed a minute early). Token shape checked; 30 s per-token coalesce; nothing logged or kept. Deployed on 195.63.160.237 as `quiet-push.service` (user quietpush, `/etc/quiet-push/sa.json` root:quietpush 0640) behind nginx `push.quite.space` → 127.0.0.1:8993 (Cloudflare-proxied; DNS record is the owner's) |
| android | `GoogleDoorbell.kt` | `FcmDoorbell : FirebaseMessagingService`; token → endpoint `https://push.quite.space/fcm/<token>` → the same `SetPushEndpoint` the UnifiedPush path uses; ring → `Doorbell.ring` (shared with UnifiedPush). Default ON where Google services exist and no UnifiedPush distributor is installed (owner's decision); `firebase_messaging_auto_init_enabled=false` so the switch is real. Build: `firebase-messaging:24.1.1`, google-services plugin applied only when `app/google-services.json` exists (CI writes it from the `GOOGLE_SERVICES_JSON` secret, base64) |
| core | `quietcore.KickSync` → `node.DoorbellRing` | a push ring is the mail-first pull + door poll + cycle, not a plain cycle kick |

**What Google learns:** a token it issued, and the moment. The relay's ping
is a fixed marker; the gateway forwards a fixed marker. Said in the settings
row (`ui.set.doorbell.google`).

**Acceptance (to run):** phone unplugged → `adb shell dumpsys battery unplug
&& adb shell dumpsys deviceidle force-idle` → message from the stand →
notification within seconds, `logcat -s quiet-fcm quiet-doorbell` shows the
ring; then `dumpsys deviceidle unforce && dumpsys battery reset`. Then the
tester's phone overnight.

**Key hygiene:** the service-account key was attached in chat once. After
the end-to-end check, mint a new key in the Firebase console, replace
`/etc/quiet-push/sa.json`, restart `quiet-push`, delete the old key.

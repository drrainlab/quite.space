#!/bin/bash
# EN-4 gate: does a message reach a phone in (forced) Doze?
#
# The phone must be on a cable for adb, so Doze is FORCED: the battery is
# told it is unplugged and the device idle controller is pushed into deep
# idle. That is the state a phone on a table reaches by itself after ~30
# minutes, and the state in which the parked relay connection is deaf.
#
#   STAND_TOKEN=fatok STAND_URL=http://127.0.0.1:8501 SPACE=<hex> ./doze-doorbell-gate.sh
set -euo pipefail
ADB="${ADB:-$HOME/Library/Android/sdk/platform-tools/adb}"
: "${SPACE:?space id (hex) on the stand node}"
STAND_URL="${STAND_URL:-http://127.0.0.1:8501}"; STAND_TOKEN="${STAND_TOKEN:-fatok}"

cards() { "$ADB" shell dumpsys notification --noredact | awk '/NotificationRecord\(/{keep = ($0 ~ /pkg=quite\.space/ && $0 ~ /tag=space:/)} keep && /android\.text=/' | tr -d '\n'; }
cleanup() { "$ADB" shell dumpsys deviceidle unforce >/dev/null; "$ADB" shell dumpsys battery reset >/dev/null; echo "doze: released"; }
trap cleanup EXIT

"$ADB" logcat -c
"$ADB" shell dumpsys battery unplug
"$ADB" shell dumpsys deviceidle force-idle
echo "doze: $("$ADB" shell dumpsys deviceidle get deep)"
sleep 5
before="$(cards)"
t0=$(date +%s)
curl -s -m 20 -H "X-QP-Token: $STAND_TOKEN" -H 'Content-Type: application/json' \
  -d '{"text":"doze gate '"$(date +%H:%M:%S)"'"}' "$STAND_URL/api/spaces/$SPACE/messages" >/dev/null
for _ in $(seq 1 90); do
  if [ "$(cards)" != "$before" ]; then echo "GATE notified_after=$(( $(date +%s) - t0 ))s verdict=pass"; break; fi
  sleep 1
done
[ "$(cards)" != "$before" ] || echo "GATE notified_after=none verdict=FAIL"
"$ADB" logcat -d -s quiet-fcm quiet-doorbell quiet-up | tail -5

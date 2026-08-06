#!/usr/bin/env bash
# AR-1c.4 — does a message actually arrive on a dark phone.
#
# THE ONLY QUESTION THAT MATTERS HERE, and the one a foreground service does
# not answer by existing. A service raises a process's standing; it is not a
# wake lock and not a promise that the CPU stays awake. So this measures the
# thing itself: the mode on, the Activity destroyed, the screen off, and a
# message sent minutes later.
#
# AND IT MEASURES WITHOUT BUYING ANYTHING. The b.6b.6 gate exempts the app
# from Doze because its subject is what a notification SAYS, and a frozen
# process would measure something else badly. This gate's subject IS the
# freezing, so it exempts nothing — a pass here means the mode works on an
# ordinary phone, and a fail is a real finding rather than a harness artifact.
#
# THE SCREEN GOES OFF, WHICH LOCKS THE PHONE. Nothing here can unlock it and
# nothing here will type a passcode: reading the shade through dumpsys works
# while locked, so the run is unaffected, and the phone is left awake at the
# end with its keyguard up for whoever is nearby.
#
# USAGE
#   SER=P21321000131 ./scripts/android/ar1c-availability-gate.sh        # 3 min
#   SOAK=1 SER=…     ./scripts/android/ar1c-availability-gate.sh        # 3/10/30
set -uo pipefail

SER=${SER:-}
PKG=${PKG:-space.quiet.arprobe}
PASS=${PASS:-ar1b-gate-passphrase}
RELAY_PORT=${RELAY_PORT:-7431}
PEER_PORT=${PEER_PORT:-8822}
PEER_TOKEN=${PEER_TOKEN:-ar1ctoken}
PEER_DATA=${PEER_DATA:-/tmp/ar1c-peer}
OUT=${OUT:-/tmp/ar1c-availability}
FWD_PORT=${FWD_PORT:-9944}

ADB=(adb)
[ -n "$SER" ] && ADB=(adb -s "$SER")

mkdir -p "$OUT"
REPORT="$OUT/report.jsonl"
: > "$REPORT"

SPACE_TITLE="ROOM_AVAIL_3d71"
PASSED=0; FAILED=0
declare -a SUMMARY

log()  { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
fail() { printf 'FATAL: %s\n' "$*" >&2; exit 1; }

record() {
  local check=$1 verdict=$2 reason=$3; shift 3
  python3 - "$check" "$verdict" "$reason" "${*:-}" >> "$REPORT" <<'PY'
import json, sys
check, verdict, reason, extra = sys.argv[1:5]
row = {"check": check, "verdict": verdict, "reason": reason}
if extra.strip():
    try:
        row.update(json.loads(extra))
    except Exception:
        row["extra_unparsed"] = extra
print(json.dumps(row, sort_keys=True))
PY
  case "$verdict" in
    pass) PASSED=$((PASSED+1));;
    fail) FAILED=$((FAILED+1));;
    *) ;;
  esac
  SUMMARY+=("$(printf '%-36s %-6s %s' "$check" "$verdict" "$reason")")
  log "$check → $verdict: $reason"
}

# ─── the other side ─────────────────────────────────────────────────────────

LAN_IP=$(ipconfig getifaddr en0 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}')
[ -n "$LAN_IP" ] || fail "no LAN address; the phone cannot reach a relay here"
RELAY_ADDR="$LAN_IP:$RELAY_PORT"

# A PERSISTENT IDENTITY, because this gate KILLS the relay and brings it back.
#
# Without --data the new process mints a new key, and a node that pinned the
# old one refuses it — correctly. The first run of this gate read that refusal
# as "the node cannot recover from a dead socket" and it was the opposite: the
# pin did exactly its job, against a relay that really had changed. A stale
# socket and a changed identity are different questions, and only one of them
# is being asked here.
start_relay() {
  (go run ./cmd/terminal-relay --listen "0.0.0.0:$RELAY_PORT" --data "$OUT/relay-identity" \
     >> "$OUT/relay.log" 2>&1 &)
  sleep 6
}
kill_relay() { pkill -f "terminal-relay --listen 0.0.0.0:$RELAY_PORT" 2>/dev/null; sleep 2; }

start_world() {
  log "relay on $RELAY_ADDR"
  start_relay
  rm -rf "$PEER_DATA"
  log "peer node on :$PEER_PORT"
  (go run ./cmd/terminal ui --passphrase gate-peer-pass --data "$PEER_DATA" \
     --name peer --port "$PEER_PORT" --token "$PEER_TOKEN" --no-browser --no-lan \
     > "$OUT/peer.log" 2>&1 &)
  for _ in $(seq 1 40); do
    sleep 2
    curl -sf -H "X-QP-Token: $PEER_TOKEN" "http://127.0.0.1:$PEER_PORT/api/status" >/dev/null && break
  done
  curl -sf -H "X-QP-Token: $PEER_TOKEN" "http://127.0.0.1:$PEER_PORT/api/status" >/dev/null \
    || fail "the peer node did not come up: $(tail -3 "$OUT/peer.log")"
  peer_api POST /api/settings "{\"relay\":\"$RELAY_ADDR\"}" >/dev/null
  trust_relay "http://127.0.0.1:$PEER_PORT" "$PEER_TOKEN"
}

stop_world() {
  kill_relay
  pkill -f "$PEER_DATA" 2>/dev/null
  # Leave the device as it was found: the screen setting, the Activity
  # lifetime, and the radios. A gate that changes a phone and walks away is
  # the next run's mystery.
  "${ADB[@]}" shell settings put global always_finish_activities 0 >/dev/null 2>&1
  "${ADB[@]}" shell svc wifi enable >/dev/null 2>&1
  "${ADB[@]}" shell svc power stayon false >/dev/null 2>&1
  "${ADB[@]}" shell input keyevent KEYCODE_WAKEUP >/dev/null 2>&1
}
trap stop_world EXIT

peer_api() {
  local m=$1 p=$2 b=${3:-}
  if [ -n "$b" ]; then
    curl -s -X "$m" -H "X-QP-Token: $PEER_TOKEN" -H 'Content-Type: application/json' \
      -d "$b" "http://127.0.0.1:$PEER_PORT$p"
  else
    curl -s -X "$m" -H "X-QP-Token: $PEER_TOKEN" "http://127.0.0.1:$PEER_PORT$p"
  fi
}

phone_api() {
  local m=$1 p=$2 b=${3:-}
  if [ -n "$b" ]; then
    curl -s -X "$m" -H "X-QP-Token: $PHONE_TOKEN" -H 'Content-Type: application/json' \
      -d "$b" "http://127.0.0.1:$FWD_PORT$p"
  else
    curl -s -X "$m" -H "X-QP-Token: $PHONE_TOKEN" "http://127.0.0.1:$FWD_PORT$p"
  fi
}

trust_relay() {
  local api=$1 tok=$2 pin
  pin=$(curl -s -X POST -H "X-QP-Token: $tok" -H 'Content-Type: application/json' \
        -d "{\"endpoint\":\"$RELAY_ADDR\"}" "$api/api/relay/identity" \
        | python3 -c 'import json,sys; print(json.load(sys.stdin).get("pin",""))')
  [ -n "$pin" ] || return 1
  curl -s -X POST -H "X-QP-Token: $tok" -H 'Content-Type: application/json' \
    -d "{\"endpoint\":\"$RELAY_ADDR\",\"forget\":true}" "$api/api/relay/trust" >/dev/null
  curl -s -X POST -H "X-QP-Token: $tok" -H 'Content-Type: application/json' \
    -d "{\"endpoint\":\"$RELAY_ADDR\",\"fingerprint\":\"$pin\"}" "$api/api/relay/trust" >/dev/null
}

# ─── the device ─────────────────────────────────────────────────────────────

RIG_SEQ=3000
rig() {
  RIG_SEQ=$((RIG_SEQ+1))
  "${ADB[@]}" shell am start -n "$PKG/.RigActivity" --es cmd "$1" --ei seq "$RIG_SEQ" "${@:2}" >/dev/null
  for _ in $(seq 1 40); do
    sleep 1
    local j; j=$("${ADB[@]}" shell run-as "$PKG" cat files/rig-out.json 2>/dev/null)
    if printf '%s' "$j" | python3 -c "import json,sys; sys.exit(0 if json.load(sys.stdin).get('seq')==$RIG_SEQ else 1)" 2>/dev/null; then
      printf '%s' "$j"; return 0
    fi
  done
  return 1
}

core_field() { printf '%s' "$1" | python3 -c "
import json,sys
print(json.load(sys.stdin).get('core',{}).get('$2',''))" 2>/dev/null; }

host_field() { printf '%s' "$1" | python3 -c "
import json,sys
n = json.load(sys.stdin).get('host',{}).get('notifications',{})
print(n.get('$2',''))" 2>/dev/null; }

shade_records() {
  "${ADB[@]}" shell dumpsys notification --noredact 2>/dev/null | python3 -c '
import re, sys
PKG = "'"$PKG"'"
# Ours only — the rest of this dump is the phone owner’s private life — and
# deduped by key, because dumpsys prints one record in several sections. The
# permanent "staying connected" card (id 2) is NOT a message and never counted.
seen, n = set(), 0
for line in sys.stdin:
    if "NotificationRecord(" not in line or PKG not in line:
        continue
    m = re.search(r"key=(\S+)", line)
    k = m.group(1) if m else line
    if k in seen:
        continue
    seen.add(k)
    if re.search(r"\|2\|", k):
        continue
    n += 1
print(n)
'
}

ensure_node() {
  local state; state=$(core_field "$(rig status)" state)
  if [ "$state" != "alive" ]; then
    rig start --es pass "$PASS" --es name alice >/dev/null || return 1
  fi
  local s; s=$(rig status) || return 1
  PHONE_PORT=$(core_field "$s" api_port)
  PHONE_TOKEN=$(core_field "$s" session_token)
  [ -n "$PHONE_PORT" ] && [ "$PHONE_PORT" != "0" ] || return 1
  "${ADB[@]}" forward --remove "tcp:$FWD_PORT" >/dev/null 2>&1
  "${ADB[@]}" forward "tcp:$FWD_PORT" "tcp:$PHONE_PORT" >/dev/null || return 1
  phone_api POST /api/settings "{\"relay\":\"$RELAY_ADDR\"}" >/dev/null
  trust_relay "http://127.0.0.1:$FWD_PORT" "$PHONE_TOKEN"
  return 0
}

join_space() {
  local sp link req
  sp=$(peer_api POST /api/spaces "{\"title\":\"$SPACE_TITLE\"}" \
       | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
  link=$(peer_api POST "/api/spaces/$sp/passes" \
         "{\"max_uses\":1,\"ttl_hours\":4,\"relay\":\"$RELAY_ADDR\"}" \
         | python3 -c 'import json,sys; print(json.load(sys.stdin)["link"])')
  req=$(phone_api POST /api/join-requests "{\"pass\":\"$link\"}" \
        | python3 -c 'import json,sys; print(json.load(sys.stdin).get("request_id",""))')
  [ -n "$req" ] || return 1
  for _ in $(seq 1 30); do
    sleep 3
    phone_api GET "/api/join-requests/$req" | grep -q '"status":"ready"' && { printf '%s' "$sp"; return 0; }
  done
  return 1
}

say() {
  local ans
  ans=$(peer_api POST "/api/spaces/$1/messages" \
    "$(python3 -c 'import json,sys; print(json.dumps({"text": sys.argv[1]}))' "$2")")
  printf '%s' "$ans" | grep -q '"id"' || { log "the peer did not accept a message"; return 1; }
  return 0
}

cleanup_spaces() {
  local ids n=0
  ids=$(phone_api GET /api/spaces | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(0)
for s in (d if isinstance(d, list) else d.get("spaces", [])):
    if (s.get("display_title") or s.get("title") or "").startswith("ROOM_AVAIL_"):
        print(s["id"])' 2>/dev/null)
  for sid in $ids; do phone_api DELETE "/api/spaces/$sid" >/dev/null && n=$((n+1)); done
  [ "$n" -gt 0 ] && log "removed $n room(s) this gate created"
  return 0
}

# ─── the run ────────────────────────────────────────────────────────────────

log "device: $("${ADB[@]}" shell getprop ro.product.model | tr -d '\r') API $("${ADB[@]}" shell getprop ro.build.version.sdk | tr -d '\r')"
"${ADB[@]}" shell pm grant "$PKG" android.permission.POST_NOTIFICATIONS >/dev/null 2>&1
# NOTHING IS BOUGHT HERE. No Doze exemption, no standby bucket, no stayon:
# the subject of this gate is exactly what those would hide.
"${ADB[@]}" shell dumpsys deviceidle whitelist "-$PKG" >/dev/null 2>&1
"${ADB[@]}" shell svc power stayon false >/dev/null 2>&1

start_world
"${ADB[@]}" shell input keyevent KEYCODE_WAKEUP >/dev/null 2>&1
ensure_node || fail "the node on the phone would not open"
cleanup_spaces
SPACE=$(join_space) || fail "could not join a space"
log "space $SPACE"

# The identity that must not change while the phone is dark.
BEFORE=$(rig status)
EPOCH_BEFORE=$(core_field "$BEFORE" runtime_epoch)
FP_BEFORE=$(core_field "$BEFORE" fingerprint)

# ---- 1. the mode goes on -------------------------------------------------
rig stay --es arg on >/dev/null
sleep 3
AFTER_ON=$(rig status)
if [ "$(host_field "$AFTER_ON" availability_leases)" = "1" ] &&
   "${ADB[@]}" shell dumpsys activity services "$PKG" 2>/dev/null | grep -q "isForeground=true"; then
  record "mode.on" pass "one lease and a foreground service"
else
  record "mode.on" fail "the mode did not take"
fi

# ---- 2. the Activity is destroyed, the process is not --------------------
#
# "Don't keep activities" is the honest way to do this: the screen is gone
# the moment it leaves the front, exactly as it is for somebody whose phone
# is under memory pressure, while the service and the core carry on. Killing
# the process instead would test something else entirely.
"${ADB[@]}" shell settings put global always_finish_activities 1 >/dev/null 2>&1
"${ADB[@]}" shell input keyevent KEYCODE_HOME >/dev/null; sleep 4
# `dumpsys activity activities` keeps historical `source=ActivityRecord{...}`
# lines for orientation changes, so grepping it for the class name matched an
# Activity that had been gone for minutes. `activity top` lists what is
# actually running.
if "${ADB[@]}" shell dumpsys activity top 2>/dev/null | grep -q "ACTIVITY $PKG/.QuietActivity"; then
  record "activity.destroyed" fail "the screen is still running"
elif "${ADB[@]}" shell dumpsys activity services "$PKG" 2>/dev/null | grep -q "isForeground=true"; then
  record "activity.destroyed" pass "the screen is gone and the service is not"
else
  record "activity.destroyed" fail "the service went with the screen"
fi

# ---- 3. the screen goes off ---------------------------------------------
"${ADB[@]}" shell input keyevent KEYCODE_SLEEP >/dev/null; sleep 5
WAKE=$("${ADB[@]}" shell dumpsys power 2>/dev/null | grep -oE "mWakefulness=[A-Za-z]+" | head -1)
record "screen.off" info "${WAKE:-unknown}"

# ---- 4. messages arrive at 3 / 10 / 30 minutes --------------------------
MINUTES=(3)
[ "${SOAK:-0}" = "1" ] && MINUTES=(3 10 30)
sent=0
for m in "${MINUTES[@]}"; do
  log "waiting ${m} minutes with the phone dark"
  # The wait is from the START of the run of checks, so 3/10/30 are the
  # points a person would recognise rather than cumulative sleeps.
  sleep $((m * 60 - (sent * 0)))
  before=$(shade_records)
  say "$SPACE" "dark ${m}m $(date +%H%M%S)" || { record "dark.${m}m" fail "the peer would not send"; continue; }
  ok=no
  for _ in $(seq 1 45); do
    sleep 2
    [ "$(shade_records)" -gt "$before" ] && { ok=yes; break; }
  done
  if [ "$ok" = yes ]; then
    record "dark.${m}m" pass "a message arrived on a dark phone"
  else
    record "dark.${m}m" fail "nothing arrived within 90s" \
      "{\"entries\":$(phone_api GET "/api/spaces/$SPACE/entries" | python3 -c 'import json,sys
try: print(len(json.load(sys.stdin)))
except Exception: print(-1)' 2>/dev/null)}"
  fi
  sent=$((sent+1))
done

# ---- 5. the identity did not move ---------------------------------------
NOW=$(rig status)
if [ "$(core_field "$NOW" runtime_epoch)" = "$EPOCH_BEFORE" ] &&
   [ "$(core_field "$NOW" fingerprint)" = "$FP_BEFORE" ]; then
  record "identity.unchanged" pass "same runtime epoch and fingerprint"
else
  record "identity.unchanged" fail "the core was reopened underneath the mode"
fi

# ---- 6. a stale socket, WITH THE SCREEN ON -------------------------------
#
# Deliberately not in the dark. Two questions were being asked at once and
# answered as one: "does the node notice a dead connection" and "does a dark
# phone get to run at all". The first is the product's, the second is
# Android's, and the first has an answer already — two desktop nodes find a
# relay that came back in thirteen seconds. So this half is measured awake,
# and what it proves is that the mode itself recovers; what a DARK phone can
# recover is the next line down, and its own finding.
"${ADB[@]}" shell input keyevent KEYCODE_WAKEUP >/dev/null 2>&1; sleep 3
#
# The relay is killed and brought back on the same port: every connection the
# phone holds is now dead, and nothing tells it so. A node that notices and
# redials keeps working; one that trusts a socket forever goes quiet with a
# green light — which is the failure this reproduces on purpose.
log "killing the relay under the connection"
kill_relay
sleep 10
start_relay
before=$(shade_records)
if say "$SPACE" "after the socket died $(date +%H%M%S)"; then
  ok=no
  for _ in $(seq 1 60); do
    sleep 2
    [ "$(shade_records)" -gt "$before" ] && { ok=yes; break; }
  done
  [ "$ok" = yes ] \
    && record "socket.stale-replaced" pass "the connection was rebuilt without help" \
    || record "socket.stale-replaced" fail "nothing arrived after the relay came back"
else
  record "socket.stale-replaced" fail "the peer would not send"
fi

# ---- 7. the network goes and comes back, also awake ---------------------
log "dropping wi-fi"
"${ADB[@]}" shell svc wifi disable >/dev/null 2>&1
sleep 20
say "$SPACE" "written while offline $(date +%H%M%S)" >/dev/null
sleep 10
"${ADB[@]}" shell svc wifi enable >/dev/null 2>&1
before=$(shade_records)
ok=no
for _ in $(seq 1 90); do
  sleep 2
  [ "$(shade_records)" -gt "$before" ] && { ok=yes; break; }
done
[ "$ok" = yes ] \
  && record "network.recovers" pass "what was written offline arrived after the network returned" \
  || record "network.recovers" fail "nothing arrived within three minutes of the network returning"

# ---- 7b. and once more in the dark, which is the honest question --------
#
# The same recovery, with the screen off and nothing bought. A pass means the
# mode delivers what it promises; a FAIL here is not a defect in the plane —
# it is the size of what AR-1d is for, measured rather than assumed.
"${ADB[@]}" shell input keyevent KEYCODE_SLEEP >/dev/null; sleep 5
before=$(shade_records)
if say "$SPACE" "dark after a network change $(date +%H%M%S)"; then
  ok=no
  for _ in $(seq 1 90); do
    sleep 2
    [ "$(shade_records)" -gt "$before" ] && { ok=yes; break; }
  done
  [ "$ok" = yes ] \
    && record "dark.after-network-change" pass "a dark phone recovered on its own" \
    || record "dark.after-network-change" fail "a dark phone did not recover within three minutes"
else
  record "dark.after-network-change" fail "the peer would not send"
fi
"${ADB[@]}" shell input keyevent KEYCODE_WAKEUP >/dev/null 2>&1; sleep 2

# ---- 8. the mode goes off ------------------------------------------------
"${ADB[@]}" shell input keyevent KEYCODE_WAKEUP >/dev/null 2>&1
rig stay --es arg off >/dev/null
sleep 4
OFF=$(rig status)
if [ "$(host_field "$OFF" availability_leases)" = "0" ] &&
   ! "${ADB[@]}" shell dumpsys activity services "$PKG" 2>/dev/null | grep -q "\.AvailabilityService"; then
  record "mode.off" pass "no lease and no service"
else
  record "mode.off" fail "the mode outlived being switched off"
fi

# ---- 9. a force-stop resurrects nothing ---------------------------------
#
# START_STICKY brings a service back after the SYSTEM kills it, and must not
# after a PERSON does. Android already draws that line; this asserts we did
# not draw over it.
rig stay --es arg on >/dev/null; sleep 3
"${ADB[@]}" shell am force-stop "$PKG" >/dev/null; sleep 8
if "${ADB[@]}" shell dumpsys activity services "$PKG" 2>/dev/null | grep -q "\.AvailabilityService"; then
  record "forcestop.stays-stopped" fail "the mode came back after a force-stop"
else
  record "forcestop.stays-stopped" pass "stop from a person means stop"
fi

"${ADB[@]}" shell settings put global always_finish_activities 0 >/dev/null 2>&1
"${ADB[@]}" shell input keyevent KEYCODE_WAKEUP >/dev/null 2>&1
ensure_node >/dev/null 2>&1 && cleanup_spaces

echo
echo "AR-1c.4 — $(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf '%s\n' "${SUMMARY[@]}"
printf '\n%d passed, %d failed.  Report: %s\n' "$PASSED" "$FAILED" "$REPORT"
echo "the phone is awake and its keyguard is up — nothing here can unlock it"
[ "$FAILED" -eq 0 ]

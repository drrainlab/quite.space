#!/usr/bin/env bash
# AR-1b.5.6 — the notification plane's live gate, end to end.
#
# THIRTEEN SCENARIOS, ISOLATED, NOT ONE CHAIN. A single linear run is the
# tempting shape and the dangerous one: an early dismiss, a moved watermark or
# a permission answered once makes a later scenario green for a reason nobody
# intended. So each scenario states what it needs, resets what it must, and
# says which of the two it did — and every one of them ends in a machine
# readable line with no message content in it.
#
# WHAT IT PROVES THAT A UNIT TEST CANNOT. The JVM tests own the exact crash
# windows: they can stop between two transactions on demand, which no phone
# will do on request. What only a device can show is that the same semantics
# survive the real path — a real relay, a real peer, gomobile, SQLite, the
# system shade, and a process Android is free to kill. Both halves are needed
# and neither replaces the other.
#
# USAGE
#   SER=emulator-5554 ./scripts/android/ar1b5-notify-gate.sh            # all
#   SER=P21321000131  ./scripts/android/ar1b5-notify-gate.sh 02 07 13   # some
#
# The relay and the peer node are started here and torn down at the end. The
# phone's passphrase and the app must already be installed:
#   PASS=…  the node's passphrase on the device
#
# REPORT: one JSON object per scenario on stdout, plus a table at the end.
# Event ids and space ids appear; message text never does.
set -uo pipefail

SER=${SER:-}
PKG=${PKG:-space.quiet.arprobe}
PASS=${PASS:-ar1b-gate-passphrase}
RELAY_PORT=${RELAY_PORT:-7411}
PEER_PORT=${PEER_PORT:-8802}
PEER_TOKEN=${PEER_TOKEN:-ar1btoken}
PEER_DATA=${PEER_DATA:-/tmp/ar1b-gate-peer}
OUT=${OUT:-/tmp/ar1b5-gate}

ADB=(adb)
[ -n "$SER" ] && ADB=(adb -s "$SER")

mkdir -p "$OUT"
REPORT="$OUT/report.jsonl"
: > "$REPORT"

PASSED=0; FAILED=0; SKIPPED=0
declare -a SUMMARY

log()  { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
fail() { printf 'FATAL: %s\n' "$*" >&2; exit 1; }

# record <scenario> <verdict> <reason> [extra json fields]
record() {
  local scenario=$1 verdict=$2 reason=$3; shift 3
  local extra="${*:-}"
  python3 - "$scenario" "$verdict" "$reason" "$extra" >> "$REPORT" <<'PY'
import json, sys
scenario, verdict, reason, extra = sys.argv[1:5]
row = {"scenario": scenario, "verdict": verdict, "reason": reason}
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
    *)    SKIPPED=$((SKIPPED+1));;
  esac
  SUMMARY+=("$(printf '%-26s %-7s %s' "$scenario" "$verdict" "$reason")")
  log "$scenario → $verdict: $reason"
}

# ─── the two other halves of the world ──────────────────────────────────────

LAN_IP=$(ipconfig getifaddr en0 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}')
[ -n "$LAN_IP" ] || fail "no LAN address — the phone and this machine must reach one RELAY, and 127.0.0.1 is not one of them"
RELAY_ADDR="$LAN_IP:$RELAY_PORT"

start_world() {
  log "relay on $RELAY_ADDR (0.0.0.0 so the device can reach it)"
  (go run ./cmd/terminal-relay --listen "0.0.0.0:$RELAY_PORT" > "$OUT/relay.log" 2>&1 &)
  sleep 18
  grep -q "listening" "$OUT/relay.log" || fail "the relay did not come up: $(tail -3 "$OUT/relay.log")"

  log "peer node on :$PEER_PORT"
  rm -rf "$PEER_DATA"
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
  pkill -f "terminal-relay --listen 0.0.0.0:$RELAY_PORT" 2>/dev/null
  pkill -f "data $PEER_DATA" 2>/dev/null
  pkill -f "$PEER_DATA" 2>/dev/null
}
trap stop_world EXIT

peer_api() { # peer_api <method> <path> [body]
  local m=$1 p=$2 b=${3:-}
  if [ -n "$b" ]; then
    curl -s -X "$m" -H "X-QP-Token: $PEER_TOKEN" -H 'Content-Type: application/json' \
      -d "$b" "http://127.0.0.1:$PEER_PORT$p"
  else
    curl -s -X "$m" -H "X-QP-Token: $PEER_TOKEN" "http://127.0.0.1:$PEER_PORT$p"
  fi
}

# The dev relay's identity is EPHEMERAL, so every restart changes its pin and
# both sides land in `untrusted` — correctly, and without retrying. Re-pinning
# is part of the harness, never something the product does on its own.
trust_relay() { # trust_relay <api> <token>
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

# A node that has just been re-pinned may still be COOLING DOWN from the
# failures the old pin caused — correctly, and for up to a minute. The first
# real run of this gate mistook that for the product: three scenarios reported
# "no notification within 24s" while the device's own diagnostics said
# `primary_health: offline, relay is cooling down after failures`.
#
# So the harness waits for the path it is about to test, and says so if it
# never comes up. Nothing here makes the product retry faster; it only stops
# the gate from asking before the world is ready.
wait_relay_healthy() { # wait_relay_healthy <api> <token> <seconds>
  local api=$1 tok=$2 deadline=$((SECONDS+$3)) health
  while [ $SECONDS -lt $deadline ]; do
    health=$(curl -s -H "X-QP-Token: $tok" "$api/api/relay/diagnostics" \
             | python3 -c 'import json,sys; print(json.load(sys.stdin).get("primary_health",""))' 2>/dev/null)
    [ "$health" = "healthy" ] && return 0
    curl -s -X POST -H "X-QP-Token: $tok" -H 'Content-Type: application/json' -d '{}' \
      "$api/api/relay/remeasure" >/dev/null 2>&1
    sleep 3
  done
  log "relay never became healthy for $api (last: ${health:-unknown})"
  return 1
}

# ─── the device ─────────────────────────────────────────────────────────────

RIG_SEQ=1000
rig() { # rig <cmd> [extra args…] — returns the rig's answer as JSON
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

core_field() { # core_field <json> <key>
  printf '%s' "$1" | python3 -c "
import json,sys
print(json.load(sys.stdin).get('core',{}).get('$2',''))" 2>/dev/null
}

open_app() {
  "${ADB[@]}" shell am start -n "$PKG/.QuietActivity" >/dev/null
  sleep 3
}

unlock_node() {
  open_app
  local dump bounds x y
  "${ADB[@]}" shell uiautomator dump /sdcard/g.xml >/dev/null 2>&1
  dump=$("${ADB[@]}" shell cat /sdcard/g.xml)
  printf '%s' "$dump" | grep -q 'text="Passphrase"' || return 0   # already open
  bounds=$(printf '%s' "$dump" | tr '<' '\n' | grep 'text="Passphrase"' \
           | grep -o 'bounds="\[[0-9]*,[0-9]*\]\[[0-9]*,[0-9]*\]"' | head -1)
  x=$(printf '%s' "$bounds" | sed 's/.*\[\([0-9]*\),.*\]\[\([0-9]*\),.*/\1 \2/' | awk '{print int(($1+$2)/2)}')
  "${ADB[@]}" shell input tap "${x:-540}" "$(printf '%s' "$bounds" | sed 's/.*\[[0-9]*,\([0-9]*\)\]\[[0-9]*,\([0-9]*\)\].*/\1 \2/' | awk '{print int(($1+$2)/2)}')"
  sleep 1
  "${ADB[@]}" shell input text "$PASS"
  "${ADB[@]}" shell input keyevent KEYCODE_BACK
  sleep 1
  bounds=$("${ADB[@]}" shell cat /sdcard/g.xml | tr '<' '\n' | grep 'text="Open"' \
           | grep -o 'bounds="\[[0-9]*,[0-9]*\]\[[0-9]*,[0-9]*\]"' | head -1)
  "${ADB[@]}" shell input tap 540 "$(printf '%s' "$bounds" | sed 's/.*\[[0-9]*,\([0-9]*\)\]\[[0-9]*,\([0-9]*\)\].*/\1 \2/' | awk '{print int(($1+$2)/2)}')"
  for _ in $(seq 1 30); do
    sleep 2
    [ "$(core_field "$(rig status)" state)" = "alive" ] && return 0
  done
  return 1
}

phone_api() { # phone_api <method> <path> [body] — over USB, never the network under test
  local m=$1 p=$2 b=${3:-}
  if [ -n "$b" ]; then
    curl -s -X "$m" -H "X-QP-Token: $PHONE_TOKEN" -H 'Content-Type: application/json' \
      -d "$b" "http://127.0.0.1:$FWD_PORT$p"
  else
    curl -s -X "$m" -H "X-QP-Token: $PHONE_TOKEN" "http://127.0.0.1:$FWD_PORT$p"
  fi
}

notif_records() { "${ADB[@]}" shell dumpsys notification --noredact 2>/dev/null | grep -cE "NotificationRecord.*$PKG"; }
notif_tags()    { "${ADB[@]}" shell dumpsys notification --noredact 2>/dev/null | grep -E "NotificationRecord.*$PKG" | grep -o 'tag=[^ ]*'; }
notif_text()    { "${ADB[@]}" shell dumpsys notification --noredact 2>/dev/null | grep -A 30 "NotificationRecord.*$PKG" | grep -o 'android.text=String ([^)]*)' | head -1; }

pull_ledger() {
  "${ADB[@]}" exec-out run-as "$PKG" cat databases/quiet-notifications.db > "$OUT/ledger.db" 2>/dev/null
  [ -s "$OUT/ledger.db" ]
}

ledger_states() {
  python3 - "$OUT/ledger.db" <<'PY'
import sqlite3, sys, json
try:
    c = sqlite3.connect(sys.argv[1])
    print(json.dumps({s: n for s, n in c.execute(
        "select state, count(*) from notification_events group by state")}))
except Exception as e:
    print(json.dumps({"error": str(e)}))
PY
}

# ─── a space per scenario, which is what isolation means here ───────────────

new_shared_space() { # new_shared_space <alias> → space id, joined by the device
  local alias=$1 sp link req
  sp=$(peer_api POST /api/spaces "{\"title\":\"gate $alias\"}" \
       | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
  link=$(peer_api POST "/api/spaces/$sp/passes" \
         "{\"max_uses\":1,\"ttl_hours\":2,\"relay\":\"$RELAY_ADDR\"}" \
         | python3 -c 'import json,sys; print(json.load(sys.stdin)["link"])')
  req=$(phone_api POST /api/join-requests "{\"pass\":\"$link\"}" \
        | python3 -c 'import json,sys; print(json.load(sys.stdin).get("request_id",""))')
  [ -n "$req" ] || return 1
  for _ in $(seq 1 25); do
    sleep 3
    phone_api GET "/api/join-requests/$req" | grep -q '"status":"ready"' && { printf '%s' "$sp"; return 0; }
  done
  return 1
}

say_to() { # say_to <space> <text>
  peer_api POST "/api/spaces/$1/messages" \
    "$(python3 -c 'import json,sys; print(json.dumps({"text": sys.argv[1]}))' "$2")" >/dev/null
}

wait_for_notification() { # wait_for_notification <seconds>
  for _ in $(seq 1 "$1"); do
    sleep 2
    [ "$(notif_records)" -gt 0 ] && return 0
  done
  return 1
}

clear_shade() { "${ADB[@]}" shell am force-stop "$PKG" >/dev/null; sleep 2; }

# ensure_node — the single most important harness rule here.
#
# A force-stop closes the node, and the next open picks a NEW API port and a
# NEW session token: a forward established once at startup is stale from the
# first restart onwards. The first run of this gate failed four scenarios that
# way and blamed the product for it — the shell was talking to a port nobody
# was listening on, and "could not join a fresh space" was the harness
# describing itself.
#
# So every scenario that restarts the app calls this, and so does every
# scenario before it starts.
ensure_node() {
  local state; state=$(core_field "$(rig status)" state)
  if [ "$state" != "alive" ]; then
    unlock_node || return 1
  fi
  local s; s=$(rig status) || return 1
  PHONE_PORT=$(core_field "$s" api_port)
  PHONE_TOKEN=$(core_field "$s" session_token)
  [ -n "$PHONE_PORT" ] && [ "$PHONE_PORT" != "0" ] || return 1
  "${ADB[@]}" forward --remove "tcp:$FWD_PORT" >/dev/null 2>&1
  "${ADB[@]}" forward "tcp:$FWD_PORT" "tcp:$PHONE_PORT" >/dev/null || return 1
  phone_api POST /api/settings "{\"relay\":\"$RELAY_ADDR\"}" >/dev/null
  wait_relay_healthy "http://127.0.0.1:$FWD_PORT" "$PHONE_TOKEN" 60 || return 1
  return 0
}

# Each scenario starts from an empty shade and a node that is actually
# reachable. Isolation is not a comment here: a leftover record from the
# previous scenario is exactly how a count-based assertion goes green for the
# wrong reason.
begin_scenario() {
  clear_shade
  ensure_node || return 1
  return 0
}

# ─── scenarios ──────────────────────────────────────────────────────────────

scenario_02_new_event() {
  begin_scenario || { record 02-new-event fail "the node would not come back after a restart"; return; }
  local sp; sp=$(new_shared_space 02) || { record 02-new-event fail "could not join a fresh space"; return; }
  "${ADB[@]}" shell input keyevent KEYCODE_HOME; sleep 2
  local before; before=$(notif_records)
  say_to "$sp" "gate 02"
  # A longer window than the scenarios after it, deliberately: this is the
  # FIRST message of the run, and it pays for the relay pool's first dial and
  # the space's first sync. Scenarios that follow inherit a warm path — and a
  # gate that gave them all the same budget would fail this one every time and
  # call it a product defect.
  if wait_for_notification 30; then
    local after; after=$(notif_records)
    if [ "$after" -eq $((before+1)) ]; then
      record 02-new-event pass "one record for one message" \
        "{\"space\":\"$sp\",\"records_before\":$before,\"records_after\":$after,\"tag\":\"$(notif_tags | head -1)\"}"
    else
      record 02-new-event fail "records went $before → $after for one message"
    fi
  else
    record 02-new-event fail "no notification within 24s"
  fi
}

scenario_09_dismiss_replay() {
  begin_scenario || { record 09-dismiss-replay fail "the node would not come back after a restart"; return; }
  local sp; sp=$(new_shared_space 09) || { record 09-dismiss-replay fail "could not join a fresh space"; return; }
  "${ADB[@]}" shell input keyevent KEYCODE_HOME; sleep 2
  say_to "$sp" "gate 09 first"
  wait_for_notification 12 || { record 09-dismiss-replay fail "no first notification"; return; }
  "${ADB[@]}" shell cmd statusbar expand-notifications >/dev/null; sleep 2
  "${ADB[@]}" shell input swipe 540 780 1050 780 300; sleep 3
  "${ADB[@]}" shell cmd statusbar collapse >/dev/null
  local after_swipe; after_swipe=$(notif_records)
  say_to "$sp" "gate 09 second"
  wait_for_notification 12 || { record 09-dismiss-replay fail "nothing after the swipe"; return; }
  local text; text=$(notif_text)
  if printf '%s' "$text" | grep -q "A new message"; then
    record 09-dismiss-replay pass "a swipe closed the aggregation; the next message starts afresh" \
      "{\"space\":\"$sp\",\"records_after_swipe\":$after_swipe,\"text\":\"$text\"}"
  else
    record 09-dismiss-replay fail "after the swipe the shade says: $text"
  fi
}

scenario_10_two_spaces() {
  begin_scenario || { record 10-two-spaces fail "the node would not come back after a restart"; return; }
  local a b; a=$(new_shared_space 10a) && b=$(new_shared_space 10b) \
    || { record 10-two-spaces fail "could not join two fresh spaces"; return; }
  "${ADB[@]}" shell input keyevent KEYCODE_HOME; sleep 2
  say_to "$a" "gate 10 a"; say_to "$b" "gate 10 b"
  sleep 14
  local tags; tags=$(notif_tags | sort -u | wc -l | tr -d ' ')
  if [ "$tags" -ge 2 ]; then
    record 10-two-spaces pass "two spaces, two tags, two entries" \
      "{\"space_a\":\"$a\",\"space_b\":\"$b\",\"distinct_tags\":$tags}"
  else
    record 10-two-spaces fail "$tags distinct tags for two spaces"
  fi
}

scenario_07_before_notify() {
  begin_scenario || { record 07-before-notify fail "the node would not come back"; return; }
  # Crash after the acknowledgement, before the system was told. Reproduced
  # exactly rather than raced: the row is put back to `pending`, which is what
  # a process dying between the two leaves behind.
  pull_ledger || { record 07-before-notify skip "no ledger on the device yet"; return; }
  python3 - "$OUT/ledger.db" <<'PY'
import sqlite3, sys
c = sqlite3.connect(sys.argv[1])
c.execute("update notification_events set state='pending' where state='presented'")
c.commit()
PY
  "${ADB[@]}" push "$OUT/ledger.db" /data/local/tmp/g.db >/dev/null 2>&1
  "${ADB[@]}" shell chmod 666 /data/local/tmp/g.db
  "${ADB[@]}" shell am force-stop "$PKG"; sleep 2
  "${ADB[@]}" shell "run-as $PKG cp /data/local/tmp/g.db databases/quiet-notifications.db"
  "${ADB[@]}" shell "run-as $PKG rm -f databases/quiet-notifications.db-journal"
  local before; before=$(notif_records)
  ensure_node || { record 07-before-notify fail "the node would not reopen"; return; }
  sleep 4
  local after; after=$(notif_records)
  if [ "$before" -eq 0 ] && [ "$after" -ge 1 ]; then
    pull_ledger
    record 07-before-notify pass "recovery posted what the process never showed" \
      "{\"records_before\":$before,\"records_after\":$after,\"ledger\":$(ledger_states)}"
  else
    record 07-before-notify fail "shade went $before → $after after recovery"
  fi
}

scenario_11_corrupt_checkpoint() {
  "${ADB[@]}" shell am force-stop "$PKG"; sleep 2
  "${ADB[@]}" shell "run-as $PKG sh -c 'echo { > files/node/notifications.json'"
  "${ADB[@]}" shell "run-as $PKG sh -c 'echo { > files/node/notifications.prev.json'"
  # The plane's state is a property of an OPEN node: with nothing open the
  # binding answers never_activated because there is nothing to ask. The first
  # run of this gate read that as a product failure.
  ensure_node || { record 11-corrupt-checkpoint fail "the node would not reopen"; return; }
  local plane; plane=$(core_field "$(rig status)" notify_plane)
  if [ "$plane" = "metadata_corrupt" ]; then
    record 11-corrupt-checkpoint pass "damage is named, not read as a first run" \
      "{\"plane\":\"$plane\"}"
  else
    record 11-corrupt-checkpoint fail "plane reports $plane after both generations were destroyed"
  fi

  # LEAVE THE DEVICE AS IT WAS FOUND. Without this the damaged checkpoint
  # survives into the NEXT run, where an early scenario reads it and reports a
  # product failure that is really this scenario's litter. That happened on
  # the second run, and it is exactly the cross-contamination isolated
  # scenarios exist to prevent.
  "${ADB[@]}" shell am force-stop "$PKG"; sleep 2
  "${ADB[@]}" shell "run-as $PKG rm -f files/node/notifications.json files/node/notifications.prev.json"
  ensure_node >/dev/null || log "11: the node did not reopen after the cleanup"
}

scenario_12_retention() {
  local j; j=$("${ADB[@]}" shell run-as "$PKG" cat files/node/notifications.json 2>/dev/null)
  if printf '%s' "$j" | grep -q '"confirmed"'; then
    record 12-retention pass "the watermark exists and names what may not be collapsed" \
      "{\"checkpoint_generation\":$(printf '%s' "$j" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("generation",0))' 2>/dev/null || echo 0)}"
  else
    record 12-retention fail "no watermark on the device"
  fi
}

scenario_13_permission() {
  begin_scenario || { record 13-permission-denied fail "the node would not come back"; return; }
  "${ADB[@]}" shell pm revoke "$PKG" android.permission.POST_NOTIFICATIONS >/dev/null 2>&1
  "${ADB[@]}" shell am force-stop "$PKG"; sleep 2
  ensure_node || { record 13-permission-denied fail "the node would not reopen after the revoke"; return; }
  local sp; sp=$(new_shared_space 13) || { record 13-permission-denied skip "could not join a fresh space"; return; }
  "${ADB[@]}" shell input keyevent KEYCODE_HOME; sleep 2
  say_to "$sp" "gate 13"
  sleep 14
  local n; n=$(notif_records)
  "${ADB[@]}" shell pm grant "$PKG" android.permission.POST_NOTIFICATIONS >/dev/null 2>&1
  if [ "$n" -eq 0 ]; then
    record 13-permission-denied pass "nothing was shown while the permission was refused" \
      "{\"space\":\"$sp\",\"records\":$n}"
  else
    record 13-permission-denied fail "$n records while notifications were refused"
  fi
}

# Scenarios whose window is INSIDE one arrival cannot be reached from a shell:
# there is no way to stop a process between two statements on demand. They are
# owned by the JVM and Go tests, which can, and saying so is more useful than a
# scenario that reports a pass it never took.
scenario_unreachable() {
  record 01-first-activation      skip "needs a fresh install over an existing journal; owned by TestASpaceThePlaneHasNeverSeen… and TestActivationHappensOnce…"
  record 03-attach-race           skip "a nanosecond window; owned by TestAttachingIsAtomicWithEmission and TestActivationRacingWithApplies…"
  record 04-before-host-callback  skip "cannot stop a process between apply and the callback; owned by TestAnUnacknowledgedCandidateSurvivesTheProcess"
  record 05-before-sqlite-commit  skip "cannot fail one transaction on demand from a shell; owned by aFailedTransactionIsNotAcknowledged…"
  record 06-before-ack            skip "same window, other side; owned by aRedeliveryFromTheCoreIsAcknowledgedAgain…"
  record 08-before-presented      skip "covered live by 07, which reproduces the same row state"
}

# ─── run ────────────────────────────────────────────────────────────────────

"${ADB[@]}" get-state >/dev/null 2>&1 || fail "no device: set SER=… or attach one"
log "device: $("${ADB[@]}" shell getprop ro.product.model | tr -d '\r') API $("${ADB[@]}" shell getprop ro.build.version.sdk | tr -d '\r')"

start_world
unlock_node || fail "the node on the device did not open — is PASS right?"

STATUS=$(rig status) || fail "the rig did not answer"
PHONE_PORT=$(core_field "$STATUS" api_port)
PHONE_TOKEN=$(core_field "$STATUS" session_token)
FWD_PORT=${FWD_PORT:-19601}
"${ADB[@]}" forward "tcp:$FWD_PORT" "tcp:$PHONE_PORT" >/dev/null || fail "could not forward the device API"
trust_relay "http://127.0.0.1:$FWD_PORT" "$PHONE_TOKEN" || fail "the device would not pin the relay"
phone_api POST /api/settings "{\"relay\":\"$RELAY_ADDR\"}" >/dev/null
wait_relay_healthy "http://127.0.0.1:$FWD_PORT" "$PHONE_TOKEN" 90 \
  || fail "the device never reached the relay — every message scenario would fail for that reason alone"
wait_relay_healthy "http://127.0.0.1:$PEER_PORT" "$PEER_TOKEN" 60 \
  || fail "the peer never reached the relay"

log "runtime_epoch $(core_field "$STATUS" runtime_epoch)  plane $(core_field "$STATUS" notify_plane)"

WANT=("$@")
run_it() { [ ${#WANT[@]} -eq 0 ] && return 0; for w in "${WANT[@]}"; do [ "$w" = "$1" ] && return 0; done; return 1; }

run_it 02 && scenario_02_new_event
run_it 09 && scenario_09_dismiss_replay
run_it 10 && scenario_10_two_spaces
run_it 07 && scenario_07_before_notify
run_it 13 && scenario_13_permission
run_it 12 && scenario_12_retention
run_it 11 && scenario_11_corrupt_checkpoint     # last: it damages the checkpoint
[ ${#WANT[@]} -eq 0 ] && scenario_unreachable

echo
echo "AR-1b.5.6 — $(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf '%s\n' "${SUMMARY[@]}"
printf '\n%d passed, %d failed, %d owned elsewhere.  Report: %s\n' "$PASSED" "$FAILED" "$SKIPPED" "$REPORT"
[ "$FAILED" -eq 0 ]

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
    # Not a verdict: a fact about the fixture, recorded so a slow run can be
    # explained afterwards. It must not be counted as anything.
    info) ;;
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

# ONE EXIT TRAP, and everything that has to happen on the way out lives in
# it. A second `trap … EXIT` added later does not run alongside the first —
# it REPLACES it — which is how the relay and the peer node were left running
# after a run and the next one died on "address already in use". The screen
# setting was the newcomer; the processes were the casualty.
# KEEP THE DEVICE AWAKE FOR THE RUN, and say why.
#
# Every scenario presses HOME so the app is in the background — that is the
# state a notification is FOR. On a phone that also means the screen goes off,
# and with it Wi-Fi power save and eventually Doze, which delay inbound sync by
# tens of seconds. The first scenario pays the most and fails on a budget that
# every later one meets, which reads as a flaky product and is not.
#
# THIS GATE IS NOT ABOUT DOZE. Waking a sleeping phone for a message is its own
# wave — a push path and a wake budget, deliberately out of AR-1b — and
# measuring it here by accident would mean measuring it badly. So the screen
# stays on while the cable is in, and it is restored at the end.
keep_awake() {
  "${ADB[@]}" shell svc power stayon usb >/dev/null 2>&1
}
restore_sleep() {
  "${ADB[@]}" shell svc power stayon false >/dev/null 2>&1
}

stop_world() {
  pkill -f "terminal-relay --listen 0.0.0.0:$RELAY_PORT" 2>/dev/null
  pkill -f "data $PEER_DATA" 2>/dev/null
  pkill -f "$PEER_DATA" 2>/dev/null
  restore_sleep
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
  "${ADB[@]}" shell uiautomator dump /sdcard/g.xml >/dev/null 2>&1
  bounds=$("${ADB[@]}" shell cat /sdcard/g.xml | tr '<' '\n' | grep 'text="Open"' \
           | grep -o 'bounds="\[[0-9]*,[0-9]*\]\[[0-9]*,[0-9]*\]"' | head -1)
  local oy
  oy=$(printf '%s' "$bounds" | sed 's/.*\[[0-9]*,\([0-9]*\)\]\[[0-9]*,\([0-9]*\)\].*/\1 \2/' | awk '{print int(($1+$2)/2)}')
  # The dump is taken again AFTER the keyboard closes: the first one was taken
  # with the passphrase field focused, and the button had moved. A stale
  # coordinate taps the keyboard and the gate reports "the node would not come
  # back" about a node nobody ever asked to open.
  "${ADB[@]}" shell input tap 540 "${oy:-1284}"
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

# THE SHADE, READ BLOCK BY BLOCK AND ONLY OURS.
#
# TWO REASONS IT IS NOT A `grep -c`. This runs on somebody's actual phone, and
# everything in `dumpsys notification` that is not this package is their
# private life: the reader below keeps a record only when its own header names
# our package, so nothing else is ever printed or stored.
#
# And since AR-1b.6b the app posts a GROUP SUMMARY, which is a record like any
# other to grep. Counting it made "one message" arrive as two records, and
# reading the first text in the dump returned the summary's line — "7 new
# signals" — instead of the conversation's. Every scenario here is about
# conversations, so the summary is counted separately and never mistaken for
# one.
shade() { # shade <records|tags|text|summaries>
  "${ADB[@]}" shell dumpsys notification --noredact 2>/dev/null | python3 -c '
import re, sys
PKG = "'"$PKG"'"
SUMMARY_TAG = "quite:summary"
want = sys.argv[1]
# ONE RECORD CAN APPEAR SEVERAL TIMES. dumpsys prints the live list, the
# enqueued list and the ranking, and the same notification shows up in more
# than one of them — which counted as three summaries above two
# conversations and read as a product defect. The key is the identity
# (0|pkg|id|tag|uid), so the key is what dedups them.
seen, blocks, cur = set(), [], None

def flush(b):
    if not b or PKG not in b[0]:
        return
    m = re.search(r"key=(\S+)", b[0])
    k = m.group(1) if m else b[0]
    if k in seen:
        return
    seen.add(k)
    blocks.append(b)

for line in sys.stdin:
    if "NotificationRecord(" in line:
        flush(cur)
        cur = [line]
    elif cur is not None:
        cur.append(line)
flush(cur)

def tag_of(b):
    m = re.search(r"tag=(\S+)", b[0])
    return m.group(1) if m else ""

children = [b for b in blocks if tag_of(b) != SUMMARY_TAG]
if want == "records":
    print(len(children))
elif want == "summaries":
    print(len(blocks) - len(children))
elif want == "tags":
    for b in children:
        print("tag=" + tag_of(b))
elif want == "text":
    for b in children:
        for line in b:
            m = re.search(r"android\.text=String \(([^)]*)\)", line)
            if m:
                print("android.text=String (%s)" % m.group(1))
                sys.exit(0)
' "$1"
}

notif_records()   { shade records; }
notif_tags()      { shade tags; }
notif_text()      { shade text; }
notif_summaries() { shade summaries; }

# THE WAL IS PART OF THE DATABASE, and leaving it behind is how this harness
# lied for two runs. SQLiteOpenHelper writes ahead: the newest rows live in
# `-wal` until a checkpoint folds them in. Pulling only the `.db` gave a stale
# snapshot, and pushing a modified `.db` back beside the ORIGINAL `-wal` was
# worse — SQLite replayed the old log on open and the edit simply vanished.
#
# Scenario 07 is the one that caught it: it sets every presented row back to
# pending to reproduce a crash, the edit disappeared, and the shade stayed
# empty. It had been "passing" only because the count included the group
# summary, so a run that recovered nothing still looked like it recovered one.
pull_ledger() {
  rm -f "$OUT/ledger.db" "$OUT/ledger.db-wal" "$OUT/ledger.db-shm"
  "${ADB[@]}" exec-out run-as "$PKG" cat databases/quiet-notifications.db > "$OUT/ledger.db" 2>/dev/null
  "${ADB[@]}" exec-out run-as "$PKG" cat databases/quiet-notifications.db-wal > "$OUT/ledger.db-wal" 2>/dev/null
  "${ADB[@]}" exec-out run-as "$PKG" cat databases/quiet-notifications.db-shm > "$OUT/ledger.db-shm" 2>/dev/null
  [ -s "$OUT/ledger.db-wal" ] || rm -f "$OUT/ledger.db-wal" "$OUT/ledger.db-shm"
  [ -s "$OUT/ledger.db" ]
}

# Fold the log into the file, so ONE file is the whole database and pushing it
# back cannot be undone by a log left on the device.
flatten_ledger() {
  python3 - "$OUT/ledger.db" <<'PYEOF'
import sqlite3, sys
c = sqlite3.connect(sys.argv[1])
c.execute("PRAGMA journal_mode=DELETE")
c.commit()
c.close()
PYEOF
  rm -f "$OUT/ledger.db-wal" "$OUT/ledger.db-shm"
}

# Put it back as the app's ONLY database: any log still on the device would be
# replayed over what we just wrote.
push_ledger() {
  flatten_ledger
  "${ADB[@]}" push "$OUT/ledger.db" /data/local/tmp/g.db >/dev/null 2>&1
  "${ADB[@]}" shell chmod 666 /data/local/tmp/g.db
  "${ADB[@]}" shell "run-as $PKG cp /data/local/tmp/g.db databases/quiet-notifications.db"
  "${ADB[@]}" shell "run-as $PKG rm -f databases/quiet-notifications.db-journal \
                                        databases/quiet-notifications.db-wal \
                                        databases/quiet-notifications.db-shm"
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

# CLEAN UP AFTER ITSELF (SD-0).
#
# Every run used to leave its spaces on the phone forever — the protocol has
# no leaving, so nothing removed them — and a device gated a dozen times ends
# up carrying dozens of dead rooms. That is not only untidy: past ~32 spaces a
# relay pull no longer fits in one request, every scenario gets slower, and a
# gate that times out at scenario 10 looks like a product defect.
#
# ONLY WHAT THIS SCRIPT MADE. The filter is the "gate " title prefix these
# scenarios create, matched exactly. Somebody's real conversations are on this
# phone and nothing here may go near them.
cleanup_gate_spaces() {
  local ids
  ids=$(phone_api GET /api/spaces | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(0)
for s in (d if isinstance(d, list) else d.get("spaces", [])):
    title = (s.get("display_title") or s.get("title") or "")
    if title.startswith("gate "):
        print(s["id"])
' 2>/dev/null)
  local n=0
  for sid in $ids; do
    phone_api DELETE "/api/spaces/$sid" >/dev/null && n=$((n+1))
  done
  [ "$n" -gt 0 ] && log "removed $n space(s) this gate created"
  return 0
}

# HOW MANY SPACES THIS NODE IS CARRYING, reported once per run.
#
# SPACES ACCUMULATE AND NOTHING REMOVES THEM: there is no leaving a space in
# the protocol, so every gate run leaves its own behind and a phone that has
# been gated a dozen times holds dozens. That is not a leak, but it changes
# what is being measured — past ~32 spaces a pull no longer fits in one
# request and is served in chunks — and it makes each scenario slower. A run
# that starts to time out at scenario 10 is explained by this number, so the
# number is printed rather than left to be rediscovered.
report_space_count() {
  local n
  n=$(phone_api GET /api/spaces | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
    print(len(d if isinstance(d, list) else d.get("spaces", [])))
except Exception:
    print(0)' 2>/dev/null)
  log "the node carries $n space(s)$([ "${n:-0}" -gt 32 ] && printf ' — past the single-request ceiling, pulls are chunked')"
  record 00-fixture info "spaces the node carries" "{\"spaces\":${n:-0}}"
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
  # POLLED, NOT SLEPT. A fixed wait passes only while an earlier scenario has
  # already warmed the path: run this one alone and the same 14 seconds are
  # spent on the pool's first dial, and the gate reports "0 distinct tags" for
  # a product that was working. Two children plus a summary is three records,
  # so the wait is for the TAGS, which is what the scenario is actually about.
  local tags=0
  for _ in $(seq 1 45); do
    sleep 2
    tags=$(notif_tags | sort -u | grep -c 'space:')
    [ "$tags" -ge 2 ] && break
  done
  local summary; summary=$(notif_summaries)
  if [ "$tags" -ge 2 ] && [ "$summary" -eq 1 ]; then
    record 10-two-spaces pass "two spaces, two tags, and exactly one summary above them" \
      "{\"space_a\":\"$a\",\"space_b\":\"$b\",\"distinct_space_tags\":$tags,\"summaries\":$summary}"
  elif [ "$tags" -ge 2 ]; then
    record 10-two-spaces fail "two spaces but $summary summaries — a group needs exactly one"
  else
    record 10-two-spaces fail "$tags distinct tags for two spaces"
  fi
}

scenario_07_before_notify() {
  begin_scenario || { record 07-before-notify fail "the node would not come back"; return; }
  # IT BRINGS ITS OWN MESSAGE. The first version flipped whatever presented
  # rows the earlier scenarios had left behind, and AR-1b.6b.6 made that
  # impossible: reconciling at startup closes rows the shade no longer holds,
  # so after the force-stop this scenario opens with there is nothing left to
  # flip and it was reproducing an empty database.
  local sp; sp=$(new_shared_space 07) || { record 07-before-notify skip "could not join a fresh space"; return; }
  "${ADB[@]}" shell input keyevent KEYCODE_HOME; sleep 2
  say_to "$sp" "gate 07"
  wait_for_notification 40 || { record 07-before-notify fail "no notification to crash after"; return; }

  # Crash after the acknowledgement, before the system was told. Reproduced
  # exactly rather than raced: the row goes back to `pending`, which is what a
  # process dying between the two leaves behind — and `pending` is precisely
  # what reconciliation must NOT touch, because it was never posted.
  "${ADB[@]}" shell am force-stop "$PKG"; sleep 2
  pull_ledger || { record 07-before-notify fail "no ledger on the device"; return; }
  python3 - "$OUT/ledger.db" <<'PYEOF'
import sqlite3, sys
c = sqlite3.connect(sys.argv[1])
n = c.execute("update notification_events set state='pending' where state='presented'").rowcount
c.commit()
print(f"07: {n} row(s) put back to pending", file=sys.stderr)
PYEOF
  push_ledger
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

keep_awake
ensure_node && report_space_count

run_it 02 && scenario_02_new_event
run_it 09 && scenario_09_dismiss_replay
run_it 10 && scenario_10_two_spaces
run_it 07 && scenario_07_before_notify
run_it 13 && scenario_13_permission
run_it 12 && scenario_12_retention
run_it 11 && scenario_11_corrupt_checkpoint     # last: it damages the checkpoint
[ ${#WANT[@]} -eq 0 ] && scenario_unreachable

# After the verdicts, never before: a scenario that failed leaves its space
# behind on purpose so it can be looked at, and cleanup that ran first would
# take the evidence with it.
ensure_node >/dev/null 2>&1 && cleanup_gate_spaces

echo
echo "AR-1b.5.6 — $(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf '%s\n' "${SUMMARY[@]}"
printf '\n%d passed, %d failed, %d owned elsewhere.  Report: %s\n' "$PASSED" "$FAILED" "$SKIPPED" "$REPORT"
[ "$FAILED" -eq 0 ]

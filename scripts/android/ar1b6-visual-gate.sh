#!/usr/bin/env bash
# AR-1b.6b.6 — the visual acceptance, and the leak scan that goes with it.
#
# WHAT THIS IS FOR, AND WHAT IT IS NOT. The structural tests already prove what
# Android HOLDS: instrumented tests read getActiveNotifications() and the
# shortcut surfaces and assert on their contents. None of that answers the only
# question left — what a person SEES. So this one takes photographs, and every
# photograph is paired with a machine check of the same moment, because a
# screenshot proves what appeared and cannot prove what did not.
#
# THE SENTINELS ARE THE WHOLE METHOD. Asserting that "Studio" is absent proves
# nothing if the label was never set. Every space, sender and message here
# carries a string that exists nowhere else on the device, so its absence is a
# fact and its presence is a leak with a name.
#
# USAGE
#   SER=P21321000131 ./scripts/android/ar1b6-visual-gate.sh
#   OUT=/tmp/b6b6 SER=… ./scripts/android/ar1b6-visual-gate.sh
#
# Leaves numbered PNGs in $OUT/shots and one JSONL report. The photographs are
# for a person to look at; the report is what fails the run.
set -uo pipefail

SER=${SER:-}
PKG=${PKG:-quite.space}
PASS=${PASS:-ar1b-gate-passphrase}
RELAY_PORT=${RELAY_PORT:-7421}
PEER_PORT=${PEER_PORT:-8812}
PEER_DATA=${PEER_DATA:-/tmp/ar1b6-peer}
OUT=${OUT:-/tmp/ar1b6-visual}
FWD_PORT=${FWD_PORT:-9942}

ADB=(adb)
[ -n "$SER" ] && ADB=(adb -s "$SER")

SHOTS="$OUT/shots"
REPORT="$OUT/report.jsonl"
rm -rf "$SHOTS"; mkdir -p "$SHOTS"; : > "$REPORT"

# Strings that exist nowhere else. The space name is what SPACE may show, the
# sender and the text are what only PREVIEW may show.
SPACE_A="ROOM_ALPHA_c41d"
SPACE_B="ROOM_BETA_9e07"
SENDER_ONE="SENDER_FIRST_5b2a"
SENDER_TWO="SENDER_RENAMED_77f1"
TEXT_ONE="MESSAGE_TEXT_a913"
TEXT_TWO="MESSAGE_SECOND_4c66"

PASSED=0; FAILED=0
declare -a SUMMARY

log()  { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
fail() { printf 'FATAL: %s\n' "$*" >&2; exit 1; }

record() { # record <check> <verdict> <reason> [extra json]
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
    *) ;;   # info and skip are facts, not verdicts
  esac
  SUMMARY+=("$(printf '%-34s %-6s %s' "$check" "$verdict" "$reason")")
  log "$check → $verdict: $reason"
}

shot() { # shot <name> — a photograph for a person, numbered in order
  SHOT_N=$((${SHOT_N:-0}+1))
  local f
  f=$(printf '%s/%02d-%s.png' "$SHOTS" "$SHOT_N" "$1")
  "${ADB[@]}" exec-out screencap -p > "$f"
  printf '%s' "$f"
}

# ─── the two other halves of the world ──────────────────────────────────────

LAN_IP=$(ipconfig getifaddr en0 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}')
[ -n "$LAN_IP" ] || fail "no LAN address; the phone cannot reach a relay on this machine"
RELAY_ADDR="$LAN_IP:$RELAY_PORT"

start_world() {
  log "relay on $RELAY_ADDR"
  (go run ./cmd/terminal-relay --listen "0.0.0.0:$RELAY_PORT" > "$OUT/relay.log" 2>&1 &)
  sleep 6
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
  pkill -f "terminal-relay --listen 0.0.0.0:$RELAY_PORT" 2>/dev/null
  pkill -f "$PEER_DATA" 2>/dev/null
  "${ADB[@]}" shell svc power stayon false >/dev/null 2>&1
}
trap stop_world EXIT

PEER_TOKEN=${PEER_TOKEN:-ar1b6token}
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

# ─── the device ─────────────────────────────────────────────────────────────

RIG_SEQ=2000
rig() { # rig <cmd> [--es key value …]
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

# ONLY OUR OWN BLOCKS, DEDUPED BY KEY. Everything else in this dump is the
# phone owner's private life; and dumpsys prints one record in several
# sections, which once counted as three summaries above two conversations.
shade() { # shade <records|tags|summaries|strings>
  "${ADB[@]}" shell dumpsys notification --noredact 2>/dev/null | python3 -c '
import re, sys
PKG = "'"$PKG"'"
SUMMARY_TAG = "quite:summary"
want = sys.argv[1]
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
        flush(cur); cur = [line]
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
    for b in children: print(tag_of(b))
elif want == "strings":
    # Everything the notifications carry, ours only — the leak scan reads this.
    for b in blocks:
        for line in b:
            print(line.rstrip())
' "$1"
}

# Every shortcut label this app has published, for the same scan.
shortcut_text() {
  "${ADB[@]}" shell dumpsys shortcut 2>/dev/null | python3 -c '
import sys
PKG = "'"$PKG"'"
keep, out = False, []
for line in sys.stdin:
    s = line.strip()
    if s.startswith("Package: ") or s.startswith("Package "):
        keep = PKG in s
    if keep:
        out.append(line.rstrip())
print("\n".join(out))
'
}

# leak_scan <label> <forbidden…> — the machine half of every photograph.
leak_scan() {
  local label=$1; shift
  local haystack; haystack=$(shade strings; shortcut_text)
  local found=()
  for s in "$@"; do
    printf '%s' "$haystack" | grep -q -- "$s" && found+=("$s")
  done
  if [ ${#found[@]} -eq 0 ]; then
    record "$label" pass "nothing forbidden appears in any system surface"
  else
    record "$label" fail "leaked: ${found[*]}"
  fi
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
  # A JOIN TRAVELS OVER THE RELAY, so "the node is open" is not enough: it has
  # to have measured this relay and found it healthy, or the first pass is
  # posted into a connection that does not exist yet.
  wait_relay_healthy "http://127.0.0.1:$FWD_PORT" "$PHONE_TOKEN" 60 || return 1
  return 0
}

join_space() { # join_space <title> <sender name> → space id
  local title=$1 who=$2 sp link req
  peer_api POST /api/identity/name "{\"name\":\"$who\"}" >/dev/null
  sp=$(peer_api POST /api/spaces "{\"title\":\"$title\"}" \
       | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
  link=$(peer_api POST "/api/spaces/$sp/passes" \
         "{\"max_uses\":1,\"ttl_hours\":2,\"relay\":\"$RELAY_ADDR\"}" \
         | python3 -c 'import json,sys; print(json.load(sys.stdin)["link"])')
  local ans; ans=$(phone_api POST /api/join-requests "{\"pass\":\"$link\"}")
  req=$(printf '%s' "$ans" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("request_id",""))' 2>/dev/null)
  if [ -z "$req" ]; then
    # SAY WHAT CAME BACK. A join that fails silently sends whoever runs this
    # to read the relay's log for an answer the phone already gave.
    log "join refused: $(printf '%s' "$ans" | head -c 200)"
    return 1
  fi
  local st
  for _ in $(seq 1 30); do
    sleep 3
    st=$(phone_api GET "/api/join-requests/$req")
    printf '%s' "$st" | grep -q '"status":"ready"' && { printf '%s' "$sp"; return 0; }
  done
  log "join never became ready: $(printf '%s' "$st" | head -c 200)"
  return 1
}

# SAYING SOMETHING HAS TO BE CHECKED. The first version discarded the answer,
# so a peer that had died mid-run looked exactly like a phone that was not
# notifying: every check failed with "no notification arrived" while the relay
# had only ever received three items. A harness that cannot tell which side
# stopped sends whoever runs it to debug the wrong half.
say() {
  local ans
  ans=$(peer_api POST "/api/spaces/$1/messages" \
    "$(python3 -c 'import json,sys; print(json.dumps({"text": sys.argv[1]}))' "$2")")
  if ! printf '%s' "$ans" | grep -q '"id"'; then
    log "the peer did not accept a message: $(printf '%s' "$ans" | head -c 160)"
    return 1
  fi
  return 0
}

wait_for_records() { # wait_for_records <n> <seconds>
  local want=$1 secs=$2
  for _ in $(seq 1 "$secs"); do
    sleep 2
    [ "$(shade records)" -ge "$want" ] && return 0
  done
  return 1
}

# CLEARED WITHOUT KILLING ANYTHING. The first version force-stopped the app
# between checks for isolation, and paid the cold path every time: a fresh
# process, a fresh relay dial, a fresh sync — so the first message of each
# check missed a 25-second budget that every later one met, and the gate
# reported "no notification arrived" for a product that was working.
#
# Marking the conversations read is the same isolation for this gate's
# purposes: the shade is empty, the aggregation is closed, and the next
# message is genuinely the first of its generation.
# WHAT THE PHONE ACTUALLY RECEIVED, asked of the node rather than the shade.
#
# Every failure here has the same two possible causes and they need different
# answers: the message never arrived, or it arrived and was not shown. Without
# this the gate can only report the second, and three runs were spent guessing
# which one it was.
delivered_count() { # delivered_count <space id>
  phone_api GET "/api/spaces/$1/entries" \
    | python3 -c 'import json,sys
try: print(len(json.load(sys.stdin)))
except Exception: print(-1)' 2>/dev/null
}

# ONLY WHAT THIS GATE MADE. Its rooms carry sentinel names, so the filter is
# exact; somebody's real conversations are on this phone. Without it every run
# leaves two more dead rooms behind, and past ~32 spaces a relay pull no longer
# fits in one request.
cleanup_gate_spaces() {
  local ids n=0
  ids=$(phone_api GET /api/spaces | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(0)
for s in (d if isinstance(d, list) else d.get("spaces", [])):
    title = (s.get("display_title") or s.get("title") or "")
    if title.startswith("ROOM_ALPHA_") or title.startswith("ROOM_BETA_"):
        print(s["id"])
' 2>/dev/null)
  for sid in $ids; do
    phone_api DELETE "/api/spaces/$sid" >/dev/null && n=$((n+1))
  done
  [ "$n" -gt 0 ] && log "removed $n room(s) this gate created"
  return 0
}

clear_shade() {
  local sp
  for sp in "$@"; do rig read --es arg "$sp" >/dev/null; done
  sleep 3
}

# ─── the run ────────────────────────────────────────────────────────────────

log "device: $("${ADB[@]}" shell getprop ro.product.model | tr -d '\r') API $("${ADB[@]}" shell getprop ro.build.version.sdk | tr -d '\r')"
"${ADB[@]}" shell pm grant "$PKG" android.permission.POST_NOTIFICATIONS >/dev/null 2>&1
# Awake, and allowed to run in the background, for the length of the run.
# See allow_background for why the second one is bought rather than assumed.
keep_awake
allow_background

start_world
ensure_node || fail "the node on the phone would not open"

# Anything left by an earlier run goes first: two rooms per run add up, and a
# phone carrying dozens of them syncs slower than the budgets here assume.
cleanup_gate_spaces

A=$(join_space "$SPACE_A" "$SENDER_ONE") || fail "could not join the first space"
B=$(join_space "$SPACE_B" "$SENDER_ONE") || fail "could not join the second space"
log "spaces: A=$A B=$B"

# WARM BOTH ROOMS BEFORE ANY CHECK RUNS.
#
# A freshly joined space takes minutes to receive its first message on this
# path — the pass completes, and the first sync of a new space is its own
# round trip after that. This gate is about what a PERSON SEES, not about how
# long the first sync takes (which b.5 measures), and the difference showed up
# as "the second conversation never arrived" while the node reported zero
# entries in it: not a notification defect at all.
warm_space() { # warm_space <space id> <label>
  local sp=$1 label=$2 n
  say "$sp" "warming $label"
  for _ in $(seq 1 60); do
    sleep 3
    n=$(delivered_count "$sp")
    [ "${n:-0}" -gt 0 ] && { log "$label is warm after $n entr(y|ies)"; return 0; }
  done
  fail "$label never received anything; the checks below would be measuring sync"
}
warm_space "$A" "room A"
warm_space "$B" "room B"

# ---- 1. HIDDEN: a notification that says nothing about anything ------------
clear_shade "$A" "$B"
rig policy --es arg hidden >/dev/null
"${ADB[@]}" shell input keyevent KEYCODE_HOME; sleep 2
say "$A" "$TEXT_ONE"
if wait_for_records 1 40; then
  "${ADB[@]}" shell cmd statusbar expand-notifications >/dev/null; sleep 2
  shot hidden >/dev/null
  "${ADB[@]}" shell cmd statusbar collapse >/dev/null
  leak_scan "hidden.says-nothing" "$SPACE_A" "$SENDER_ONE" "$TEXT_ONE"
else
  record "hidden.says-nothing" fail "no notification arrived"
fi

# ---- 2. SPACE: the room may be named; nobody and nothing else -------------
clear_shade "$A" "$B"
rig policy --es arg space >/dev/null
"${ADB[@]}" shell input keyevent KEYCODE_HOME; sleep 2
say "$A" "$TEXT_TWO"
if wait_for_records 1 40; then
  "${ADB[@]}" shell cmd statusbar expand-notifications >/dev/null; sleep 2
  shot space >/dev/null
  "${ADB[@]}" shell cmd statusbar collapse >/dev/null
  if shade strings | grep -q -- "$SPACE_A"; then
    leak_scan "space.names-only-the-room" "$SENDER_ONE" "$TEXT_TWO"
  else
    record "space.names-only-the-room" fail "the space is not named at all"
  fi
else
  record "space.names-only-the-room" fail "no notification arrived"
fi

# ---- 3. PREVIEW: the conversation, in full, and on purpose ---------------
clear_shade "$A" "$B"
rig policy --es arg preview >/dev/null
"${ADB[@]}" shell input keyevent KEYCODE_HOME; sleep 2
say "$A" "$TEXT_ONE"
if wait_for_records 1 40; then
  "${ADB[@]}" shell cmd statusbar expand-notifications >/dev/null; sleep 3
  shot preview >/dev/null
  "${ADB[@]}" shell cmd statusbar collapse >/dev/null
  missing=()
  for s in "$SPACE_A" "$SENDER_ONE" "$TEXT_ONE"; do
    shade strings | grep -q -- "$s" || missing+=("$s")
  done
  if [ ${#missing[@]} -eq 0 ]; then
    record "preview.shows-the-conversation" pass "space, sender and message are all there"
  else
    record "preview.shows-the-conversation" fail "preview is missing: ${missing[*]}"
  fi
else
  record "preview.shows-the-conversation" fail "no notification arrived"
fi

# ---- 4. two conversations, one summary -----------------------------------
#
# A LONGER BUDGET THAN THE OTHERS, AND THE REASON IS A FINDING RATHER THAN AN
# EXCUSE. Room B has sat idle for the three checks above while room A was
# written to repeatedly; its next message takes minutes to arrive, where a
# message into the busy room takes seconds. The sender reports nothing held
# and the phone eventually receives it, so nothing is lost — but a
# conversation that has been quiet for a few minutes is slow to wake, and that
# belongs to the availability wave, not to this gate. Measured here so the
# number is on the record.
b_sent_at=$(date +%s)
say "$B" "$TEXT_TWO"
if wait_for_records 2 90; then
  log "room B woke after $(( $(date +%s) - b_sent_at ))s"
  "${ADB[@]}" shell cmd statusbar expand-notifications >/dev/null; sleep 2
  shot two-and-summary >/dev/null
  "${ADB[@]}" shell cmd statusbar collapse >/dev/null
  s=$(shade summaries)
  [ "$s" -eq 1 ] \
    && record "group.one-summary-above-two" pass "exactly one summary" \
    || record "group.one-summary-above-two" fail "$s summaries above two conversations"
else
  # THE SENDER'S OWN ACCOUNT OF WHY. Its diagnostics name the space it is
  # holding frames for and the reason — "no recipient", "no relay" — which is
  # the difference between a phone that is not notifying and a peer that never
  # handed the message over.
  held=$(peer_api GET /api/relay/diagnostics \
    | python3 -c 'import json,sys
try:
    d = json.load(sys.stdin)
except Exception:
    print("[]"); raise SystemExit
print(json.dumps(d.get("held", [])))' 2>/dev/null)
  # AND THE PHONE'S OWN ACCOUNT. A node that is waiting out a rate limit looks
  # identical to one that is broken — sync active, relay healthy, nothing
  # arriving — so it is asked directly whether it is waiting on purpose.
  phone_wait=$(phone_api GET /api/relay/diagnostics \
    | python3 -c 'import json,sys
try: print(json.load(sys.stdin).get("throttled_for_ms", 0))
except Exception: print(-1)' 2>/dev/null)
  record "group.one-summary-above-two" fail "the second conversation never arrived" \
    "{\"entries_in_b\":$(delivered_count "$B"),\"records\":$(shade records),\"sender_held\":${held:-[]},\"phone_throttled_for_ms\":${phone_wait:-0}}"
fi

# ---- 5. read one: the summary goes, the other stays -----------------------
rig read --es arg "$A" >/dev/null
sleep 3
"${ADB[@]}" shell cmd statusbar expand-notifications >/dev/null; sleep 2
shot one-left-no-summary >/dev/null
"${ADB[@]}" shell cmd statusbar collapse >/dev/null
left=$(shade records); sums=$(shade summaries)
if [ "$left" -eq 1 ] && [ "$sums" -eq 0 ]; then
  record "group.reading-one-leaves-the-other" pass "one conversation, no summary"
else
  record "group.reading-one-leaves-the-other" fail "$left conversation(s), $sums summary(ies)"
fi

# ---- 6. a rename: the new name, and no trace of the old one --------------
peer_api POST /api/identity/name "{\"name\":\"$SENDER_TWO\"}" >/dev/null
# A RENAME IS ITS OWN EVENT, NOT A FIELD ON THE MESSAGE. Renaming republishes
# a manifest into every space, and the name on a notification is whatever the
# core knew when it DECORATED the candidate — so a message sent two seconds
# later is decorated with the old name, correctly, and the check fails for a
# product that is working. Wait until the phone knows, then write.
renamed=no
for _ in $(seq 1 40); do
  sleep 3
  if phone_api GET "/api/spaces/$B/members" | grep -q -- "$SENDER_TWO"; then renamed=yes; break; fi
done
[ "$renamed" = yes ] || log "the phone never learned the new name; the check below will say so"
say "$B" "$TEXT_ONE"
# POLLED, AND FOR LONGER THAN AN ARRIVAL. A rename is not carried by the
# message: it is its own event, and the name on a notification is whatever the
# core knew when it decorated the candidate. So the new name appears when the
# profile event has synced AND a message has arrived after it — two round
# trips, not one. Fifty seconds was not enough and reported a rename that had
# in fact arrived.
for _ in $(seq 1 60); do
  sleep 2
  shade strings | grep -q -- "$SENDER_TWO" && break
done
"${ADB[@]}" shell cmd statusbar expand-notifications >/dev/null; sleep 2
shot renamed-sender >/dev/null
"${ADB[@]}" shell cmd statusbar collapse >/dev/null
if shade strings | grep -q -- "$SENDER_TWO"; then
  record "rename.shows-the-new-name" pass "the new name is on the notification"
else
  record "rename.shows-the-new-name" fail "the rename never reached the shade" \
    "{\"entries_in_b\":$(delivered_count "$B")}"
fi

# ---- 7. PREVIEW → HIDDEN: the surfaces retreat ---------------------------
rig policy --es arg hidden >/dev/null
sleep 4
"${ADB[@]}" shell cmd statusbar expand-notifications >/dev/null; sleep 2
shot tightened-to-hidden >/dev/null
"${ADB[@]}" shell cmd statusbar collapse >/dev/null
leak_scan "tighten.takes-the-surfaces-back" "$SPACE_A" "$SPACE_B" "$SENDER_ONE" "$SENDER_TWO" "$TEXT_ONE" "$TEXT_TWO"

# ---- 8. the lock screen, which is where a notification is most public ----
#
# OPT-IN, BECAUSE THIS ONE CANNOT CLEAN UP AFTER ITSELF. Photographing a lock
# screen means putting the device to sleep, and on a phone with a PIN nothing
# here can unlock it again — the harness has no business typing a passcode and
# will not. So it runs only when asked, and whoever asks knows they will pick
# the phone up afterwards:
#
#   LOCKSCREEN=1 SER=… ./scripts/android/ar1b6-visual-gate.sh
#
# Left out by default, the run ends with the phone awake and usable.
if [ "${LOCKSCREEN:-0}" != "1" ]; then
  record "lockscreen.photographed" skip "set LOCKSCREEN=1 — it leaves the phone locked"
  keep_awake
  cleanup_gate_spaces
  echo
  echo "AR-1b.6b.6 — $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf '%s\n' "${SUMMARY[@]}"
  printf '\n%d passed, %d failed.  Shots: %s  Report: %s\n' "$PASSED" "$FAILED" "$SHOTS" "$REPORT"
  exit $([ "$FAILED" -eq 0 ] && echo 0 || echo 1)
fi

clear_shade "$A" "$B"
rig policy --es arg preview >/dev/null
"${ADB[@]}" shell input keyevent KEYCODE_HOME; sleep 2
say "$A" "$TEXT_TWO"
wait_for_records 1 40
"${ADB[@]}" shell input keyevent KEYCODE_SLEEP; sleep 3
"${ADB[@]}" shell input keyevent KEYCODE_WAKEUP; sleep 3
shot lockscreen-preview >/dev/null
"${ADB[@]}" shell input keyevent KEYCODE_MENU >/dev/null 2>&1
record "lockscreen.photographed" info "a person has to look at this one"

# After the verdicts: a failed check leaves its room behind so it can be
# looked at, and cleanup that ran first would take the evidence with it.
cleanup_gate_spaces

echo
echo "AR-1b.6b.6 — $(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf '%s\n' "${SUMMARY[@]}"
printf '\n%d passed, %d failed.  Shots: %s  Report: %s\n' "$PASSED" "$FAILED" "$SHOTS" "$REPORT"
[ "$FAILED" -eq 0 ]

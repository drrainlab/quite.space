#!/usr/bin/env bash
# AR-0c — the live lifecycle gate, driven headlessly over the HTTP API.
#
# Modelled on scripts/radio/livegate.sh: log() timestamps, FATAL: lines, no
# screen involved. What it proves is not performance — it is FACTS, so a
# throttled phone is a perfectly good phone to run it on.
#
# IT RUNS ONLY AGAINST THE PACKAGE HOST (android/host). A process launched from
# /data/local/tmp lives in the wrong lifecycle model: filesDir is
# package-private, `am force-stop` acts on a package, and App Standby, the App
# Freezer and LMK all key on an app UID. A KILL test run there would prove
# something other than what it claims.
#
# FOUR CLASSES OF CONNECTION DEATH, kept apart because they are four different
# things and a single "it reconnected" would hide which one was tested:
#
#   A  interface switch      Wi-Fi <-> cellular, process alive
#   B  silent stale socket   the interface changed and the old TCP/TLS has NOT
#                            yet reported an error — the case a happy path
#                            skips, because there the OS returned a prompt,
#                            well-formed error and the client was never tested
#   C  process suspension    Doze, in the documented sequence, with proof
#   D  process death         SIGKILL, and force-stop, asserted SEPARATELY
#
# Plus background -> return, which is the one whose claim is "process KEPT":
# it compares core_pid before and after and REFUSES to pass on a restart.
#
# Durations come from the MONOTONIC clock. Wall clock appears only for log
# correlation — across a network change, a Doze and a system resume it makes a
# diagnosis ambiguous, and the tree already has three places that learned this
# the hard way (node/listening.go, node/relaypool.go, node/attention.go).
set -uo pipefail

PKG=${PKG:-quite.space}
SER=${SER:-}
MAC_API=${MAC_API:-http://127.0.0.1:18900}
MAC_TOKEN=${MAC_TOKEN:-ar0ctoken}
PHONE_PORT=${PHONE_PORT:-18901}
SPACE=${SPACE:?set SPACE to the shared space id}
PASS=${PASS:-ar0-phone-passphrase}
ADB=${ADB:-$HOME/Library/Android/sdk/platform-tools/adb}

FAILURES=0
log()   { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*"; }
fatal() { printf '%s  FATAL: %s\n' "$(date +%H:%M:%S)" "$*"; FAILURES=$((FAILURES+1)); }
pass()  { printf '%s  ok    %s\n' "$(date +%H:%M:%S)" "$*"; }
section() { printf '\n────────────────────────────────────────────────────────────\n%s\n' "$*"; }

a() { if [ -n "$SER" ]; then "$ADB" -s "$SER" "$@"; else "$ADB" "$@"; fi; }
mono() { python3 -c 'import time;print(f"{time.monotonic():.3f}")'; }

# ── the rig ──────────────────────────────────────────────────────────────────

SEQ=100
rig() { # rig <cmd> [extra am args…] — returns when the answer carries OUR seq
  local cmd=$1; shift
  SEQ=$((SEQ+1))
  a shell am start -W -n "$PKG/.RigActivity" --es cmd "$cmd" --ei seq "$SEQ" "$@" >/dev/null 2>&1
  local i
  for i in $(seq 1 40); do
    sleep 1
    local got
    got=$(a shell run-as "$PKG" cat files/rig-out.json 2>/dev/null \
          | python3 -c 'import json,sys
try: print(json.load(sys.stdin).get("seq",""))
except Exception: print("")' 2>/dev/null)
    [ "$got" = "$SEQ" ] && return 0
  done
  return 1
}

rig_json() { a shell run-as "$PKG" cat files/rig-out.json 2>/dev/null; }

# field <json> <dotted path> — "" when absent, never a stack trace
field() {
  python3 -c '
import json,sys
d=json.loads(sys.stdin.read() or "{}")
for k in sys.argv[1].split("."):
    d = d.get(k) if isinstance(d, dict) else None
    if d is None: print(""); raise SystemExit
print(d)' "$1" 2>/dev/null
}

phone_forward() { # the API port changes every start; re-forward every time
  local p; p=$(rig_json | field core.api_port)
  [ -z "$p" ] && return 1
  a forward "tcp:$PHONE_PORT" "tcp:$p" >/dev/null 2>&1
  PHONE_TOKEN=$(rig_json | field core.session_token)
  [ -n "$PHONE_TOKEN" ]
}

phone() { curl -s -H "X-QP-Token: $PHONE_TOKEN" "$@"; }
mac()   { curl -s -H "X-QP-Token: $MAC_TOKEN"   "$@"; }

# ── what "the same" means, precisely ─────────────────────────────────────────

# events <api> <token> — the SORTED set of EventIDs. This is the frontier for
# gate purposes: "every event exactly once" and "no fork" are both statements
# about this set, and a count alone would miss a duplicate that replaced a loss.
events() {
  curl -s -H "X-QP-Token: $2" "$1/api/spaces/$SPACE/messages" | python3 -c '
import json,sys
d=json.load(sys.stdin); m = d if isinstance(d,list) else d.get("messages",[])
ids=[x.get("id","") for x in m]
print(len(ids), len(set(ids)), ",".join(sorted(ids)))' 2>/dev/null
}

say() { # say <api> <token> <text>
  curl -s -X POST -H "X-QP-Token: $2" -H 'Content-Type: application/json' \
    -d "{\"text\":$(python3 -c 'import json,sys;print(json.dumps(sys.argv[1]))' "$3")}" \
    "$1/api/spaces/$SPACE/messages" >/dev/null
}

# snapshot prints pid|epoch|fingerprint|mono|boot — TAB-separated, and the
# separator is the whole point.
#
# The first version split on whitespace, and a fingerprint is rendered in
# groups ("58fb ab86 d002 …"). So `read -r PID EPOCH FP MONO BOOT` put "58fb"
# in FP and "ab86" in MONO: every identity assertion in this gate was comparing
# the FIRST WORD of a fingerprint, and the clock arithmetic was parsing hex as
# an integer — which is how it announced itself. A gate that compares a
# quarter of an identity would pass a changed one.
snapshot() {
  local j; j=$(rig_json)
  printf '%s\t%s\t%s\t%s\t%s' \
    "$(printf '%s' "$j" | field core.core_pid)" \
    "$(printf '%s' "$j" | field core.runtime_epoch)" \
    "$(printf '%s' "$j" | field core.fingerprint)" \
    "$(printf '%s' "$j" | field core.mono_ns)" \
    "$(printf '%s' "$j" | field core.boot_ns)"
}
# snap_read <var-prefix> — sets ${prefix}_PID ${prefix}_EPOCH ${prefix}_FP …
snap_read() {
  local p=$1 line
  line=$(snapshot)
  IFS=$'\t' read -r "${p}_PID" "${p}_EPOCH" "${p}_FP" "${p}_MONO" "${p}_BOOT" <<<"$line"
}

converge() { # converge <seconds> — both sides hold the same EventID set
  local deadline=$1 i
  for i in $(seq 1 "$deadline"); do
    sleep 1
    local A B
    A=$(events "$MAC_API" "$MAC_TOKEN"); B=$(events "http://127.0.0.1:$PHONE_PORT" "$PHONE_TOKEN")
    if [ -n "$A" ] && [ "$A" = "$B" ]; then echo "$i"; return 0; fi
  done
  echo "$deadline"; return 1
}

assert_no_dupes() { # <events output> <label>
  local n u
  n=$(echo "$1" | awk '{print $1}'); u=$(echo "$1" | awk '{print $2}')
  if [ "$n" != "$u" ]; then
    fatal "$2: $n events but only $u distinct — an event arrived twice"
  fi
}

# ── the gate ─────────────────────────────────────────────────────────────────

section "AR-0c — live lifecycle gate"
log "package $PKG   space ${SPACE:0:16}…   relay is whatever the node is configured with"
a shell settings put global stay_on_while_plugged_in 15 >/dev/null 2>&1

section "0 — baseline"
rig start --es pass "$PASS" || { fatal "the rig would not answer a start"; exit 1; }
phone_forward || { fatal "no api port from the rig"; exit 1; }
snap_read A; PID0=$A_PID; EPOCH0=$A_EPOCH; FP0=$A_FP; MONO0=$A_MONO; BOOT0=$A_BOOT
log "pid=$PID0 epoch=${EPOCH0:0:8} fingerprint=$FP0"
[ -z "$FP0" ] && { fatal "no fingerprint — the core did not open"; exit 1; }
E0=$(events "$MAC_API" "$MAC_TOKEN"); log "mac holds $(echo "$E0" | awk '{print $1}') events"

section "1 — a message each way, one EventID per event"
say "$MAC_API" "$MAC_TOKEN" "ar0c step 1: from the mac"
say "http://127.0.0.1:$PHONE_PORT" "$PHONE_TOKEN" "ar0c step 1: from the phone"
T=$(converge 60) && pass "converged in ${T}s" || fatal "did not converge in ${T}s"
EV=$(events "$MAC_API" "$MAC_TOKEN"); assert_no_dupes "$EV" "step 1"

section "D1 — SIGKILL, then reopen"
# run-as switches to the app UID, so the app may kill its own process. This is
# process DEATH, and it is asserted separately from force-stop below: they are
# different user-visible events with different resumption rules.
say "$MAC_API" "$MAC_TOKEN" "ar0c D1: sent before the kill"
sleep 3
a shell run-as "$PKG" kill -9 "$PID0" >/dev/null 2>&1
sleep 4
[ "$(a shell ps -A -o NAME 2>/dev/null | grep -c "$PKG")" = "0" ] \
  && pass "the process is gone" || log "note: a process still present after SIGKILL"
rig start --es pass "$PASS" || fatal "the rig would not restart after SIGKILL"
phone_forward || fatal "no api port after SIGKILL"
snap_read B; PID1=$B_PID; FP1=$B_FP
[ "$FP1" = "$FP0" ] && pass "IDENTITY SURVIVED SIGKILL (${FP1:0:19}…)" \
                    || fatal "fingerprint changed across SIGKILL: $FP0 -> $FP1"
[ "$PID1" != "$PID0" ] && pass "new pid $PID1 (the process really died)" \
                       || fatal "same pid after SIGKILL — nothing died"
T=$(converge 90) && pass "caught up in ${T}s" || fatal "did not catch up in ${T}s"
assert_no_dupes "$(events "$MAC_API" "$MAC_TOKEN")" "after SIGKILL"

section "D2 — force-stop, then an EXPLICIT restart"
say "$MAC_API" "$MAC_TOKEN" "ar0c D2: sent before the force-stop"
sleep 3
a shell am force-stop "$PKG" >/dev/null 2>&1
sleep 5
[ "$(a shell ps -A -o NAME 2>/dev/null | grep -c "$PKG")" = "0" ] \
  && pass "force-stop stopped everything" || fatal "something survived force-stop"
sleep 6
[ "$(a shell ps -A -o NAME 2>/dev/null | grep -c "$PKG")" = "0" ] \
  && pass "nothing resumed on its own — a force-stop is not a restart" \
  || fatal "the package restarted itself after force-stop"
rig start --es pass "$PASS" || fatal "the rig would not restart after force-stop"
phone_forward || fatal "no api port after force-stop"
snap_read C; PID2=$C_PID; FP2=$C_FP
[ "$FP2" = "$FP0" ] && pass "identity survived force-stop" || fatal "fingerprint changed across force-stop"
T=$(converge 90) && pass "caught up in ${T}s" || fatal "did not catch up in ${T}s"

section "BACKGROUND -> RETURN — the process must be KEPT"
snap_read D; PIDB=$D_PID; EPOCHB=$D_EPOCH; FPB=$D_FP
EVB=$(events "http://127.0.0.1:$PHONE_PORT" "$PHONE_TOKEN")
log "before HOME: pid=$PIDB epoch=${EPOCHB:0:8} events=$(echo "$EVB" | awk '{print $1}')"
a shell input keyevent KEYCODE_HOME >/dev/null 2>&1
sleep 45
say "$MAC_API" "$MAC_TOKEN" "ar0c background: published while the app was away"
sleep 30
rig status || fatal "the rig would not answer on return"
phone_forward || fatal "no api port on return"
snap_read E; PIDR=$E_PID; EPOCHR=$E_EPOCH; FPR=$E_FP
if [ "$PIDR" = "$PIDB" ] && [ "$EPOCHR" = "$EPOCHB" ]; then
  pass "PROCESS KEPT: same pid $PIDR and same runtime_epoch"
else
  # NOT a pass. A restart that satisfies the scenario would be reported as
  # "the process survived" on evidence that it did not.
  fatal "NOT PASSED as background->return: pid $PIDB->$PIDR epoch ${EPOCHB:0:8}->${EPOCHR:0:8}
         — Android killed a backgrounded process and the host restarted it.
         That is a lifecycle RESULT worth having, and it is not this scenario."
fi
[ "$FPR" = "$FP0" ] && pass "identity unchanged" || fatal "fingerprint changed on return"
T=$(converge 90) && pass "caught up in ${T}s" || fatal "did not catch up in ${T}s"
assert_no_dupes "$(events "$MAC_API" "$MAC_TOKEN")" "background->return"

section "C — Doze, in the documented sequence, with proof of state"
a shell dumpsys deviceidle >/dev/null 2>&1
IDLE_BEFORE=$(a shell dumpsys deviceidle get deep 2>/dev/null | tr -d '\r')
snap_read F; MONOD0=$F_MONO; BOOTD0=$F_BOOT
a shell dumpsys battery unplug >/dev/null 2>&1
a shell dumpsys deviceidle force-idle >/dev/null 2>&1
sleep 3
IDLE_DURING=$(a shell dumpsys deviceidle get deep 2>/dev/null | tr -d '\r')
log "deviceidle: $IDLE_BEFORE -> $IDLE_DURING"
[ "$IDLE_DURING" = "IDLE" ] && pass "the device really entered Doze" \
  || log "note: deviceidle reports $IDLE_DURING — the doze may not have engaged"
say "$MAC_API" "$MAC_TOKEN" "ar0c doze: published while the phone was idle"
sleep 60
a shell dumpsys deviceidle unforce >/dev/null 2>&1
a shell dumpsys battery reset >/dev/null 2>&1
sleep 5
rig status >/dev/null 2>&1
phone_forward >/dev/null 2>&1
snap_read G; MONOD1=$G_MONO; BOOTD1=$G_BOOT
if [ -n "$MONOD0" ] && [ -n "$BOOTD0" ] && [ -n "$MONOD1" ]; then
  python3 - "$MONOD0" "$MONOD1" "$BOOTD0" "$BOOTD1" <<'PY'
import sys
m0,m1,b0,b1 = (int(x) for x in sys.argv[1:5])
dm,db=(m1-m0)/1e9,(b1-b0)/1e9
print(f"          CLOCK_MONOTONIC advanced {dm:7.1f}s   (what time.Since sees)")
print(f"          CLOCK_BOOTTIME  advanced {db:7.1f}s   (includes suspend)")
print(f"          suspended       {db-dm:7.1f}s   ← the number finding 5 is about")
PY
fi
T=$(converge 120) && pass "caught up after Doze in ${T}s" || fatal "did not catch up after Doze in ${T}s"
assert_no_dupes "$(events "$MAC_API" "$MAC_TOKEN")" "after Doze"

section "A + B — Wi-Fi <-> cellular, and the stale socket underneath it"
# A and B share a mechanism and assert DIFFERENT things.
#
#   A asks: after the interface changes, does the node reconnect and end up
#           with every event exactly once?
#   B asks: does it do so WITHOUT the process being restarted?
#
# B is the one a happy path skips. It is also the one that was FAILING when
# this gate was first run: the pool classified `lan: connection closed` as
# non-fatal, never retired the dead socket, and the node pushed forever while
# pulling nothing — recovered only by a restart. So the pid comparison below
# is not ceremony; it is the assertion that found a real defect.
snap_read W; PIDW=$W_PID; EPOCHW=$W_EPOCH

# PRECONDITION, checked rather than assumed: a phone with no working cellular
# DATA cannot exercise Wi-Fi -> cellular at all, and reporting "did not catch
# up" would blame the client for a network that was never there. Probed from
# the SHELL, which is not subject to app restrictions — so a NO here is about
# the SIM, not about Android policy.
CELL_OK=no
a shell svc wifi disable >/dev/null 2>&1
sleep 18
a shell 'timeout 12 nc -z -w 10 91.201.114.71 7411 >/dev/null 2>&1' && CELL_OK=yes
a shell svc wifi enable >/dev/null 2>&1
sleep 10
if [ "$CELL_OK" = "no" ]; then
  log "NOT EXERCISED: Wi-Fi -> cellular. This phone has no working cellular data"
  log "               path (the shell itself cannot reach the relay with Wi-Fi"
  log "               off), so the direction is untested rather than failed."
  log "               It needs a SIM with an active data plan."
fi

for DIR in "wifi-off" "wifi-on"; do
  if [ "$DIR" = "wifi-off" ]; then
    [ "$CELL_OK" = "no" ] && continue
    log "A: Wi-Fi OFF -> cellular"
    a shell svc wifi disable >/dev/null 2>&1
  else
    log "A: cellular -> Wi-Fi ON"
    a shell svc wifi enable >/dev/null 2>&1
  fi
  sleep 20
  TRANSPORT=$(a shell dumpsys connectivity 2>/dev/null | grep -oE "Active default network: [0-9]+" | head -1)
  NET=$(a shell dumpsys connectivity 2>/dev/null | grep -oE "Transports: (WIFI|CELLULAR)" | head -1)
  log "   $TRANSPORT   $NET"
  say "$MAC_API" "$MAC_TOKEN" "ar0c $DIR: published across the switch"
  T=$(converge 150) && pass "A/$DIR: caught up in ${T}s" || fatal "A/$DIR: did not catch up in ${T}s"
  assert_no_dupes "$(events "$MAC_API" "$MAC_TOKEN")" "A/$DIR"
  snap_read X
  if [ "$X_PID" = "$PIDW" ] && [ "$X_EPOCH" = "$EPOCHW" ]; then
    pass "B/$DIR: recovered WITHOUT a restart (pid $X_PID unchanged)"
  else
    fatal "B/$DIR: the node only recovered because it was restarted — pid $PIDW->$X_PID.
         A stale socket that needs a process restart to clear is the class-B failure."
  fi
  [ "$X_FP" = "$FP0" ] && pass "B/$DIR: identity unchanged" || fatal "B/$DIR: fingerprint changed"
done
a shell svc wifi enable >/dev/null 2>&1; sleep 10

section "7 — both ends restart"
say "$MAC_API" "$MAC_TOKEN" "ar0c step 7: before the restarts"
sleep 5
rig stop >/dev/null 2>&1
a shell am force-stop "$PKG" >/dev/null 2>&1
sleep 4
rig start --es pass "$PASS" || fatal "the rig would not come back"
phone_forward || fatal "no api port after the restart"
snap_read Z
[ "$Z_FP" = "$FP0" ] && pass "identity survived the whole gate (${Z_FP})" || fatal "fingerprint changed over the gate"
T=$(converge 120) && pass "converged after the restart in ${T}s" || fatal "did not converge after the restart"
assert_no_dupes "$(events "$MAC_API" "$MAC_TOKEN")" "step 7"

section "SUMMARY"
if [ "$FAILURES" = "0" ]; then
  echo "PASSES the AR-0c classes run above."
else
  echo "DOES NOT PASS: $FAILURES assertion(s) failed."
fi
exit "$FAILURES"

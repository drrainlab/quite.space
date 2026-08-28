#!/usr/bin/env bash
# SP-3.2 — the live sweep gate: does the Field Session survive a real phone.
#
# WHAT THIS PROVES THAT THE SUITE CANNOT. The Go suite proves the engine —
# sagas, gaps, the event, the exports — against a filesystem. The host suite
# proves the classifier's honesty against arithmetic. What neither can prove
# is a phone: Doze on this OEM, the sticky restart on this Android, GPS under
# these trees. This gate is the wave's exit, and it is also the INPUT to the
# wake-lock decision ADR-034 defers — the numbers it produces are the
# evidence that decision is made on.
#
# HALF CHECKLIST, HALF HARNESS, deliberately. A sweep is a walk; no script
# walks. The automatable probes run through adb and the node's own API; the
# steps that need legs or a second device ask their question and record the
# person's answer. A recorded "no" is a finding, not a failure of the script.
#
# THE PHONE'S LOCK IS RESPECTED (the ar1c discipline): nothing here types a
# passcode, and the probes that matter run while the phone is locked —
# that being the whole point of a background session.
#
# USAGE
#   SER=<serial> TOKEN=<api token> ./scripts/android/sp32-sweep-gate.sh
#   Steps can be skipped with s; every answer lands in the report.
#
#   TOKEN is the node's session token; the API is reached through
#   `adb forward` on loopback, the same way the page reaches it.
set -uo pipefail

SER=${SER:-}
PKG=${PKG:-quite.space}
TOKEN=${TOKEN:-}
API_PORT=${API_PORT:-8480}
FWD_PORT=${FWD_PORT:-9945}
OUT=${OUT:-/tmp/sp32-sweep-gate}

ADB=(adb)
[ -n "$SER" ] && ADB=(adb -s "$SER")

mkdir -p "$OUT"
REPORT="$OUT/report.jsonl"
: > "$REPORT"

log()  { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
fail() { printf 'FATAL: %s\n' "$*" >&2; exit 1; }

record() { # name verdict detail
  printf '{"step":"%s","verdict":"%s","detail":"%s","at":"%s"}\n' \
    "$1" "$2" "$3" "$(date -u +%FT%TZ)" >> "$REPORT"
}

ask() { # name question
  local name="$1"; shift
  printf '\n== %s\n   %s\n   [y]es / [n]o / [s]kip: ' "$name" "$*" >&2
  local a; read -r a
  case "$a" in
    y|Y) record "$name" pass "eyes";;
    s|S) record "$name" skip "";;
    *)   record "$name" FAIL "eyes";;
  esac
}

api() { # path [curl args…]
  local path="$1"; shift
  curl -sf -H "X-QP-Token: $TOKEN" "http://127.0.0.1:$FWD_PORT$path" "$@"
}

"${ADB[@]}" get-state >/dev/null 2>&1 || fail "no device (SER=$SER)"
[ -n "$TOKEN" ] || fail "TOKEN is required — the node's session token"
"${ADB[@]}" forward "tcp:$FWD_PORT" "tcp:$API_PORT" >/dev/null || fail "adb forward"
api /api/sweeps >/dev/null || fail "the node's API does not answer through the forward"

actives() { api /api/sweeps | python3 -c 'import sys,json;print(len(json.load(sys.stdin)["sweeps"]))' 2>/dev/null || echo "?"; }

sweep_note_shown() {
  "${ADB[@]}" shell dumpsys notification --noredact 2>/dev/null | grep -q "Sweep in progress"
}

log "report → $REPORT"
log "active sweeps now: $(actives)"

# ---- 1. the session's face --------------------------------------------------
ask start "On the phone: open a sector object, press «Начать свип», grant \
location. Did the persistent notification appear with the sector's name?"

if sweep_note_shown; then
  record notification pass "dumpsys sees the card"
else
  record notification FAIL "dumpsys does not see 'Sweep in progress'"
  log "!! the visible half of the first law is missing"
fi

# ---- 2. the 30-minute screen-off walk --------------------------------------
ask walk "Screen off, phone in a pocket, walk ~30 min. (Check the \
notification's distance afterwards.) Did the counter move while walking?"

# ---- 3. airplane stretch → no_fix is NOT what silence earns -----------------
ask airplane "Mid-walk: airplane mode 2 min (GPS off with it on this OEM \
varies — use the quick tile that kills location if it does not). Later, in \
the exported GPX: is that stretch a SEGMENT BREAK, not a straight line?"

# ---- 4. forced Doze: the suspended gap -------------------------------------
log "forcing Doze (deviceidle) for a measured suspended gap…"
"${ADB[@]}" shell dumpsys deviceidle force-idle >/dev/null 2>&1 \
  && record doze_forced pass "deviceidle force-idle" \
  || record doze_forced skip "this build refuses force-idle"
log "leave it idle ≥2 min, then:"
"${ADB[@]}" shell dumpsys deviceidle unforce >/dev/null 2>&1 || true
ask doze_gap "After waking it: does the track carry a gap for that stretch \
(suspended or unknown — NEVER no_fix)?"

# ---- 5. process kill → sticky resume ---------------------------------------
log "killing the app process (NOT force-stop — Android restarts sticky)…"
PID=$("${ADB[@]}" shell pidof "$PKG" | tr -d '\r')
if [ -n "$PID" ]; then
  "${ADB[@]}" shell "run-as $PKG kill $PID" 2>/dev/null \
    || "${ADB[@]}" shell am crash "$PKG" 2>/dev/null || true
  sleep 20
  if sweep_note_shown; then
    record sticky_resume pass "the card came back on its own"
  else
    record sticky_resume FAIL "no card 20 s after the kill"
  fi
else
  record sticky_resume skip "no pid — is the app running?"
fi
ask resume_gap "In the final track: does the kill show as ONE gap(suspended) \
with a believable duration?"

# ---- 6. the orphan: force-stop → interrupted --------------------------------
ask orphan "Separate short sweep: start it, then Settings → Force stop. \
Reopen Quiet after >2 min. Did the sweep finalize as INTERRUPTED, with the \
walked track preserved, and its task still OPEN?"

# ---- 7. the fact travels ----------------------------------------------------
ask relay "On a second device (relay sync): did sweep.completed arrive — \
the sweep card readable, result and distance right?"
ask lora "On the LoRa bench (if up): did the completion fact cross as one \
frame? (348 B cold is the measured worst — a refusal here is a finding.)"

# ---- 8. the export, by eye --------------------------------------------------
ask gpx "Export the GPX. Two things by eye: <trkseg> breaks at every gap, \
and no editor draws a line through the airplane stretch?"

# ---- 9. after Stop: silence -------------------------------------------------
ask stop_silence "After finishing the sweep: no further position claims from \
this device on the second device's map (the ● goes stale on its TTL)?"

# ---- summary ----------------------------------------------------------------
echo
log "==== sweep gate summary ===="
python3 - "$REPORT" <<'EOF'
import json, sys
rows = [json.loads(l) for l in open(sys.argv[1])]
w = max(len(r["step"]) for r in rows)
bad = 0
for r in rows:
    mark = {"pass": "✓", "skip": "·", "FAIL": "✗"}[r["verdict"]]
    print(f'  {mark} {r["step"]:<{w}}  {r["detail"]}')
    bad += r["verdict"] == "FAIL"
print()
if bad:
    print(f'  {bad} FAILED — the wave does not close; findings above are the work')
    sys.exit(1)
print('  the Field Session survived the phone — SP-3.2 closes, and the doze/walk')
print('  numbers above are the input to the ADR-034 wake-lock decision')
EOF

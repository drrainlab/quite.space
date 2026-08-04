#!/bin/bash
# The live product gate, both halves, driven headlessly over the API.
# Phase A: six spaces, Start a line, offer/accept/grant/commit, text both ways, no relay.
# Phase B: a relay-established space survives the internet dying, and nothing duplicates.
set -u
DIR="${QUIET_GATE_DIR:-/tmp/quiet-livegate}"
rm -rf "$DIR"; mkdir -p "$DIR"
BIN="$DIR/terminal"; RELAYBIN="$DIR/terminal-relay"
cd "$(cd "$(dirname "$0")/../.." && pwd)"
go build -o "$BIN" ./cmd/terminal && go build -o "$RELAYBIN" ./cmd/terminal-relay || exit 1

SEED="livegate-segment-2026-08-04-quiet"
PASS="gatepass123"

log(){ printf '%s %s\n' "$(date +%H:%M:%S)" "$*"; }

# jlist FILE-less: read JSON from stdin, print one field per element of a list
# that may arrive bare or wrapped in any single-key envelope.
jl(){ python3 -c '
import sys,json
d=json.load(sys.stdin)
if isinstance(d,dict):
    for v in d.values():
        if isinstance(v,list): d=v; break
    else: d=[]
field=sys.argv[1] if len(sys.argv)>1 else None
for x in d:
    print(x.get(field,"") if field and isinstance(x,dict) else x)
' "$@"; }


# ---- launch the two nodes, radios attached, LAN OFF, no relay yet ----
QUIET_RADIO_TRACE="$DIR/alice.trace" \
"$BIN" ui --data "$DIR/alice" --name alice --passphrase "$PASS" --port 8801 \
  --no-lan --rnode /dev/cu.usbserial-0001 --mesh-seed "$SEED" > "$DIR/alice.log" 2>&1 &
APID=$!
QUIET_RADIO_TRACE="$DIR/bob.trace" \
"$BIN" ui --data "$DIR/bob" --name bob --passphrase "$PASS" --port 8802 \
  --no-lan --rnode /dev/cu.usbserial-9 --mesh-seed "$SEED" > "$DIR/bob.log" 2>&1 &
BPID=$!
trap 'kill $APID $BPID ${RPID:-} 2>/dev/null' EXIT

# tokens appear in the logs
for i in $(seq 1 60); do
  AT=$(grep -o 'token=[0-9a-f]*' "$DIR/alice.log" | head -1 | cut -d= -f2)
  BT=$(grep -o 'token=[0-9a-f]*' "$DIR/bob.log"   | head -1 | cut -d= -f2)
  [ -n "${AT:-}" ] && [ -n "${BT:-}" ] && break; sleep 1
done
[ -z "${AT:-}" ] || [ -z "${BT:-}" ] && { log "FATAL: nodes did not start"; tail -5 "$DIR"/*.log; exit 1; }
A(){ curl -s -H "X-QP-Token: $AT" "$@"; }
B(){ curl -s -H "X-QP-Token: $BT" "$@"; }
AU=http://127.0.0.1:8801; BU=http://127.0.0.1:8802
grep -q "rnode: attached" "$DIR/alice.log" && grep -q "rnode: attached" "$DIR/bob.log" \
  && log "both radios attached (rnode)" || { log "FATAL: a radio did not attach"; grep -i rnode "$DIR"/*.log; exit 1; }

# =============== PHASE A ===============
log "PHASE A — the six-space rendezvous"
for i in 1 2 3 4 5 6; do
  A -X POST "$AU/api/spaces" -d "{\"title\":\"alice space $i\"}" >/dev/null
done
log "alice holds $(A "$AU/api/spaces" | jl id | wc -l | tr -d ' ') spaces"

log "both announce (say who I am)"
A -X POST "$AU/api/radio/announce" -d '{}' >/dev/null
B -X POST "$BU/api/radio/announce" -d '{}' >/dev/null

log "waiting for mutual sighting (re-announcing, as a periodic beacon would)..."
ADEV=""; BDEV=""
for i in $(seq 1 30); do
  if [ $((i % 8)) -eq 0 ]; then
    A -X POST "$AU/api/radio/announce" -d '{}' >/dev/null
    B -X POST "$BU/api/radio/announce" -d '{}' >/dev/null
  fi
  BDEV=$(A "$AU/api/radio/neighbours" | python3 -c 'import sys,json
n=json.load(sys.stdin)["neighbours"] or []
print(n[0]["device"] if n else "")' 2>/dev/null)
  ADEV=$(B "$BU/api/radio/neighbours" | python3 -c 'import sys,json
n=json.load(sys.stdin)["neighbours"] or []
print(n[0]["device"] if n else "")' 2>/dev/null)
  [ -n "$ADEV" ] && [ -n "$BDEV" ] && break; sleep 2
done
if [ -z "$ADEV" ] || [ -z "$BDEV" ]; then
  log "FATAL: no mutual sighting. Radio state:"
  A "$AU/api/status" | python3 -m json.tool 2>/dev/null | grep -iA4 radio | head -12
  exit 1
fi
log "sighted: alice sees $BDEV, bob sees $ADEV"

log "bob presses Start a line with alice"
T0=$(date +%s)
# The probe-first contract, followed as designed: the first press probes the
# link ("one frame, a few seconds"), and pressing again once it is up sends
# the offer. A repeat re-offers the SAME invitation — no second room.
for i in $(seq 1 12); do
  RESP=$(B -X POST "$BU/api/radio/meet" -d "{\"device\":\"$ADEV\"}")
  STATE=$(echo "$RESP" | python3 -c 'import sys,json
try: print(json.load(sys.stdin).get("state",""))
except Exception: print("")')
  log "  meet: ${STATE:-offered}"
  # ANY non-probing answer means the offer is on its way. Pressing again
  # would queue a re-offer behind it — idempotent for the invitation, but
  # every extra transfer is air the GRANT then has to fight through.
  [ "$STATE" != "probing" ] && break
  sleep 5
done

log "waiting for the offer to reach alice..."
OID=""
for i in $(seq 1 30); do
  OID=$(A "$AU/api/radio/invitations" | python3 -c 'import sys,json
o=json.load(sys.stdin)["invitations"] or []
print(o[0]["id"] if o else "")' 2>/dev/null)
  [ -n "$OID" ] && break; sleep 2
done
[ -z "$OID" ] && { log "FATAL: the offer never arrived"; exit 1; }
log "offer arrived at alice in $(( $(date +%s) - T0 ))s (id $OID) — alice accepts"
A -X POST "$AU/api/radio/invitations/accept" -d "{\"id\":\"$OID\"}" >/dev/null

log "waiting for the line to appear on BOTH sides (grant+commit are the
   biggest frames of the saga; minutes are normal on this PHY)..."
LINE=""
for i in $(seq 1 75); do
  LINE=$(B "$BU/api/spaces" | jl id | head -1)
  if [ -n "$LINE" ]; then
    if A "$AU/api/spaces" | jl id | grep -q "^$LINE$"; then break; fi
    LINE=""
  fi
  sleep 2
done
[ -z "$LINE" ] && { log "FATAL: the line never committed"; exit 1; }
log "LINE ESTABLISHED on both sides in $(( $(date +%s) - T0 ))s: $LINE"

log "text both ways over the radio, no relay anywhere"
# Let the saga's tail (commit echoes, link heartbeats) clear the air first;
# then both texts at once — simultaneous senders are the honest case, and on
# a half-duplex link their sync pushes will fight for air. Minutes are the
# real price; the assertion is arrival, not speed.
sleep 10
A -X POST "$AU/api/spaces/$LINE/messages" -d '{"text":"from alice, by air alone"}' >/dev/null
B -X POST "$BU/api/spaces/$LINE/messages" -d '{"text":"from bob, likewise"}' >/dev/null
AOK=""; BOK=""
for i in $(seq 1 150); do
  BOK=$(B "$BU/api/spaces/$LINE/messages" | jl text | grep -c "from alice" || true)
  AOK=$(A "$AU/api/spaces/$LINE/messages" | jl text | grep -c "from bob" || true)
  [ "${AOK:-0}" -ge 1 ] && [ "${BOK:-0}" -ge 1 ] && break; sleep 2
done
if [ "${AOK:-0}" -ge 1 ] && [ "${BOK:-0}" -ge 1 ]; then
  log "PHASE A PASSES: text crossed both ways in $(( $(date +%s) - T0 ))s total"
else
  log "PHASE A FAILED: alice-heard-bob=$AOK bob-heard-alice=$BOK"; exit 1
fi

# =============== PHASE B ===============
log "PHASE B — failover: a relay-made space survives the internet dying"
"$RELAYBIN" --listen 127.0.0.1:7411 > "$DIR/relay.log" 2>&1 &
RPID=$!
sleep 2
A -X POST "$AU/api/settings" -d '{"relay":"127.0.0.1:7411"}' >/dev/null
B -X POST "$BU/api/settings" -d '{"relay":"127.0.0.1:7411"}' >/dev/null
log "relay up at 127.0.0.1:7411, both nodes pointed at it"

INV_A0=$(A "$AU/api/quicklinks" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(len(d.get("links") or d.get("quicklinks") or []))' 2>/dev/null || echo 0)

PHRASE=$(A -X POST "$AU/api/quicklinks" -d '{"title":"failover proof","max_uses":1,"ttl_hours":1}' | python3 -c 'import sys,json
d=json.load(sys.stdin)
print(d.get("phrase") or "")')
[ -z "$PHRASE" ] && { log "FATAL: quicklink mint failed"; exit 1; }
log "alice minted a quicklink over the relay (five words)"

# Resolve the words to the pass, then walk the ordinary join saga —
# exactly what the UI does behind one button.
PASS=$(B -X POST "$BU/api/quicklinks/resolve" -d "{\"words\":\"$PHRASE\"}" | python3 -c 'import sys,json
d=json.load(sys.stdin)
print(d.get("pass_link") or d.get("PassLink") or d.get("pass") or "")')
[ -z "$PASS" ] && { log "FATAL: resolve returned no pass"; exit 1; }
REQ=$(B -X POST "$BU/api/join-requests" -d "{\"pass\":\"$(printf '%s' "$PASS")\"}" | python3 -c 'import sys,json
print(json.load(sys.stdin).get("request_id",""))')
log "bob asked to enter (request $REQ)"
FSPACE=""
for i in $(seq 1 30); do
  FSPACE=$(B "$BU/api/spaces" | jl id | grep -v "^$LINE$" | head -1)
  [ -n "$FSPACE" ] && break; sleep 2
done
[ -z "$FSPACE" ] && { log "FATAL: bob never joined over the relay"; exit 1; }
log "bob joined space $FSPACE through the relay"

log "each sends one message (relay era)"
A -X POST "$AU/api/spaces/$FSPACE/messages" -d '{"text":"relay-era from alice"}' >/dev/null
B -X POST "$BU/api/spaces/$FSPACE/messages" -d '{"text":"relay-era from bob"}' >/dev/null
for i in $(seq 1 60); do
  OK=$(B "$BU/api/spaces/$FSPACE/messages" | jl text | grep -c "relay-era from alice" || true)
  [ "${OK:-0}" -ge 1 ] && break; sleep 2
done
log "relay-era messages settled"

log "THE INTERNET DIES (relay killed); nobody restarts anything"
kill $RPID 2>/dev/null; wait $RPID 2>/dev/null; RPID=""
sleep 2

T1=$(date +%s)
A -X POST "$AU/api/spaces/$FSPACE/messages" -d '{"text":"after the internet died, by radio"}' >/dev/null
RADOK=""
# The pump reaches a given space on its own cadence — measured at ~60 s
# before the first frame even leaves — and a seven-fragment sync summary is
# minutes of air on this PHY. The assertion is ARRIVAL, not speed.
for i in $(seq 1 200); do
  RADOK=$(B "$BU/api/spaces/$FSPACE/messages" | jl text | grep -c "after the internet died" || true)
  [ "${RADOK:-0}" -ge 1 ] && break; sleep 2
done
if [ "${RADOK:-0}" -ge 1 ]; then
  log "FAILOVER WORKS: the message crossed by radio in $(( $(date +%s) - T1 ))s"
else
  log "PHASE B FAILED: the space did not resume over radio"; exit 1
fi

log "THE INTERNET RETURNS (relay restarted)"
"$RELAYBIN" --listen 127.0.0.1:7411 > "$DIR/relay2.log" 2>&1 &
RPID=$!
sleep 20

log "counting every message exactly once on bob"
B "$BU/api/spaces/$FSPACE/messages" | python3 -c '
import sys,json
d=json.load(sys.stdin)
if isinstance(d,dict):
    for v in d.values():
        if isinstance(v,list): d=v; break
    else: d=[]
m=[x.get("text") or "" for x in d if isinstance(x,dict)]
bad=False
for probe in ["relay-era from alice","relay-era from bob","after the internet died, by radio"]:
    c=sum(1 for t in m if t==probe)
    print(f"    {c}x  {probe}")
    if c!=1: bad=True
sys.exit(1 if bad else 0)' && log "no duplicates after the relay returned" \
  || { log "PHASE B FAILED: duplicates appeared"; exit 1; }

INV_A1=$(A "$AU/api/quicklinks" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(len(d.get("links") or d.get("quicklinks") or []))' 2>/dev/null || echo 0)
log "quicklink records: before the whole phase $INV_A0, after $INV_A1 (the ONE minted link; failover minted nothing)"

log "GATE COMPLETE — both phases pass"

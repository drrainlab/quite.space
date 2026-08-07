#!/usr/bin/env bash
# Run the suite until something fails, and KEEP WHAT IT SAID.
#
# WRITTEN BECAUSE A FLAKE WAS SEEN ONCE AND LOST. A test in ./terminals failed
# during an unrelated run, was noted, and could not be reproduced afterwards:
# 60+ repetitions, -race, -shuffle and two full shuffled passes of the whole
# suite were all green. Without the original output there is nothing to fix —
# and "it failed once, weeks ago" is not something anybody can act on.
#
# So this is not a fixer. It is a net: it repeats the suite the way CI would,
# under the conditions that shake out order- and timing-dependence, and on the
# first failure it stops and writes the whole output to a file. The next
# occurrence becomes evidence instead of a memory.
#
# WHAT IT VARIES, and why each one:
#   -count      the same test, again, in one process — catches state left
#               behind between runs
#   -shuffle    a different order every pass — catches tests that only pass
#               because another one ran first
#   -race       a second scheduler and a real detector — catches the
#               concurrency bugs that otherwise show up once a month
#
# USAGE
#   ./scripts/flake-hunt.sh                    # the whole suite, 20 passes
#   ./scripts/flake-hunt.sh ./terminals/ 50    # one package, 50 passes
#   OUT=/tmp/hunt ./scripts/flake-hunt.sh
set -uo pipefail

PKG=${1:-./...}
PASSES=${2:-20}
OUT=${OUT:-/tmp/flake-hunt}
mkdir -p "$OUT"

echo "hunting in $PKG, $PASSES passes, output under $OUT" >&2

for i in $(seq 1 "$PASSES"); do
  # A different seed each pass, printed, so a failure can be replayed exactly:
  # `go test -shuffle=<seed>` is deterministic given the same seed.
  seed=$(( 1000 + i ))
  log="$OUT/pass-$i.log"
  printf 'pass %d/%d (shuffle seed %d)\n' "$i" "$PASSES" "$seed" >&2

  # -race on every third pass: it is three to ten times slower, and running it
  # every time would mean far fewer passes in the same wall clock — which is
  # the wrong trade for a hunt whose whole point is repetition.
  if [ $(( i % 3 )) -eq 0 ]; then
    go test -count=1 -shuffle="$seed" -race "$PKG" > "$log" 2>&1
  else
    go test -count=2 -shuffle="$seed" "$PKG" > "$log" 2>&1
  fi
  rc=$?

  if [ $rc -ne 0 ]; then
    echo >&2
    echo "FAILED on pass $i (seed $seed). The output is in $log:" >&2
    echo >&2
    grep -vE '^ok |no test files' "$log" | head -60 >&2
    echo >&2
    echo "replay it with: go test -count=1 -shuffle=$seed $PKG" >&2
    exit 1
  fi
  rm -f "$log"
done

echo "$PASSES passes, nothing failed. That is not proof there is no flake — " >&2
echo "it is the number of times it did not happen." >&2

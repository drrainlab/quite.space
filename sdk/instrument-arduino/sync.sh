#!/bin/sh
# One source of truth: the C core lives in sdk/instrument-c. This copies
# it into src/qi/ so the library is self-contained for Arduino/PlatformIO.
set -e
cd "$(dirname "$0")"
rm -rf src/qi && mkdir -p src/qi/qi
cp ../instrument-c/include/qi/*.h src/qi/qi/
cp ../instrument-c/src/*.c src/qi/
echo "synced qi-core into src/qi"

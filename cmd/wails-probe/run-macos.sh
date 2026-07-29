#!/bin/sh
# Launch the bundled probe with its stdout captured, so the PROBE verdicts
# land in dist/probe.log even when started from Finder muscle memory.
set -e
cd "$(dirname "$0")"
exec "dist/Quiet Spaces Probe.app/Contents/MacOS/quiet-spaces-probe" 2>&1 | tee dist/probe.log

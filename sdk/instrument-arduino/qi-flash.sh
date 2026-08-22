#!/bin/sh
# One command from sketch to running board:
#   ./qi-flash.sh ClimateDHT            build + upload (default env), then monitor
#   ./qi-flash.sh ClimateDHT lilygo-t3-s3
#   ./qi-flash.sh Greenhouse heltec_wifi_lora_32_V3 --no-monitor
# The port is autodetected by PlatformIO; pass PORT=/dev/cu.xxx to force.
set -e
EXAMPLE="${1:?usage: qi-flash.sh <Example> [env] [--no-monitor]}"
ENV="$2"
cd "$(dirname "$0")/examples/$EXAMPLE"
./../../sync.sh >/dev/null
ARGS="-t upload"
[ -n "$ENV" ] && [ "$ENV" != "--no-monitor" ] && ARGS="$ARGS -e $ENV"
[ -n "$PORT" ] && ARGS="$ARGS --upload-port $PORT"
pio run $ARGS
echo
echo "flashed. next: run the stand from the repo root —"
echo "  go run ./cmd/instrument-serial --port \${PORT:-/dev/cu.usbmodemXXXX} --api http://127.0.0.1:PORT --token TOKEN --space SPACEHEX"
case "$*" in *--no-monitor*) exit 0;; esac
pio device monitor ${PORT:+-p $PORT}

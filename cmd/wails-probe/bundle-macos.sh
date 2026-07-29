#!/bin/sh
# Build the probe as a real macOS .app bundle into dist/.
#
# The bundle is not cosmetics: without an Info.plist carrying
# NSMicrophoneUsageDescription, macOS refuses the microphone permission
# prompt entirely and the mic-record gate cannot even be attempted. A bare
# binary inherits the terminal's identity; a bundle is what the OS treats as
# an application — which is exactly what DS-0 is probing.
#
# Unsigned on purpose: a locally built app carries no quarantine attribute,
# so Gatekeeper does not object. Signing and notarisation are DS-4.
set -e
cd "$(dirname "$0")"

APP="dist/Quiet Spaces Probe.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS"

# Match the deployment target to the linked frameworks so the build is quiet;
# 13.3 is what the Wails alpha's objects are built for.
MACOSX_DEPLOYMENT_TARGET=13.3 CGO_CFLAGS="-mmacosx-version-min=13.3" \
CGO_LDFLAGS="-mmacosx-version-min=13.3" \
	go build -o "$APP/Contents/MacOS/quiet-spaces-probe" .

cat > "$APP/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key>            <string>Quiet Spaces Probe</string>
  <key>CFBundleDisplayName</key>     <string>Quiet Spaces Probe</string>
  <key>CFBundleIdentifier</key>      <string>space.quiet.probe</string>
  <key>CFBundleExecutable</key>      <string>quiet-spaces-probe</string>
  <key>CFBundlePackageType</key>     <string>APPL</string>
  <key>CFBundleInfoDictionaryVersion</key> <string>6.0</string>
  <key>CFBundleShortVersionString</key> <string>0.0.1</string>
  <key>LSMinimumSystemVersion</key>  <string>13.3</string>
  <key>NSHighResolutionCapable</key> <true/>
  <key>NSMicrophoneUsageDescription</key>
  <string>The probe records a short clip to verify voice messages work inside the shell.</string>
</dict>
</plist>
PLIST

echo "built: $APP"
echo "run:   open \"$APP\"   (stdout with PROBE verdicts: dist/probe.log via run-macos.sh)"

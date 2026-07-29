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

# The dock icon: macOS reads it from the bundle's .icns, not from runtime
# code. Built from the color glyph with the OS's own tools — sips for the
# size ladder, iconutil for the container. The 512px repo asset upscales to
# fill the 1024 slot; a proper master pipeline is DS-4's business.
ICONSET="dist/probe.iconset"
rm -rf "$ICONSET" && mkdir -p "$ICONSET"
for sz in 16 32 128 256 512; do
	sips -z $sz $sz assets/app-icon.png --out "$ICONSET/icon_${sz}x${sz}.png" >/dev/null
	dbl=$((sz * 2))
	sips -z $dbl $dbl assets/app-icon.png --out "$ICONSET/icon_${sz}x${sz}@2x.png" >/dev/null
done
mkdir -p "$APP/Contents/Resources"
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/probe.icns"
rm -rf "$ICONSET"

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
  <key>CFBundleIconFile</key>          <string>probe</string>
  <key>LSMinimumSystemVersion</key>  <string>13.3</string>
  <key>NSHighResolutionCapable</key> <true/>
  <key>NSMicrophoneUsageDescription</key>
  <string>The probe records a short clip to verify voice messages work inside the shell.</string>
</dict>
</plist>
PLIST

echo "built: $APP"
echo "run:   open \"$APP\"   (stdout with PROBE verdicts: dist/probe.log via run-macos.sh)"

#!/bin/sh
# Build Quiet Spaces as a macOS .app and wrap it in a .dmg.
#
# THE SIGNATURE IS AD-HOC, AND THAT IS A DECISION RATHER THAN AN OMISSION.
# There is no Apple Developer Program membership behind this beta, so there is
# no Developer ID to sign with and no notarisation to staple. What there IS is
# `codesign -s -`, which costs nothing and is not optional: on Apple Silicon
# the kernel refuses to execute an arm64 binary carrying no signature at all,
# so an "unsigned" build would not start rather than merely warn.
#
# What an ad-hoc signature does NOT do is satisfy Gatekeeper. A .dmg that
# arrived through a browser carries com.apple.quarantine, and macOS will say it
# cannot verify the developer. That is true — nobody has verified us — and the
# honest remedy is one the person performs deliberately, printed at the end of
# this script and repeated on the download page. Hiding it behind a friendly
# sentence would be teaching people to wave through exactly the dialog that
# protects them.
#
#   ARCH=universal ./bundle-macos.sh     both slices in one .app (default)
#   ARCH=arm64     ./bundle-macos.sh     this machine only, and faster
#   VERSION=0.1.0  ./bundle-macos.sh     what the About box says
set -e
cd "$(dirname "$0")"

ARCH=${ARCH:-universal}
VERSION=${VERSION:-0.1.0-beta}
NAME="Quiet Spaces"
BIN="quiet-spaces"
# FROZEN FROM THE FIRST RELEASE. macOS keys preferences, keychain items and
# the TCC permission grants (microphone, most importantly) on this string —
# changing it later silently orphans all of them, exactly the way changing an
# Android signing key orphans an install.
BUNDLE_ID="space.quite.desktop"

APP="dist/$NAME.app"
rm -rf "$APP" dist/dmg
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

# 13.3 is what this Wails alpha's objects are built for; matching it keeps the
# link quiet instead of printing a screen of version warnings that hide a real
# one when it appears.
build_slice() {
	arch=$1
	out=$2
	echo "  building darwin/$arch…"
	CGO_ENABLED=1 GOOS=darwin GOARCH=$arch \
	MACOSX_DEPLOYMENT_TARGET=13.3 \
	CGO_CFLAGS="-mmacosx-version-min=13.3" \
	CGO_LDFLAGS="-mmacosx-version-min=13.3" \
		go build -trimpath -ldflags "-s -w" -o "$out" . 2>&1 | grep -v '^ld: warning' || true
	[ -f "$out" ]
}

case "$ARCH" in
universal)
	if build_slice arm64 "dist/$BIN.arm64" && build_slice amd64 "dist/$BIN.amd64"; then
		lipo -create "dist/$BIN.arm64" "dist/$BIN.amd64" -output "$APP/Contents/MacOS/$BIN"
		SLICES="arm64 + amd64"
	else
		# Said out loud rather than silently producing a narrower build than
		# the file name promises: an Intel Mac that downloads an arm64-only
		# "universal" dmg fails in a way nobody can diagnose from the outside.
		echo "  amd64 slice failed — falling back to this machine's architecture only"
		HOST=$(uname -m); [ "$HOST" = "x86_64" ] && HOST=amd64
		build_slice "$HOST" "$APP/Contents/MacOS/$BIN"
		SLICES="$HOST only"
		ARCH="$HOST"
	fi
	rm -f "dist/$BIN.arm64" "dist/$BIN.amd64"
	;;
*)
	build_slice "$ARCH" "$APP/Contents/MacOS/$BIN"
	SLICES="$ARCH"
	;;
esac

# The dock icon comes from the bundle's .icns and from nowhere else — no
# amount of runtime code sets it. Built with the OS's own tools from the
# colour glyph the binary already embeds for its about box.
ICONSET="dist/icon.iconset"
rm -rf "$ICONSET" && mkdir -p "$ICONSET"
for sz in 16 32 128 256 512; do
	sips -z $sz $sz assets/app-icon.png --out "$ICONSET/icon_${sz}x${sz}.png" >/dev/null
	dbl=$((sz * 2))
	sips -z $dbl $dbl assets/app-icon.png --out "$ICONSET/icon_${sz}x${sz}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/quiet-spaces.icns"
rm -rf "$ICONSET"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key>            <string>$NAME</string>
  <key>CFBundleDisplayName</key>     <string>$NAME</string>
  <key>CFBundleIdentifier</key>      <string>$BUNDLE_ID</string>
  <key>CFBundleExecutable</key>      <string>$BIN</string>
  <key>CFBundlePackageType</key>     <string>APPL</string>
  <key>CFBundleInfoDictionaryVersion</key> <string>6.0</string>
  <key>CFBundleShortVersionString</key>    <string>$VERSION</string>
  <key>CFBundleVersion</key>               <string>$VERSION</string>
  <key>CFBundleIconFile</key>        <string>quiet-spaces</string>
  <key>LSMinimumSystemVersion</key>  <string>13.3</string>
  <key>NSHighResolutionCapable</key> <true/>
  <key>NSMicrophoneUsageDescription</key>
  <string>Quiet Spaces records voice messages only while you hold the record button, and they never leave the people you send them to.</string>
</dict>
</plist>
PLIST

# Ad-hoc, and over the whole bundle so the embedded frameworks are covered.
# --deep is deprecated; signing the bundle after its contents are in place is
# the supported shape and is what this is.
codesign --force --sign - --timestamp=none "$APP"
codesign --verify --verbose=1 "$APP" 2>&1 | sed 's/^/  /'

# The .dmg: the app beside a symlink to /Applications, which is the drag
# target every Mac user already knows. UDZO because it compresses and every
# macOS since forever mounts it without a second thought.
STAGE="dist/dmg"
mkdir -p "$STAGE"
cp -R "$APP" "$STAGE/"
ln -s /Applications "$STAGE/Applications"
case "$ARCH" in
universal) DMG="dist/quite-space-macos-universal.dmg" ;;
*)         DMG="dist/quite-space-macos-$ARCH.dmg" ;;
esac
rm -f "$DMG"
hdiutil create -quiet -volname "$NAME" -srcfolder "$STAGE" -ov -format UDZO "$DMG"
rm -rf "$STAGE"

echo
echo "built: $DMG   ($SLICES, version $VERSION)"
echo "       $(du -h "$DMG" | cut -f1)"
echo
echo "This build is EXPERIMENTAL and signed ad-hoc — there is no Apple"
echo "Developer certificate behind it. Opening it after a download takes one"
echo "deliberate step, and the download page must say so:"
echo
echo "  1. open the .dmg and drag Quiet Spaces to Applications"
echo "  2. launch it once — macOS will refuse and say it cannot verify it"
echo "  3. System Settings -> Privacy & Security -> Open Anyway"
echo
echo "Or, for somebody who prefers a command to a dialog:"
echo "  xattr -dr com.apple.quarantine \"/Applications/$NAME.app\""

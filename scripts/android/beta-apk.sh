#!/usr/bin/env bash
# Build the beta APK, and refuse to hand over one that is wrong.
#
# THREE THINGS ARE CHECKED, and each is something that has shipped by accident
# in somebody's project before:
#
#   THE RIG IS NOT IN IT. RigActivity and the contender process are a control
#   surface that starts nodes on command and a second process that fights for
#   the data-dir lock. They live in src/debug and a release variant does not
#   compile them — but "does not compile them" is a claim, and this looks.
#
#   IT IS DEBUGGABLE NOWHERE. A debuggable release is `run-as` for anybody
#   with a cable: the whole data directory, including the keystore, readable
#   from a shell.
#
#   IT IS SIGNED, OR IT SAYS SO LOUDLY. An unsigned apk cannot be installed;
#   worse is one signed by a debug key, because it installs, works, and can
#   never be updated by the real build — every tester would have to uninstall,
#   losing their node with it.
#
# The key is not in this repository and this script will not create one. See
# app/build.gradle.kts for where Gradle looks; make the key with keytool.
set -uo pipefail

cd "$(dirname "$0")/../.."
HOST=android/host
OUT=$HOST/app/build/outputs/apk/release
SDK=${ANDROID_HOME:-$HOME/Library/Android/sdk}

say()  { printf '%s\n' "$*" >&2; }
die()  { printf 'REFUSED: %s\n' "$*" >&2; exit 1; }

# The web UI and the core are EMBEDDED in the gomobile binding, so an apk
# built against a stale .aar ships yesterday's interface with today's Kotlin.
# That has happened twice in one day; it is cheap to make impossible.
if [ "${SKIP_AAR:-0}" != "1" ]; then
  say "rebuilding the core binding (SKIP_AAR=1 to skip)…"
  ( cd android/quietcore && \
    PATH="$HOME/go/bin:$PATH" ANDROID_HOME="$SDK" \
    ANDROID_NDK_HOME="$(ls -d "$SDK"/ndk/* 2>/dev/null | tail -1)" \
    gomobile bind -target=android/arm64 -androidapi 24 -javapkg space.quiet \
      -o ../host/app/libs/quietcore.aar . ) || die "the binding would not build"
fi

say "building the release apk…"
ANDROID_HOME="$SDK" gradle -p "$HOST" :app:assembleRelease -q || die "the build failed"

APK=$(ls "$OUT"/*.apk 2>/dev/null | head -1)
[ -n "$APK" ] || die "no apk was produced"
say "built: $APK ($(( $(stat -f%z "$APK" 2>/dev/null || stat -c%s "$APK") / 1024 / 1024 )) MB)"

# ---- the rig ---------------------------------------------------------------
rig=0
for dex in $(unzip -Z1 "$APK" 'classes*.dex' 2>/dev/null); do
  n=$(unzip -p "$APK" "$dex" | strings | grep -c "RigActivity\|ContenderActivity" || true)
  rig=$(( rig + n ))
done
[ "$rig" -eq 0 ] || die "the measurement rig is in the beta apk ($rig references)"
say "ok: no rig, no contender"

# ---- debuggable ------------------------------------------------------------
AAPT=$(ls "$SDK"/build-tools/*/aapt2 2>/dev/null | tail -1)
if [ -n "$AAPT" ]; then
  if "$AAPT" dump xmltree --file AndroidManifest.xml "$APK" 2>/dev/null \
     | grep -q 'debuggable.*0xffffffff)=(type 0x12)0xffffffff'; then
    die "the beta apk is debuggable — run-as would hand anybody the keystore"
  fi
  say "ok: not debuggable"
else
  say "note: aapt2 not found, could not check the debuggable flag"
fi

# ---- signature -------------------------------------------------------------
SIGNER=$(ls "$SDK"/build-tools/*/apksigner 2>/dev/null | tail -1)
case "$APK" in
  *unsigned*)
    say ""
    say "UNSIGNED. This apk cannot be installed."
    say "Give Gradle a key — see android/host/app/build.gradle.kts — and run again:"
    say "  keytool -genkeypair -v -keystore quiet-release.jks -alias quiet \\"
    say "    -keyalg RSA -keysize 4096 -validity 10000"
    say "  cp android/host/keystore.properties.example android/host/keystore.properties"
    exit 2
    ;;
esac

if [ -n "$SIGNER" ]; then
  "$SIGNER" verify --print-certs "$APK" >/dev/null 2>&1 || die "the signature does not verify"
  digest=$("$SIGNER" verify --print-certs "$APK" 2>/dev/null | grep -i "SHA-256 digest" | head -1)
  say "ok: signed"
  say "     $digest"
  say ""
  say "KEEP THAT DIGEST. An apk signed by a different key is a different app to"
  say "Android: it cannot update this one, and the node inside the old install"
  say "becomes unreachable."
else
  say "note: apksigner not found, could not verify the signature"
fi

say ""
say "install with:  adb install -r $APK"

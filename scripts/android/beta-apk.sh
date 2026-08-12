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
  # The output directory holds nothing but gomobile's own products, so
  # android/.gitignore excludes all of them — and git does not track an empty
  # directory, which means a FRESH CLONE has no libs/ at all. gomobile does not
  # create its output's parent, so it fails with a bare
  #   open ../host/app/libs/quietcore.aar: no such file or directory
  # A machine that has ever built this once never sees it; a clean runner sees
  # nothing else.
  mkdir -p android/host/app/libs
  ( cd android/quietcore && \
    PATH="$HOME/go/bin:$PATH" ANDROID_HOME="$SDK" \
    ANDROID_NDK_HOME="$(ls -d "$SDK"/ndk/* 2>/dev/null | tail -1)" \
    gomobile bind -target=android/arm64 -androidapi 24 -javapkg space.quiet \
      -o ../host/app/libs/quietcore.aar . ) || die "the binding would not build"
fi

# THE BUILD MUST NOT DEPEND ON WHOSE SHELL RAN IT.
#
# Gradle 9 needs a JVM 17 or newer, and it takes JAVA_HOME when that is set —
# so a profile pointing at an older JDK turns this script into an error
# message, on the same machine where it had just worked for somebody whose
# JAVA_HOME happened to be empty. That difference is not the operator's to
# debug at the moment they are trying to cut a release.
#
# So a suitable JDK is FOUND rather than assumed, and if there is none the
# script says which ones it looked for.
pick_jdk() {
  # An explicit choice, if it is new enough, wins: somebody who set this meant it.
  if [ -n "${JAVA_HOME:-}" ] && [ -x "$JAVA_HOME/bin/javac" ]; then
    v=$("$JAVA_HOME/bin/javac" -version 2>&1 | sed -E 's/javac ([0-9]+).*/\1/')
    case "$v" in ''|*[!0-9]*) ;; *) [ "$v" -ge 17 ] && return 0 ;; esac
  fi
  for cand in \
    "$(/usr/libexec/java_home -v 17+ 2>/dev/null)" \
    /opt/homebrew/opt/openjdk/libexec/openjdk.jdk/Contents/Home \
    /usr/local/opt/openjdk/libexec/openjdk.jdk/Contents/Home \
    "/Applications/Android Studio.app/Contents/jbr/Contents/Home" \
    "$HOME/.sdkman/candidates/java/current"
  do
    [ -n "$cand" ] && [ -x "$cand/bin/javac" ] || continue
    v=$("$cand/bin/javac" -version 2>&1 | sed -E 's/javac ([0-9]+).*/\1/')
    case "$v" in ''|*[!0-9]*) continue ;; esac
    [ "$v" -ge 17 ] || continue
    export JAVA_HOME="$cand"
    say "using JDK $v at $JAVA_HOME"
    return 0
  done
  return 1
}
if ! pick_jdk; then
  die "no JDK 17 or newer found. Gradle needs one. Looked at JAVA_HOME, \
/usr/libexec/java_home -v 17+, Homebrew's openjdk, and Android Studio's \
bundled runtime. Install one (brew install openjdk) or point JAVA_HOME at it."
fi

say "building the release apk…"
# THE WRAPPER, NOT WHATEVER gradle IS ON PATH.
#
# This used to invoke a bare `gradle`, which meant the build depended on
# whichever version the machine happened to have. That worked for as long as
# only one machine ever ran it. On a clean runner it met Gradle 9.6.1 and AGP
# 8.13 died on the spot — "relies on org.gradle.api.problems.internal.Internal-
# Problems, a Gradle internal API that was removed in Gradle 9.6.0" — a failure
# no change of ours had caused and no local run could reproduce.
#
# gradle/wrapper/gradle-wrapper.properties pins 9.2.1: the version this beta
# was actually built and tested with. Whoever clones this gets that version,
# downloaded once, whatever they have installed.
#
# GRADLE_ARGS is how the release workflow passes the version from the tag
# (-PquietVersionName / -PquietVersionCode). Deliberately unquoted so several
# properties arrive as several arguments; a local build passes none.
# shellcheck disable=SC2086
ANDROID_HOME="$SDK" "$HOST/gradlew" -p "$HOST" :app:assembleRelease -q ${GRADLE_ARGS:-} \
	|| die "the build failed"

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

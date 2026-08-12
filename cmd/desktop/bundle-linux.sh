#!/bin/sh
# Build Quiet Spaces as a Debian package into dist/.
#
# On Linux it builds where it stands. Anywhere else it rents a Linux userland
# from Docker, because Wails needs CGO against GTK and WebKitGTK and there is
# no honest way to cross-compile that from a Mac.
#
# GTK3, DELIBERATELY. This Wails alpha's DEFAULT Linux path is GTK4 +
# webkitgtk-6.0 and it needs GTK >= 4.10 (GtkFileDialog), which is newer than
# Debian 12 ships — so a default build would refuse to install on the
# distribution most likely to be running on somebody's spare machine. The
# complete GTK3 + webkit2gtk-4.1 variant lives behind `-tags gtk3` and reaches
# Debian 12+ and Ubuntu 22.04+. CI keeps building the GTK4 path in
# cmd/wails-probe, so the day that stops being the right trade we will know.
#
# EXPERIMENTAL and unsigned: no repository, no key, alpha testers only.
# Install with apt rather than dpkg — apt resolves the GTK and WebKit runtime
# dependencies and dpkg -i alone will leave them unmet:
#
#     sudo apt install ./quite-space-linux-amd64.deb
#
#   ARCH=amd64|arm64 ./bundle-linux.sh
#   VERSION=0.1.0    ./bundle-linux.sh
set -e
cd "$(dirname "$0")"

ARCH="${ARCH:-amd64}"
VERSION="${VERSION:-0.1.0-beta}"
PKG="quite-space"
mkdir -p dist

# The build itself, run either here or inside the container. Written once, as
# a string, so the two paths cannot drift into being two different packages.
BUILD='
	set -e
	ROOT="$(mktemp -d)"
	mkdir -p "$ROOT/DEBIAN" "$ROOT/usr/bin" \
		"$ROOT/usr/share/applications" \
		"$ROOT/usr/share/icons/hicolor/512x512/apps"

	go build -tags gtk3 -trimpath -ldflags "-s -w" -o "$ROOT/usr/bin/$PKG" .

	cp assets/app-icon.png \
		"$ROOT/usr/share/icons/hicolor/512x512/apps/$PKG.png"

	cat > "$ROOT/usr/share/applications/space.quite.desktop.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=Quiet Spaces
Comment=A quiet place for the people you choose
Exec=$PKG
Icon=$PKG
Terminal=false
Categories=Network;Chat;
DESKTOP

	cat > "$ROOT/DEBIAN/control" <<CONTROL
Package: $PKG
Version: $VERSION
Architecture: $ARCH
Maintainer: Quiet Spaces <beta@quite.space>
Depends: libgtk-3-0, libwebkit2gtk-4.1-0
Section: net
Priority: optional
Description: Quiet Spaces — a local-first, end-to-end encrypted place
 Quiet Spaces keeps your conversations on your own devices and carries them
 over whatever path is available: the internet, a local network, or a LoRa
 radio. This is an EXPERIMENTAL beta build. It is unsigned and served from
 no repository.
CONTROL

	# Version-free file name: the download link on quite.space points at
	# releases/latest/download/<name>, and a version in the name would break
	# that URL on every release.
	dpkg-deb --build --root-owner-group "$ROOT" "dist/$PKG-linux-$ARCH.deb"
	dpkg-deb -I "dist/$PKG-linux-$ARCH.deb" | sed "s/^/  /"
	rm -rf "$ROOT"
'

if [ "$(uname -s)" = "Linux" ]; then
	echo "building natively on $(uname -m)…"
	command -v dpkg-deb >/dev/null || {
		echo "dpkg-deb is missing — apt-get install dpkg-dev" >&2
		exit 1
	}
	PKG="$PKG" VERSION="$VERSION" ARCH="$ARCH" sh -c "$BUILD"
else
	echo "no Linux here — renting one from Docker (linux/$ARCH)…"
	command -v docker >/dev/null || {
		echo "Docker is not installed, and a Linux build needs a Linux userland." >&2
		echo "Either install Docker or run this script on the target distribution." >&2
		exit 1
	}
	# Named volumes so only the first run pays for the module and build cache.
	docker run --rm --platform "linux/$ARCH" \
		-v "$(cd ../.. && pwd)":/src \
		-v qp-deb-gomod:/go/pkg/mod \
		-v "qp-deb-gocache-$ARCH":/root/.cache/go-build \
		-w /src/cmd/desktop \
		-e PKG="$PKG" -e VERSION="$VERSION" -e ARCH="$ARCH" \
		golang:1.25-bookworm \
		sh -ec '
			export DEBIAN_FRONTEND=noninteractive
			apt-get update -qq
			apt-get install -qq -y --no-install-recommends \
				libgtk-3-dev libwebkit2gtk-4.1-dev >/dev/null
			'"$BUILD"
fi

echo
echo "built: dist/$PKG-linux-$ARCH.deb   (version $VERSION, GTK3 variant)"
echo
echo "EXPERIMENTAL. Install with apt so the GTK and WebKit runtimes resolve:"
echo "  sudo apt install ./$PKG-linux-$ARCH.deb"
echo "Runs on Debian 12+ and Ubuntu 22.04+."

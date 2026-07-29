#!/bin/sh
# Build the probe as a Debian package into dist/, from any host with Docker.
#
# Wails needs CGO against GTK3 + WebKitGTK, so a Linux build must happen
# inside a real Linux userland — this script rents one from Docker rather
# than asking for a second machine. The image is golang:1.25-bookworm
# (Debian 12).
#
# DS-0 finding: this Wails alpha's DEFAULT Linux path is GTK4 +
# webkitgtk-6.0, and it needs GTK >= 4.10 (GtkFileDialog) — newer than
# Debian 12's 4.8. The complete GTK3 + webkit2gtk-4.1 variant lives
# behind `-tags gtk3`, and that is what this package builds: it runs on
# Debian 12+ and Ubuntu 22.04+. CI (ubuntu-24.04, GTK 4.14) covers the
# default GTK4 path, so both variants stay watched.
#
# ARCH picks the target (amd64 default — the common Debian desktop; arm64
# builds natively fast on Apple Silicon). On an Apple Silicon host the
# amd64 build runs under Rosetta — slower, but a build, not a marathon.
# Module and build caches persist in named Docker volumes, so only the
# first run pays full price.
#
# Unsigned, no repository, alpha testers only: install with
#     sudo apt install ./quiet-spaces-probe_0.0.1_amd64.deb
# (apt resolves the GTK/WebKit runtime dependencies; dpkg -i alone won't.)
set -e
cd "$(dirname "$0")"

ARCH="${ARCH:-amd64}"
VERSION="0.0.1"
PKG="quiet-spaces-probe"

mkdir -p dist

docker run --rm --platform "linux/$ARCH" \
	-v "$(cd ../.. && pwd)":/src \
	-v qp-deb-gomod:/go/pkg/mod \
	-v "qp-deb-gocache-$ARCH":/root/.cache/go-build \
	-w /src/cmd/wails-probe \
	golang:1.25-bookworm \
	sh -ec '
		export DEBIAN_FRONTEND=noninteractive
		apt-get update -qq
		apt-get install -qq -y --no-install-recommends \
			libgtk-3-dev libwebkit2gtk-4.1-dev >/dev/null

		ARCH="'"$ARCH"'"; VERSION="'"$VERSION"'"; PKG="'"$PKG"'"
		# Assemble the package tree in container-local /tmp: the bind mount
		# maps container root to an unprivileged macOS user, so building the
		# root-owned payload happens where root actually owns files.
		ROOT="/tmp/pkg"
		rm -rf "$ROOT"
		mkdir -p "$ROOT/DEBIAN" "$ROOT/usr/bin" \
			"$ROOT/usr/share/applications" \
			"$ROOT/usr/share/icons/hicolor/512x512/apps"

		go build -tags gtk3 -o "$ROOT/usr/bin/$PKG" .

		cp assets/app-icon.png \
			"$ROOT/usr/share/icons/hicolor/512x512/apps/$PKG.png"

		cat > "$ROOT/usr/share/applications/space.quiet.probe.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=Quiet Spaces Probe
Comment=DS-0 shell compatibility canary for Quiet Spaces
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
Description: Quiet Spaces probe shell (DS-0 canary)
 A minimal Wails shell that runs the full Quiet Spaces node and web UI
 locally, plus the DS-0 compatibility gates. Beta-test artifact; not a
 release.
CONTROL

		dpkg-deb --build --root-owner-group "$ROOT" \
			"dist/${PKG}_${VERSION}_${ARCH}.deb"
		dpkg-deb -I "dist/${PKG}_${VERSION}_${ARCH}.deb"
	'

echo "built: dist/${PKG}_${VERSION}_${ARCH}.deb"

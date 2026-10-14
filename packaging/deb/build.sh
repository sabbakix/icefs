#!/bin/sh
# Builds icefs_<version>_<arch>.deb into dist/.
# Usage: packaging/deb/build.sh <version> [amd64|arm64]
set -eu

VERSION=${1:?usage: build.sh <version> [arch]}
VERSION=${VERSION#v}
ARCH=${2:-amd64}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
chmod 0755 "$STAGE"

cd "$ROOT"
mkdir -p dist "$STAGE/DEBIAN" "$STAGE/usr/sbin" "$STAGE/usr/bin" \
	"$STAGE/lib/systemd/system" "$STAGE/etc/icefs" "$STAGE/usr/share/doc/icefs"

LDFLAGS="-s -w"
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -ldflags "$LDFLAGS" -o "$STAGE/usr/sbin/icefsd" ./cmd/icefsd
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -ldflags "$LDFLAGS" -o "$STAGE/usr/bin/icefs" ./cmd/icefs

install -m 0644 packaging/icefsd.service "$STAGE/lib/systemd/system/icefsd.service"
install -m 0600 packaging/config.default.toml "$STAGE/etc/icefs/config.toml"
install -m 0644 packaging/config.example.toml "$STAGE/usr/share/doc/icefs/config.example.toml"
install -m 0644 README.md "$STAGE/usr/share/doc/icefs/README.md"
for f in postinst prerm postrm; do
	install -m 0755 "packaging/deb/$f" "$STAGE/DEBIAN/$f"
done
echo /etc/icefs/config.toml > "$STAGE/DEBIAN/conffiles"

SIZE=$(du -sk "$STAGE" | cut -f1)
cat > "$STAGE/DEBIAN/control" <<CONTROL
Package: icefs
Version: $VERSION
Architecture: $ARCH
Maintainer: IceFs <noreply@github.com>
Installed-Size: $SIZE
Recommends: samba, e2fsprogs
Section: admin
Priority: optional
Homepage: https://github.com/sabbakix/icefs
Description: freezes finished Veeam backup files with the immutable flag
 IceFs watches Samba shares that Veeam backs up to. Once a .vbk or .vib
 file is finished it sets the Linux immutable flag so nothing can delete
 or change it. Whole chains melt again after a set number of days
 (default 15). Includes the icefsd service and the icefs TUI.
CONTROL

OUT="dist/icefs_${VERSION}_${ARCH}.deb"
dpkg-deb --root-owner-group --build "$STAGE" "$OUT" >/dev/null
echo "$OUT"

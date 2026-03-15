#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
version="${VERSION:-$("$repo_root/scripts/version.sh")}"
arch="${DEB_ARCH:-$(dpkg --print-architecture)}"
output_dir="${OUTPUT_DIR:-$repo_root/dist}"
pkgroot="$(mktemp -d)"
trap 'rm -rf "$pkgroot"' EXIT

mkdir -p "$output_dir"

DESTDIR="$pkgroot" PREFIX=/usr "$repo_root/scripts/stage-system.sh"
mkdir -p "$pkgroot/DEBIAN"

cat >"$pkgroot/DEBIAN/control" <<EOF
Package: ddcfast
Version: $version
Section: utils
Priority: optional
Architecture: $arch
Maintainer: KY <249657796+keewai704@users.noreply.github.com>
Depends: ddcutil
Description: Fast DDC/CI monitor control daemon and CLI
 ddcfast keeps DDC/CI brightness, contrast, and power changes
 responsive by reusing cached display state through a local daemon.
EOF

dpkg-deb --build --root-owner-group "$pkgroot" "$output_dir/ddcfast_${version}_${arch}.deb"

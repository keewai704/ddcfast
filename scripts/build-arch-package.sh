#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
version="${PKGVER:-${VERSION:-$("$repo_root/scripts/version.sh")}}"
pkgrel="${PKGREL:-1}"
output_dir="${OUTPUT_DIR:-$repo_root/dist}"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

mkdir -p "$output_dir"

archive="$tmpdir/ddcfast-${version}.tar.gz"
(
    cd "$repo_root"
    tar \
        --exclude-vcs \
        --exclude='./dist' \
        --exclude='./result' \
        -czf "$archive" \
        --transform "s,^\\.,ddcfast-${version}," \
        .
)
pkgsum="$(sha256sum "$archive" | awk '{print $1}')"

cp "$repo_root/packaging/arch/PKGBUILD" "$tmpdir/PKGBUILD"

(
    cd "$tmpdir"
    PKGVER="$version" PKGREL="$pkgrel" PKGSUM="$pkgsum" SOURCE_PATH="$archive" makepkg -f --clean
)

find "$tmpdir" -maxdepth 1 -type f -name '*.pkg.tar.*' -exec mv {} "$output_dir"/ \;

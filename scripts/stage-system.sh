#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
destdir="${DESTDIR:-}"
prefix="${PREFIX:-/usr}"
bindir="$destdir$prefix/bin"
sharedir="$destdir$prefix/share/ddcfast"
systemd_user_dir="$destdir$prefix/lib/systemd/user"

[ -n "$destdir" ] || {
    echo "DESTDIR must be set for staging installs" >&2
    exit 2
}

install -d "$bindir" "$sharedir" "$systemd_user_dir"

(
    cd "$repo_root"
    go build -trimpath -o "$bindir/ddcfast" .
)

install -Dm644 "$repo_root/examples/config.json" "$sharedir/config.json"
install -Dm644 "$repo_root/systemd/ddcfast.service" "$systemd_user_dir/ddcfast.service"

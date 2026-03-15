#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

if [ "${VERSION:-}" ]; then
    printf '%s\n' "$VERSION"
    exit 0
fi

if git -C "$repo_root" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    tag="$(git -C "$repo_root" describe --tags --match 'v[0-9]*' --exact-match 2>/dev/null || true)"
    if [ -n "$tag" ]; then
        printf '%s\n' "${tag#v}"
        exit 0
    fi

    short_rev="$(git -C "$repo_root" rev-parse --short HEAD)"
    printf '0.0.0+git.%s\n' "$short_rev"
    exit 0
fi

printf '0.0.0\n'

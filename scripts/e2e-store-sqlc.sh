#!/usr/bin/env bash
set -euo pipefail

store_dir="$(mktemp -d "${TMPDIR:-/tmp}/wacli-sqlc-e2e.XXXXXX")"
trap 'rm -rf "$store_dir"' EXIT

mkdir -p dist
CGO_ENABLED=1 CGO_CFLAGS="${CGO_CFLAGS:+$CGO_CFLAGS }-Wno-error=missing-braces" go build -tags sqlite_fts5 -o dist/wacli ./cmd/wacli

stats_json="$(./dist/wacli --store "$store_dir" --json store stats)"

case "$stats_json" in
  *'"success":true'*'"data"'*'"chats":0'*'"groups":0'*'"left_groups":0'*'"messages":0'*)
    ;;
  *)
    echo "unexpected store stats JSON: $stats_json" >&2
    exit 1
    ;;
esac

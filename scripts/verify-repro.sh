#!/usr/bin/env bash
# Builds twice into separate directories, the second time with an empty
# build cache so nothing is reused, and fails if any hash differs.
set -euo pipefail
cd "$(dirname "$0")/.."
OUT=dist ./build.sh >/dev/null
cache="$(mktemp -d)"
trap 'rm -rf "$cache"' EXIT
GOCACHE="$cache" OUT=dist-rebuild ./build.sh >/dev/null
if diff <(cd dist && cat SHA256SUMS) <(cd dist-rebuild && cat SHA256SUMS); then
  echo "reproducible: all $(wc -l < dist/SHA256SUMS | tr -d ' ') hashes match"
  rm -rf dist-rebuild
else
  echo "NOT reproducible" >&2; exit 1
fi

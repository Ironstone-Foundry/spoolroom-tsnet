#!/usr/bin/env bash
# Prints the sha256 table the watcher pins, from dist/manifest.json.
set -euo pipefail
cd "$(dirname "$0")/.."
M="${1:-dist/manifest.json}"
printf '%-13s %-64s %10s %10s\n' target "sha256 (decompressed binary)" bytes gz-bytes
grep -E '^    "' "$M" | sed -E 's/^ *"([^"]+)".*"gzSize": ([0-9]+), "sha256": "([0-9a-f]+)", "size": ([0-9]+).*/\1 \3 \4 \2/' |
  while read -r key sha size gz; do printf '%-13s %-64s %10s %10s\n' "$key" "$sha" "$size" "$gz"; done

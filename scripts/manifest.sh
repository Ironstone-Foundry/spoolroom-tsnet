#!/usr/bin/env bash
# Prints the manifest the watcher pins (watcher/tsnet-helper.json): per
# target, the gzip asset to download and the sha256 and size of the
# decompressed binary it must verify before running it.
set -euo pipefail
cd "$(dirname "$0")/.."
OUT="${OUT:-dist}"
VERSION="${VERSION:-$(tr -d '[:space:]' < VERSION)}"
TS="$(go list -m -f '{{.Version}}' tailscale.com)"
GOV="$(go env GOVERSION)"
base="https://github.com/Ironstone-Foundry/spoolroom-tsnet/releases/download/v${VERSION}"

sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
size() { wc -c < "$1" | tr -d ' '; }

printf '{\n  "helperVersion": "%s",\n  "tailscaleVersion": "%s",\n  "go": "%s",\n  "targets": {\n' "$VERSION" "$TS" "$GOV"
first=1
for f in $(cd "$OUT" && LC_ALL=C ls spoolroom-tsnet-* | grep -v '\.gz$' | LC_ALL=C sort); do
  key="${f#spoolroom-tsnet-}"; key="${key%.exe}"
  [[ $first == 1 ]] || printf ',\n'
  first=0
  printf '    "%s": {"url": "%s/%s.gz", "gzSha256": "%s", "gzSize": %s, "sha256": "%s", "size": %s}' \
    "$key" "$base" "$f" "$(sha "$OUT/$f.gz")" "$(size "$OUT/$f.gz")" "$(sha "$OUT/$f")" "$(size "$OUT/$f")"
done
printf '\n  }\n}\n'

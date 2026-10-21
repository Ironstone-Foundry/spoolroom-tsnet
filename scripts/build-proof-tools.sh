#!/usr/bin/env bash
# Builds what the PROOF.md device runs need into dist-proof/:
#   <target>/spoolroom-tsnet[.exe]            the release build (same hash as dist/)
#   <target>/spoolroom-tsnet-nopeerapi[.exe]  experiment: ts_omit_peerapiserver
#   <target>/fakegateway[.exe], drive[.exe]   test gateway and supervisor
# Targets: linux-x64 (headless Linux and Docker) and win32-x64 (Windows).
set -euo pipefail
cd "$(dirname "$0")/.."
P=dist-proof
rm -rf "$P" dist-nopeerapi
OUT=dist ./build.sh >/dev/null
OUT=dist-nopeerapi EXTRA_TAGS=ts_omit_peerapiserver,ts_omit_peerapiclient ./build.sh >/dev/null
for pair in linux-x64:linux:amd64 win32-x64:windows:amd64; do
  key=${pair%%:*}; rest=${pair#*:}; goos=${rest%%:*}; goarch=${rest#*:}
  ext=""; [[ $goos == windows ]] && ext=".exe"
  mkdir -p "$P/$key"
  cp "dist/spoolroom-tsnet-$key$ext" "$P/$key/spoolroom-tsnet$ext"
  cp "dist-nopeerapi/spoolroom-tsnet-$key$ext" "$P/$key/spoolroom-tsnet-nopeerapi$ext"
  for tool in fakegateway drive; do
    CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath -buildvcs=false -ldflags "-s -w" -o "$P/$key/$tool$ext" "./tools/$tool"
  done
  ( cd "$P/$key" && shasum -a 256 * > SHA256SUMS )
done
rm -rf dist-nopeerapi
echo "dist-proof ready:"; find "$P" -type f | sort

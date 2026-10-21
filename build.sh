#!/usr/bin/env bash
# Reproducible release build: six targets, cross-compiled with CGO off from
# any one machine. Writes dist/: binaries, .gz, SHA256SUMS, manifest.json.
# Usage: VERSION=0.1.0 ./build.sh   (VERSION defaults to the VERSION file)
set -euo pipefail
cd "$(dirname "$0")"

VERSION="${VERSION:-$(tr -d '[:space:]' < VERSION)}"
OUT="${OUT:-dist}"
# Features the helper never links in. logtail is off by owner decision (no
# log upload to Tailscale). Kept on purpose: acme (certificates), netstack,
# portmapper, tailnetlock (members may use Tailnet Lock), useproxy (members
# behind an HTTP proxy), c2n, cloud and captiveportal.
TAGS="ts_omit_logtail,ts_omit_netlog,ts_omit_oauthkey,ts_omit_identityfederation,ts_omit_aws,ts_omit_kube,ts_omit_ssh,ts_omit_taildrop,ts_omit_drive,ts_omit_tap,ts_omit_capture,ts_omit_relayserver,ts_omit_clientupdate,ts_omit_tpm,ts_omit_webclient,ts_omit_appconnectors,ts_omit_advertiseexitnode,ts_omit_advertiseroutes,ts_omit_bird,ts_omit_conn25,ts_omit_syspolicy,ts_omit_debugeventbus,ts_omit_portlist,ts_omit_posture"
# Stamp Tailscale's version so the coordination server sees a real client
# version (1.104.2) rather than a "-dev" build. Values come from go.mod and
# the module's recorded origin, so the build stays reproducible.
TS_VER="$(go list -m -f '{{.Version}}' tailscale.com)"
TS_SHORT="${TS_VER#v}"
TS_COMMIT="$(go mod download -json "tailscale.com@${TS_VER}" | sed -n 's/.*"Hash": "\([0-9a-f]\{40\}\)".*/\1/p' | head -1)"
[[ ${#TS_COMMIT} == 40 ]] || { echo "could not read tailscale.com commit" >&2; exit 1; }
# EXTRA_TAGS: for experiments only (e.g. ts_omit_peerapiserver, see
# docs/upstream-peerapi.md); release builds leave it empty.
TAGS="${TAGS}${EXTRA_TAGS:+,$EXTRA_TAGS}"
LDFLAGS="-s -w -buildid= -X main.version=${VERSION}"
LDFLAGS+=" -X tailscale.com/version.shortStamp=${TS_SHORT}"
LDFLAGS+=" -X tailscale.com/version.longStamp=${TS_SHORT}-t${TS_COMMIT:0:9}"
LDFLAGS+=" -X tailscale.com/version.gitCommitStamp=${TS_COMMIT}"

# watcher key (process.platform-process.arch) -> GOOS GOARCH GOARM
TARGETS=(
  "darwin-arm64 darwin arm64 -"
  "darwin-x64 darwin amd64 -"
  "linux-x64 linux amd64 -"
  "linux-arm64 linux arm64 -"
  "linux-arm linux arm 7"
  "win32-x64 windows amd64 -"
)

rm -rf "$OUT" && mkdir -p "$OUT"
go build -trimpath -buildvcs=false -o "$OUT/.gz-tool" ./tools/gz

for t in "${TARGETS[@]}"; do
  read -r key goos goarch goarm <<<"$t"
  ext=""; [[ $goos == windows ]] && ext=".exe"
  name="spoolroom-tsnet-${key}${ext}"
  env CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" GOARM="${goarm/-/}" GOFLAGS="${GOFLAGS:-}" \
    go build -trimpath -buildvcs=false -tags "$TAGS" -ldflags "$LDFLAGS" -o "$OUT/$name" .
  "$OUT/.gz-tool" "$OUT/$name"
  echo "built $name"
done
rm -f "$OUT/.gz-tool"

( cd "$OUT" && LC_ALL=C ls spoolroom-tsnet-* | LC_ALL=C sort | xargs shasum -a 256 > SHA256SUMS )
VERSION="$VERSION" OUT="$OUT" scripts/manifest.sh > "$OUT/manifest.json"
echo "wrote $OUT/SHA256SUMS and $OUT/manifest.json"

#!/usr/bin/env bash
# Regenerates THIRD_PARTY_NOTICES.md from the license files of every module
# linked into any release target. Run after changing go.mod; CI checks the
# committed file is current.
set -euo pipefail
cd "$(dirname "$0")/.."
TAGS="$(sed -n 's/^TAGS="\(.*\)"$/\1/p' build.sh | head -1)"
mods=$(
  for t in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 linux/arm windows/amd64; do
    GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go list -tags "$TAGS" -deps \
      -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' .
  done | LC_ALL=C sort -u
)
{
  echo "# Third-party notices"
  echo
  echo "spoolroom-tsnet uses Tailscale and the open-source Go modules below. Each"
  echo "module's license text follows, as found in the module at the pinned version."
  goroot="$(go env GOROOT)"
  echo
  echo "## Go standard library and runtime $(go env GOVERSION)"
  echo
  for f in "$goroot/LICENSE" "$goroot/PATENTS"; do
    echo '```'; cat "$f"; echo '```'
  done
  while read -r path ver dir; do
    [[ -z $path ]] && continue
    echo; echo "## $path $ver"; echo
    found=0
    for f in "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/COPYING* "$dir"/NOTICE* "$dir"/PATENTS; do
      [[ -f $f ]] || continue
      found=1
      echo '```'; cat "$f"; echo '```'
    done
    [[ $found == 1 ]] || echo "(no license file in module root; see the module source)"
  done <<<"$mods"
} > THIRD_PARTY_NOTICES.md
echo "THIRD_PARTY_NOTICES.md: $(grep -c '^## ' THIRD_PARTY_NOTICES.md) modules"

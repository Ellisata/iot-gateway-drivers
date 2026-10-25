#!/usr/bin/env bash
# Build every official driver for the current GOOS/GOARCH.
# Usage: ./build.sh [output-dir]   (default: ./plugins)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="${1:-$ROOT/plugins}"
EXT=""
[ "${GOOS:-$(go env GOOS)}" = "windows" ] && EXT=".exe"

# Plugin Meta.Version 由构建期注入，避免每个 main.go 手填后与 tag 漂移。
VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --abbrev=0 2>/dev/null || echo dev)}"

mkdir -p "$OUT"
built=0
for d in "$ROOT"/*/; do
    [ -f "${d}main.go" ] || continue
    name="$(basename "$d")"
    echo "  [driver] $name ($VERSION)"
    (cd "$ROOT" && go build -ldflags="-s -w -X main.version=$VERSION" -o "$OUT/iot-driver-${name}${EXT}" "./${name}")
    built=$((built + 1))
done

[ "$built" -gt 0 ] || { echo "no drivers built" >&2; exit 1; }
echo "built $built drivers into $OUT"

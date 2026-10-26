#!/usr/bin/env bash
# Build every official driver, cross-compiled for the gateway's target platforms.
#
# Usage:
#   ./build.sh                       # all platforms (default)
#   ./build.sh windows               # windows/amd64 only
#   ./build.sh linux                 # linux/amd64 only
#   ./build.sh arm64                 # linux/arm64 only
#   ./build.sh arm32                 # linux/arm (GOARM=7) only
#   ./build.sh native                # current platform only
#   ./build.sh <target> [out-dir]    # single platform into an explicit directory
#   ./build.sh [out-dir]             # legacy: native build into <out-dir>
#
#   VERSION=v0.2.0 ./build.sh        # override the injected Meta.Version
#
# Output directories — one per platform, because every driver has the same file
# name (iot-driver-<name>[.exe]) on every platform: a shared directory would
# silently overwrite one architecture with another, and the gateway would skip
# every mismatched plugin without reporting an error.
#
#   linux/amd64   -> plugins-linux/         windows/amd64 -> plugins-windows/
#   linux/arm64   -> plugins-linux-arm64/   native        -> plugins/
#   linux/arm(v7) -> plugins-linux-arm/
#
# Copy the matching directory's contents into the gateway's plugins/ directory
# (next to the gateway executable). Plugins must match the gateway's GOOS/GOARCH
# and GOARM — a wrong-architecture plugin is silently skipped at runtime.
#
# arm32 is pinned to GOARM=7 (ARMv7-A + VFPv3). For older boards (ARMv6/ARMv5,
# e.g. Raspberry Pi 1 / Zero) use: GOARM=5 ./build.sh arm32
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

TARGET=""
OUT=""
while [ $# -gt 0 ]; do
    case "$1" in
        -o|--out)
            [ -z "$OUT" ] || { echo "$1 given more than once" >&2; exit 1; }
            OUT="${2:?missing value for $1}"
            shift 2
            ;;
        all|windows|win|linux|arm64|arm32|arm7|arm|native)
            [ -z "$TARGET" ] || { echo "unexpected argument: $1" >&2; exit 1; }
            TARGET="$1"
            shift
            ;;
        *)
            # Legacy form: ./build.sh [output-dir]
            [ -z "$OUT" ] || { echo "unexpected argument: $1" >&2; exit 1; }
            OUT="$1"
            shift
            ;;
    esac
done

if [ -n "$TARGET" ]; then
    case "$TARGET" in
        all|windows|linux|arm64|arm32|native) ;;
        win)         TARGET=windows ;;
        arm7|arm)    TARGET=arm32 ;;
        *) echo "unknown target: $TARGET (use all | windows | linux | arm64 | arm32 | native)" >&2; exit 1 ;;
    esac
else
    # No target given: a bare out-dir keeps the old single-platform behavior.
    if [ -n "$OUT" ]; then TARGET=native; else TARGET=all; fi
fi

# `all` writes one directory per platform, so a single explicit dir makes no sense.
if [ "$TARGET" = "all" ] && [ -n "$OUT" ]; then
    echo "error: an explicit output dir needs a single target (try: ./build.sh native '$OUT')" >&2
    exit 1
fi

# Plugin Meta.Version is injected at build time so it cannot drift from the tag
# and every main.go does not have to hard-code it.
VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --abbrev=0 2>/dev/null || echo dev)}"

platform_dir() { # $1=GOOS $2=GOARCH
    # amd64 keeps the short name (plugins-linux / plugins-windows), matching
    # iot-gateway's build.sh and release.sh so the dirs can be used directly.
    if [ "$2" = "amd64" ]; then
        echo "$ROOT/plugins-$1"
    else
        echo "$ROOT/plugins-$1-$2"
    fi
}

build_one() { # $1=GOOS $2=GOARCH $3=output-dir [$4=GOARM]
    local goos="$1" goarch="$2" out="$3" goarm="${4:-}"
    local -a genv=(CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch")
    # GOARM only applies (and is only passed) for GOARCH=arm. Inject it through
    # env per call instead of exporting, so it cannot leak into another platform.
    [ -n "$goarm" ] && genv+=(GOARM="$goarm")

    local ext="" built=0 name
    [ "$goos" = "windows" ] && ext=".exe"

    mkdir -p "$out"
    out="$(cd "$out" && pwd)"
    # Drop previous outputs: a removed or renamed driver must not linger as a
    # stale, differently-versioned plugin next to the freshly built ones.
    rm -f "$out"/iot-driver-*

    echo "  [$goos/$goarch${goarm:+/$goarm}] -> $out"
    for d in "$ROOT"/*/; do
        [ -f "${d}main.go" ] || continue
        name="$(basename "$d")"
        ( cd "$ROOT" && env "${genv[@]}" go build \
            -ldflags="-s -w -X main.version=${VERSION}" \
            -o "$out/iot-driver-${name}${ext}" "./${name}" )
        echo "    [OK] iot-driver-${name}${ext}"
        built=$((built + 1))
    done

    # Zero plugins is always a broken environment, never a valid result.
    if [ "$built" = "0" ]; then
        echo "  [FAIL] $goos/$goarch: no driver was built" >&2
        return 1
    fi
    echo "  [OK] $built drivers for $goos/$goarch"
}

build_platform() { # $1=canonical target
    local goos goarch goarm="" out
    case "$1" in
        windows) goos=windows; goarch=amd64 ;;
        linux)   goos=linux;   goarch=amd64 ;;
        arm64)   goos=linux;   goarch=arm64 ;;
        arm32)   goos=linux;   goarch=arm; goarm="${GOARM:-7}" ;;
    esac
    out="${OUT:-$(platform_dir "$goos" "$goarch")}"
    build_one "$goos" "$goarch" "$out" "$goarm"
}

echo "version: $VERSION"
case "$TARGET" in
    all)
        build_platform linux
        build_platform arm64
        build_platform arm32
        build_platform windows
        ;;
    native)
        goos="${GOOS:-$(go env GOOS)}"
        goarch="${GOARCH:-$(go env GOARCH)}"
        goarm=""
        [ "$goarch" = "arm" ] && goarm="${GOARM:-7}"
        build_one "$goos" "$goarch" "${OUT:-$ROOT/plugins}" "$goarm"
        ;;
    *)
        build_platform "$TARGET"
        ;;
esac

echo
echo "done: $TARGET built with version $VERSION"

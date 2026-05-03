#!/usr/bin/env bash
# build-binaries.sh -- cross-compile noo-noo and noo-nood for darwin/amd64
# and darwin/arm64, then fuse each into a universal Mach-O via lipo.
#
# Inputs (env):
#   VERSION   git tag like v0.4.0; defaults to "vDEV" if unset.
# Outputs:
#   dist/noo-noo                      universal binary
#   dist/noo-nood                     universal binary
#   dist/noo-noo-${VERSION}-darwin.tar.gz   tarball of both
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
VERSION="${VERSION:-vDEV}"
DIST="${ROOT}/dist"
# T79: dist/ is created by scripts/release.sh too, but `mkdir -p` makes this
# script safe to invoke standalone (e.g. for a quick CLI-only rebuild).
mkdir -p "${DIST}"

LDFLAGS="-s -w -X main.version=${VERSION}"

build_one() {
    local pkg="$1" arch="$2" out="$3"
    echo "==> building ${pkg} for darwin/${arch} -> ${out}"
    GOOS=darwin GOARCH="${arch}" CGO_ENABLED=0 \
        go build -trimpath -ldflags="${LDFLAGS}" -o "${out}" "${pkg}"
}

fuse() {
    local name="$1"
    echo "==> fusing ${name} via lipo"
    lipo -create \
        -output "${DIST}/${name}" \
        "${DIST}/${name}-amd64" \
        "${DIST}/${name}-arm64"
    rm -f "${DIST}/${name}-amd64" "${DIST}/${name}-arm64"
    file "${DIST}/${name}" | grep -q 'Mach-O universal'
}

for cmd in noo-noo noo-nood; do
    build_one "./cmd/${cmd}" amd64 "${DIST}/${cmd}-amd64"
    build_one "./cmd/${cmd}" arm64 "${DIST}/${cmd}-arm64"
    fuse "${cmd}"
done

echo "==> tarball noo-noo-${VERSION}-darwin.tar.gz"
tar -czf "${DIST}/noo-noo-${VERSION}-darwin.tar.gz" \
    -C "${DIST}" noo-noo noo-nood

echo "==> done"
ls -lh "${DIST}"

#!/usr/bin/env bash
# checksums.sh -- generate dist/checksums.txt covering every release artifact.
#
# Inputs (files):
#   dist/*    everything produced by build-binaries.sh, build-app.sh,
#             build-dmg.sh.
# Outputs:
#   dist/checksums.txt   plain `shasum -a 256` format
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
DIST="${ROOT}/dist"
OUT="${DIST}/checksums.txt"

if [[ ! -d "${DIST}" ]]; then
    echo "ERROR: ${DIST} missing; run build-* scripts first" >&2
    exit 1
fi

echo "==> generating ${OUT}"
: > "${OUT}"
# include only release-shaped artifacts; skip raw binaries from intermediate
# steps (they're not uploaded).
shopt -s nullglob
files=(
    "${DIST}/noo-noo"
    "${DIST}/noo-nood"
    "${DIST}/Noo-Noo.app.zip"
    "${DIST}"/Noo-Noo-*.dmg
    "${DIST}"/noo-noo-*-darwin.tar.gz
)
for f in "${files[@]}"; do
    [[ -f "$f" ]] || continue
    (cd "${DIST}" && shasum -a 256 "$(basename "$f")") >> "${OUT}"
done

# stable order
sort -k 2 -o "${OUT}" "${OUT}"

# T79: refuse to write an empty manifest -- if the glob caught nothing,
# something upstream is broken and the release would ship without checksums.
if [[ ! -s "${OUT}" ]]; then
    echo "ERROR: ${OUT} is empty; no release artifacts found in ${DIST}" >&2
    exit 1
fi

echo "==> done"
cat "${OUT}"

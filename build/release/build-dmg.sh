#!/usr/bin/env bash
# build-dmg.sh -- wrap dist/Noo-Noo.app in a .dmg via hdiutil.
#
# Inputs (env):
#   VERSION   git tag like v0.4.0; defaults to "vDEV" if unset.
# Inputs (files):
#   dist/Noo-Noo.app          from build-app.sh
# Outputs:
#   dist/Noo-Noo-${VERSION}.dmg
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
VERSION="${VERSION:-vDEV}"
DIST="${ROOT}/dist"
APP="${DIST}/Noo-Noo.app"
DMG="${DIST}/Noo-Noo-${VERSION}.dmg"

if [[ ! -d "${APP}" ]]; then
    echo "ERROR: ${APP} missing; run build-app.sh first" >&2
    exit 1
fi

# T79: detach any stale Noo-Noo volume from a previous failed run so
# `hdiutil create` doesn't trip "resource busy".
for vol in /Volumes/Noo-Noo*; do
    [[ -d "$vol" ]] || continue
    hdiutil detach -force "$vol" >/dev/null 2>&1 || true
done

echo "==> staging"
STAGE="$(mktemp -d -t noo-noo-dmg)"
trap 'rm -rf "${STAGE}"' EXIT
cp -R "${APP}" "${STAGE}/Noo-Noo.app"
ln -s /Applications "${STAGE}/Applications"

echo "==> hdiutil create ${DMG}"
rm -f "${DMG}"
hdiutil create \
    -volname "Noo-Noo ${VERSION}" \
    -srcfolder "${STAGE}" \
    -ov \
    -format UDZO \
    -fs HFS+ \
    -imagekey zlib-level=9 \
    "${DMG}" >/dev/null

echo "==> verifying"
hdiutil verify "${DMG}" >/dev/null

echo "==> done"
ls -lh "${DMG}"

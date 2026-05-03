#!/usr/bin/env bash
# build-app.sh -- assemble Noo-Noo.app bundle and sign it.
#
# Inputs (env):
#   VERSION       git tag like v0.4.0; defaults to "vDEV" if unset.
#   DEVELOPER_ID  Apple Developer ID for production signing. If unset,
#                 ad-hoc signing is used (Phase 0.4 default).
#                 TODO 0.4.1: enable Developer ID + notarization.
# Inputs (files):
#   dist/noo-noo, dist/noo-nood   from build-binaries.sh
#   cmd/noo-noo-app/...           Wails source (Phase 0.3)
# Outputs:
#   dist/Noo-Noo.app               assembled, signed bundle
#   dist/Noo-Noo.app.zip           ditto-compressed for upload
set -euo pipefail

# Pin deployment target so Wails v3 alpha (which compiles native objects
# targeting the SDK SDK version) doesn't trigger linker version warnings
# against Go's default. 11.0 = Big Sur, the oldest macOS we support.
export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-11.0}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
VERSION="${VERSION:-vDEV}"
SHORT_VERSION="${VERSION#v}"
DIST="${ROOT}/dist"
APP="${DIST}/Noo-Noo.app"
CONTENTS="${APP}/Contents"
MACOS="${CONTENTS}/MacOS"
RES="${CONTENTS}/Resources"

echo "==> building Wails app binary"
# T79: ensure frontend deps are installed before vite build (CI/clean checkouts
# won't have node_modules from a prior dev run). Prefer reproducible `npm ci`
# when a lockfile exists; fall back to `npm install` otherwise.
(
    cd cmd/noo-noo-app/frontend
    if [[ -f package-lock.json ]]; then
        npm ci
    else
        npm install --no-audit --no-fund
    fi
    npm run build
)
GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 \
    go build -trimpath -tags production \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o "${DIST}/noo-noo-app-amd64" ./cmd/noo-noo-app
GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 \
    go build -trimpath -tags production \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o "${DIST}/noo-noo-app-arm64" ./cmd/noo-noo-app
lipo -create \
    -output "${DIST}/noo-noo-app" \
    "${DIST}/noo-noo-app-amd64" \
    "${DIST}/noo-noo-app-arm64"
rm -f "${DIST}/noo-noo-app-amd64" "${DIST}/noo-noo-app-arm64"

echo "==> assembling ${APP}"
rm -rf "${APP}"
mkdir -p "${MACOS}" "${RES}"

cp "${DIST}/noo-noo-app" "${MACOS}/Noo-Noo"
chmod +x "${MACOS}/Noo-Noo"
# bundle the CLI binaries inside the app for the postflight installer
cp "${DIST}/noo-noo" "${MACOS}/noo-noo"
cp "${DIST}/noo-nood" "${MACOS}/noo-nood"
cp cmd/noo-noo-app/build/appicon.png "${RES}/appicon.png"

echo "==> rendering Info.plist"
sed \
    -e "s/__VERSION__/${SHORT_VERSION}/g" \
    -e "s/__BUILD__/$(date +%Y%m%d%H%M)/g" \
    cmd/noo-noo-app/Info.plist.tmpl \
    > "${CONTENTS}/Info.plist"

echo "==> codesign"
if [[ -n "${DEVELOPER_ID:-}" ]]; then
    # 0.4.1 path: real Developer ID + hardened runtime + timestamp
    codesign --sign "${DEVELOPER_ID}" \
        --options runtime \
        --timestamp \
        --deep --force \
        "${APP}"
else
    # 0.4 default: ad-hoc signature
    codesign --sign - --deep --force "${APP}"
fi
codesign --verify --deep --strict --verbose=2 "${APP}"

echo "==> zipping bundle"
ditto -c -k --sequesterRsrc --keepParent "${APP}" "${DIST}/Noo-Noo.app.zip"

echo "==> done"
ls -lh "${DIST}/Noo-Noo.app" "${DIST}/Noo-Noo.app.zip"

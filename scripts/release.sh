#!/usr/bin/env bash
# release.sh -- local pre-tag smoke runner for the release pipeline.
#
# Usage:
#   scripts/release.sh --dry-run        # build artifacts into dist/, no upload
#   scripts/release.sh --version v0.4.0 # specify version explicitly
#   scripts/release.sh                  # default: --dry-run with VERSION=vTEST
#
# Whether or not --dry-run is passed, this script never uploads to GitHub
# or pushes to the tap repo -- those steps live only in CI.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

DRY_RUN=1
VERSION="${VERSION:-vTEST}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run)
            DRY_RUN=1
            shift
            ;;
        --version)
            VERSION="$2"
            shift 2
            ;;
        --help|-h)
            sed -n '2,12p' "${BASH_SOURCE[0]}"
            exit 0
            ;;
        *)
            echo "unknown arg: $1" >&2
            exit 2
            ;;
    esac
done

export VERSION

echo "=================================================================="
echo " release.sh   VERSION=${VERSION}   DRY_RUN=${DRY_RUN}"
echo "=================================================================="

rm -rf dist
mkdir -p dist

echo
echo "==> (1/4) build-binaries.sh"
bash build/release/build-binaries.sh

echo
echo "==> (2/4) build-app.sh"
bash build/release/build-app.sh

echo
echo "==> (3/4) build-dmg.sh"
bash build/release/build-dmg.sh

echo
echo "==> (4/4) checksums.sh"
bash build/release/checksums.sh

echo
echo "=================================================================="
echo " LOCAL ARTIFACTS"
echo "=================================================================="
ls -lh dist/

if [[ "${DRY_RUN}" == "1" ]]; then
    echo
    echo "DRY-RUN: skipped GitHub Release upload + tap repo push."
    echo "To do a real release, push a git tag (e.g. git push origin v0.4.0)."
fi

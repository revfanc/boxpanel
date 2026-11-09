#!/usr/bin/env bash
# Frontend must already be built. Keep the exact archive contract in one place.
set -euo pipefail
: "${RELEASE_TAG:?Set RELEASE_TAG to vX.Y.Z or vX.Y.Z-rc.N}"
[[ $RELEASE_TAG =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9]+(\.[A-Za-z0-9]+)*)?$ ]] || { echo 'Invalid release tag' >&2; exit 1; }
output=${1:?Usage: RELEASE_TAG=vX.Y.Z bash scripts/build-release.sh OUTPUT_DIR}
mkdir -p "$output"
output=$(cd "$output" && pwd)
stage=$(mktemp -d)
trap 'rm -rf -- "$stage"' EXIT
export CGO_ENABLED=0 COPYFILE_DISABLE=1
for arch in amd64 arm64; do
    GOOS=linux GOARCH="$arch" "${GO:-go}" build -trimpath -ldflags "-s -w -X main.version=$RELEASE_TAG" -o "$stage/boxpanel" ./cmd/boxpanel
    tar -czf "$output/boxpanel-linux-$arch.tar.gz" -C "$stage" boxpanel
done
(
    cd "$output"
    if command -v sha256sum >/dev/null; then
        sha256sum boxpanel-linux-amd64.tar.gz boxpanel-linux-arm64.tar.gz >checksums.txt
    else
        shasum -a 256 boxpanel-linux-amd64.tar.gz boxpanel-linux-arm64.tar.gz >checksums.txt
    fi
)

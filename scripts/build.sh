#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
GO=${GO:-go}
VERSION=${VERSION:-dev}
COMMIT=${COMMIT:-$(git describe --always --dirty)}
BUILD_DATE=${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
mkdir -p dist
for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
    target_os=${target%/*}
    target_arch=${target#*/}
    artifact="lbctl_${target_os}_${target_arch}"
    if [ "$target_os" = windows ]; then artifact="${artifact}.exe"; fi
    CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" "$GO" build -trimpath \
        -ldflags "-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.buildDate=$BUILD_DATE" \
        -o "dist/$artifact" ./cmd/lbctl
done
cd dist
if command -v sha256sum >/dev/null 2>&1; then
    sha256sum lbctl_* > checksums.txt
else
    shasum -a 256 lbctl_* > checksums.txt
fi

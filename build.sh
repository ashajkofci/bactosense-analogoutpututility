#!/bin/sh
set -eu

VERSION="${1:-1.0.0}"
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$SCRIPT_DIR"

go test ./...
mkdir -p release
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build \
  -trimpath \
  -ldflags "-H=windowsgui -s -w -X main.version=$VERSION" \
  -o release/AnalogOutputUtility.exe \
  .

CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build \
  -trimpath \
  -ldflags "-s -w -X main.version=$VERSION" \
  -o release/AnalogOutputUtility-macos-arm64 \
  .

CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build \
  -trimpath \
  -ldflags "-s -w -X main.version=$VERSION" \
  -o release/AnalogOutputUtility-macos-amd64 \
  .

if command -v sha256sum >/dev/null 2>&1; then
  sha256sum release/AnalogOutputUtility.exe release/AnalogOutputUtility-macos-arm64 release/AnalogOutputUtility-macos-amd64
else
  shasum -a 256 release/AnalogOutputUtility.exe release/AnalogOutputUtility-macos-arm64 release/AnalogOutputUtility-macos-amd64
fi

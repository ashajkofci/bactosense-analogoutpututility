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

sha256sum release/AnalogOutputUtility.exe

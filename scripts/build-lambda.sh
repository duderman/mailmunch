#!/usr/bin/env bash
set -euo pipefail

NAME="${1:-hello}"
ZIP_PATH="${2:-dist/hello.zip}"

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
SRC_DIR="$ROOT_DIR/lambda/$NAME"
DIST_DIR="$ROOT_DIR/dist"
BIN_DIR="$DIST_DIR/$NAME"

mkdir -p "$BIN_DIR"
mkdir -p "$DIST_DIR"

export GOOS=linux
export GOARCH=arm64
export CGO_ENABLED=0

# Build binary (bootstrap for custom runtime)
pushd "$SRC_DIR" >/dev/null
go build -ldflags='-s -w' -o "$BIN_DIR/bootstrap" ./
popd >/dev/null

# Package zip
pushd "$BIN_DIR" >/dev/null
zip -q -r "../${NAME}.zip" bootstrap
popd >/dev/null

echo "Built Lambda package at $ZIP_PATH"

#!/usr/bin/env bash
set -euo pipefail

OUTPUT_DIR="${1:-dist}"
VERSION=$(tr -d '[:space:]' <VERSION)
if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "VERSION must contain a semantic version such as 0.1.0" >&2
  exit 2
fi
mkdir -p "$OUTPUT_DIR"

if ! command -v npm >/dev/null 2>&1; then
  echo "npm is required to build the embedded management UI" >&2
  exit 2
fi

npm --prefix frontend ci
npm --prefix frontend run build

build() {
  local package="$1"
  local output="$2"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags="-s -w -X simple_cdn/internal/version.Version=$VERSION" \
    -o "$OUTPUT_DIR/$output" "$package"
}

build ./cmd/control cdn-control-linux-amd64
build ./cmd/edge-agent cdn-edge-agent-linux-amd64

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$OUTPUT_DIR" && sha256sum *-linux-amd64 >SHA256SUMS)
else
  (cd "$OUTPUT_DIR" && shasum -a 256 *-linux-amd64 >SHA256SUMS)
fi

echo "Built Linux AMD64 release assets in $OUTPUT_DIR"

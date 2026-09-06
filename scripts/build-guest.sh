#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p guestbin
staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT
for arch in arm64 amd64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" "${GO:-go}" build -trimpath -ldflags '-s -w' -o "$staging/verify" ./cmd/dev-sandbox-verify
  gzip -n -c "$staging/verify" > "$staging/verify-linux-$arch.gz"
  mv "$staging/verify-linux-$arch.gz" guestbin/
done

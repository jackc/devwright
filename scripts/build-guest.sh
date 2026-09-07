#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p guestbin
staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT
for goos in linux darwin; do
for arch in arm64 amd64; do
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$arch" "${GO:-go}" build -trimpath -ldflags '-s -w' -o "$staging/verify" ./cmd/devwright-verify
  gzip -n -c "$staging/verify" > "$staging/verify-$goos-$arch.gz"
  mv "$staging/verify-$goos-$arch.gz" guestbin/
done
done

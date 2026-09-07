#!/bin/bash
# Snapshot by default; publishing requires an exact version tag on a clean tree.
set -euo pipefail
cd "$(dirname "$0")/.."
mode=${1:-snapshot}
case "$mode" in
  snapshot)
    exec "${GORELEASER:-goreleaser}" release --clean --snapshot
    ;;
  build|publish)
    version=$(git describe --tags --exact-match)
    [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$ ]] || {
      echo 'Expected HEAD tagged vX.Y.Z' >&2
      exit 1
    }
    if [[ "$mode" == publish ]]; then
      : "${GITHUB_TOKEN:?Set GITHUB_TOKEN to publish a release}"
      "${GORELEASER:-goreleaser}" release --clean --draft
    else
      "${GORELEASER:-goreleaser}" release --clean --skip=publish
    fi
    repository=${GITHUB_REPOSITORY:-$(gh repo view --json nameWithOwner --jq .nameWithOwner)}
    bash scripts/release-formula.sh "$version" "$repository"
    if [[ "$mode" == publish ]]; then
      GH_TOKEN="$GITHUB_TOKEN" gh release upload "$version" .build/releases/devwright.rb --repo "$repository"
      GH_TOKEN="$GITHUB_TOKEN" gh release edit "$version" --draft=false --repo "$repository"
    fi
    ;;
  *)
    echo 'Usage: mise run release [snapshot|build|publish]' >&2
    exit 1
    ;;
esac

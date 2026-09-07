#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
"${GO:-go}" mod download
bash scripts/build-guest.sh
mkdir -p .build/release-licenses
for dependency in golang.org/x/sys github.com/pelletier/go-toml/v2 github.com/google/jsonschema-go; do
  module_dir=$("${GO:-go}" list -m -f '{{.Dir}}' "$dependency")
  case "$dependency" in
    golang.org/x/sys) name=golang-x-sys ;;
    github.com/pelletier/go-toml/v2) name=go-toml ;;
    github.com/google/jsonschema-go) name=jsonschema-go ;;
  esac
  cp "$module_dir/LICENSE" ".build/release-licenses/$name.txt"
done
cp internal/claudepolicy/schema/LICENSE .build/release-licenses/claude-settings-schema.txt

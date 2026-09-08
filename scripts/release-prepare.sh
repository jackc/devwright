#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
"${GO:-go}" mod download
mkdir -p .build/release-licenses
for dependency in golang.org/x/sys golang.org/x/term gopkg.in/yaml.v3 github.com/spf13/cobra github.com/spf13/pflag github.com/inconshreveable/mousetrap; do
  module_dir=$("${GO:-go}" list -m -f '{{.Dir}}' "$dependency")
  case "$dependency" in
    golang.org/x/sys) name=golang-x-sys ;;
    golang.org/x/term) name=golang-x-term ;;
    gopkg.in/yaml.v3) name=yaml ;;
    github.com/spf13/cobra) name=cobra ;;
    github.com/spf13/pflag) name=pflag ;;
    github.com/inconshreveable/mousetrap) name=mousetrap ;;
  esac
  license="$module_dir/LICENSE"
  if [ ! -f "$license" ]; then license="$module_dir/LICENSE.txt"; fi
  cp -f "$license" ".build/release-licenses/$name.txt"
done

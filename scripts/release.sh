#!/bin/bash
# Build distributable binaries. Pass OWNER/REPO to also generate a Homebrew formula.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
version=${1:?Usage: bash scripts/release.sh vX.Y.Z [OWNER/REPO]}
repository=${2:-}
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$ ]]; then
  echo 'Expected a version such as v0.1.0' >&2
  exit 1
fi
if [[ -n "$repository" && ! "$repository" =~ ^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$ ]]; then
  echo 'Expected a GitHub repository in OWNER/REPO form' >&2
  exit 1
fi
dist="$root/.build/releases/$version"
mkdir -p "$dist"
staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT
mkdir -p "$staging/licenses"
cp README.md DEVELOPMENT.md VALIDATION.md CLAUDE-CODE-DESIGN.md "$staging/"
cp -R docs "$staging/"
go mod download
bash scripts/build-guest.sh
module_dir=$(go list -m -f '{{.Dir}}' golang.org/x/sys)
cp "$module_dir/LICENSE" "$staging/licenses/golang-x-sys.txt"
module_dir=$("${GO:-go}" list -m -f '{{.Dir}}' github.com/pelletier/go-toml/v2)
cp "$module_dir/LICENSE" "$staging/licenses/go-toml.txt"
module_dir=$("${GO:-go}" list -m -f '{{.Dir}}' github.com/google/jsonschema-go)
cp "$module_dir/LICENSE" "$staging/licenses/jsonschema-go.txt"
cp internal/claudepolicy/schema/LICENSE "$staging/licenses/claude-settings-schema.txt"
for platform in darwin_arm64 darwin_amd64 linux_arm64 linux_amd64; do
  goos=${platform%_*}
  arch=${platform#*_}
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$version" -o "$staging/devwright" ./cmd/devwright
  COPYFILE_DISABLE=1 tar --format=ustar -czf "$dist/devwright_${version}_${platform}.tar.gz" \
    -C "$staging" devwright README.md DEVELOPMENT.md VALIDATION.md CLAUDE-CODE-DESIGN.md docs licenses
done
(
  cd "$dist"
  shasum -a 256 devwright_*.tar.gz > checksums.txt
)
if [[ -n "$repository" ]]; then
  checksum() { shasum -a 256 "$dist/devwright_${version}_$1.tar.gz" | cut -d ' ' -f 1; }
  base="https://github.com/$repository/releases/download/$version"
  cat > "$dist/devwright.rb" <<FORMULA
class Devwright < Formula
  desc "Manage development VMs, containers, or restricted users"
  homepage "https://github.com/$repository"
  version "${version#v}"

  on_macos do
    depends_on "lima"
    on_arm do
      url "$base/devwright_${version}_darwin_arm64.tar.gz"
      sha256 "$(checksum darwin_arm64)"
    end
    on_intel do
      url "$base/devwright_${version}_darwin_amd64.tar.gz"
      sha256 "$(checksum darwin_amd64)"
    end
  end
  on_linux do
    on_arm do
      url "$base/devwright_${version}_linux_arm64.tar.gz"
      sha256 "$(checksum linux_arm64)"
    end
    on_intel do
      url "$base/devwright_${version}_linux_amd64.tar.gz"
      sha256 "$(checksum linux_amd64)"
    end
  end

  def install
    bin.install "devwright"
    doc.install "README.md", "DEVELOPMENT.md", "VALIDATION.md", "CLAUDE-CODE-DESIGN.md", "docs", "licenses"
  end

  test do
    assert_match "devwright $version", shell_output("#{bin}/devwright --version")
    assert_equal true, JSON.parse(shell_output("#{bin}/devwright render"))["plain"]
  end
end
FORMULA
fi
echo "Release files: $dist"

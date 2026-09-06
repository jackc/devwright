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
cp README.md VALIDATION.md "$staging/"
go mod download
for dependency in term sys; do
  module_dir=$(go list -m -f '{{.Dir}}' "golang.org/x/$dependency")
  cp "$module_dir/LICENSE" "$staging/licenses/golang-x-$dependency.txt"
done
for platform in darwin_arm64 darwin_amd64 linux_arm64 linux_amd64; do
  goos=${platform%_*}
  arch=${platform#*_}
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$version" -o "$staging/agent-vm" ./cmd/agent-vm
  COPYFILE_DISABLE=1 tar --format=ustar -czf "$dist/agent-vm_${version}_${platform}.tar.gz" \
    -C "$staging" agent-vm README.md VALIDATION.md licenses
done
(
  cd "$dist"
  shasum -a 256 agent-vm_*.tar.gz > checksums.txt
)
if [[ -n "$repository" ]]; then
  checksum() { shasum -a 256 "$dist/agent-vm_${version}_$1.tar.gz" | cut -d ' ' -f 1; }
  base="https://github.com/$repository/releases/download/$version"
  cat > "$dist/agent-vm.rb" <<FORMULA
class AgentVm < Formula
  desc "Provision isolated Lima development VMs"
  homepage "https://github.com/$repository"
  version "${version#v}"

  on_macos do
    on_arm do
      url "$base/agent-vm_${version}_darwin_arm64.tar.gz"
      sha256 "$(checksum darwin_arm64)"
    end
    on_intel do
      url "$base/agent-vm_${version}_darwin_amd64.tar.gz"
      sha256 "$(checksum darwin_amd64)"
    end
  end
  on_linux do
    on_arm do
      url "$base/agent-vm_${version}_linux_arm64.tar.gz"
      sha256 "$(checksum linux_arm64)"
    end
    on_intel do
      url "$base/agent-vm_${version}_linux_amd64.tar.gz"
      sha256 "$(checksum linux_amd64)"
    end
  end

  depends_on "lima"

  def install
    bin.install "agent-vm"
    doc.install "README.md", "VALIDATION.md", "licenses"
  end

  test do
    assert_match "agent-vm $version", shell_output("#{bin}/agent-vm --version")
    assert_equal true, JSON.parse(shell_output("#{bin}/agent-vm render"))["plain"]
  end
end
FORMULA
fi
echo "Release files: $dist"

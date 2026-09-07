#!/bin/bash
# Generate a tap formula from GoReleaser's completed archives.
set -euo pipefail
cd "$(dirname "$0")/.."
version=${1:?Expected vX.Y.Z}
repository=${2:?Expected OWNER/REPO}
dist=.build/releases
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$ ]] || exit 1
[[ "$repository" =~ ^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$ ]] || exit 1
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
    assert_match "devwright $version", shell_output("#{bin}/devwright version")
    assert_equal true, JSON.parse(shell_output("#{bin}/devwright render"))["plain"]
  end
end
FORMULA

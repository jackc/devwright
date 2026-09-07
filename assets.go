package devwright

import "embed"

// Keep the source recipe editable in its existing locations. A released binary
// carries exactly the recipe it was built with, independent of the current directory.
// Build with mise run build, or run mise run guest before invoking go build directly.
//
//go:embed lima/devwright.json lima/bootstrap.sh lima/provision.sh lima/dotfiles.sh lima/credentials.sh incus/bootstrap.sh config/codex/*.toml config/claude/*.json guestbin/verify-linux-arm64.gz guestbin/verify-linux-amd64.gz guestbin/verify-darwin-arm64.gz guestbin/verify-darwin-amd64.gz
var recipe embed.FS

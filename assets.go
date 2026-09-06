package agentvm

import "embed"

// Keep the source recipe editable in its existing locations. A released binary
// carries exactly the recipe it was built with, independent of the current directory.
// Build with mise run build, or run mise run guest before invoking go build directly.
//
//go:embed lima/agent.json lima/bootstrap.sh lima/provision.sh lima/dotfiles.sh incus/bootstrap.sh config/codex/*.toml guestbin/verify-linux-arm64.gz guestbin/verify-linux-amd64.gz
var recipe embed.FS

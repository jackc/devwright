package agentvm

import "embed"

// Keep the source recipe editable in its existing locations. A released binary
// carries exactly the recipe it was built with, independent of the current directory.
//
//go:embed lima/agent.json lima/bootstrap.sh lima/provision.sh lima/dotfiles.sh incus/bootstrap.sh config/codex/*.toml scripts/verify_guest.rb scripts/check_codex.rb
var recipe embed.FS

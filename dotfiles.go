package devwright

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
)

// Only this host-side Git runner inherits authentication. A bundle contains
// objects and refs, never the clone's config, hooks, or credential helpers.
func prepareDotfiles(run runner, repository string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "devwright-dotfiles-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	clone := filepath.Join(dir, "repository.git")
	bundle := filepath.Join(dir, "dotfiles.bundle")
	for _, args := range [][]string{
		{"git", "clone", "--bare", "--single-branch", "--", repository, clone},
		{"git", "-C", clone, "bundle", "create", bundle, "--all"},
	} {
		if _, err := run(args, nil, false); err != nil {
			return nil, fmt.Errorf("fetch dotfiles on host: %w", err)
		}
	}
	return os.ReadFile(bundle)
}

func dotfilesBundleScript(bundle []byte, provision string) string {
	// Keep cleanup in the parent shell: provisioning has its own temporary-file
	// traps. The bundle remains available through installation by both accounts.
	return "set -eu\n" +
		"dotfiles_transport=$(mktemp -d /tmp/devwright-dotfiles.XXXXXXXXXX)\n" +
		"trap 'rm -rf -- \"$dotfiles_transport\"' EXIT\n" +
		"export dotfiles_bundle=\"$dotfiles_transport/dotfiles.bundle\"\n" +
		"printf '%s' '" + base64.StdEncoding.EncodeToString(bundle) + "' | base64 -d > \"$dotfiles_bundle\"\n" +
		"(\n" + provision + "\n)\n"
}

package devwright

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

const nativeCodexConfig = `# Editable defaults; the OS account is the enforced boundary.
default_permissions = ":workspace"
approval_policy = "on-request"
[shell_environment_policy]
inherit = "all"
ignore_default_excludes = true
[features]
apps = false
plugins = false
browser_use = false
in_app_browser = false
computer_use = false
[apps._default]
enabled = false
`

// Editable Claude Code defaults for the account; managed-only keys are omitted
// because a user file cannot set them.
const nativeClaudeSettings = `{
  "sandbox": {
    "enabled": true,
    "failIfUnavailable": true,
    "allowUnsandboxedCommands": false,
    "autoAllowBashIfSandboxed": true,
    "filesystem": {
      "denyRead": ["~/.ssh", "~/.pgpass", "~/.aws", "~/.gnupg", "~/.kube", "~/.codex/auth.json", "~/.claude/.credentials.json"]
    },
    "network": {"allowedDomains": ["*"]}
  },
  "permissions": {
    "deny": ["Read(~/.ssh/**)", "Read(~/.pgpass)", "Read(~/.aws/**)", "Read(~/.gnupg/**)", "Read(~/.kube/**)", "Read(~/.codex/auth.json)", "Read(~/.claude/.credentials.json)"]
  },
  "disableClaudeAiConnectors": true,
  "allowedMcpServers": [],
  "deniedMcpServers": [{"serverName": "claude-in-chrome"}, {"serverName": "computer-use"}],
  "enableAllProjectMcpServers": false
}
`

func (a *userAdmin) asUser(s *userState, input io.Reader, args ...string) (string, error) {
	base := []string{"/usr/bin/sudo", "-u", s.Account, "-H", "--", "/usr/bin/env", "-i", "HOME=" + s.Home, "USER=" + s.Account, "LOGNAME=" + s.Account, "SHELL=/bin/bash", "PATH=" + s.Home + "/.local/bin:/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
	return a.commandIn(s.Home, input, append(base, args...)...)
}
func (a *userAdmin) setupHome(s *userState, r userRequest) error {
	config := r.Config
	if len(config) == 0 {
		config = []byte(nativeCodexConfig)
	}
	claudeConfig := r.ClaudeConfig
	if len(claudeConfig) == 0 {
		claudeConfig = []byte(nativeClaudeSettings)
	}
	script := `set -euo pipefail
umask 077
cd "$HOME"
mkdir -p projects .local/bin .codex .claude .config
installer=$(mktemp "$HOME/.local/codex-install.XXXXXX")
trap 'rm -f "$installer"' EXIT
curl -fsSL https://chatgpt.com/codex/install.sh -o "$installer"
CODEX_INSTALL_DIR="$HOME/.local/bin" CODEX_NON_INTERACTIVE=1 sh "$installer" --release latest
rm -f "$installer"
trap - EXIT
"$HOME/.local/bin/codex" --version
# The official installer targets this account's home; the account owns the install.
claude_installer=$(mktemp "$HOME/.local/claude-install.XXXXXX")
trap 'rm -f "$claude_installer"' EXIT
curl -fsSL https://claude.ai/install.sh -o "$claude_installer"
bash "$claude_installer" latest
rm -f "$claude_installer"
trap - EXIT
"$HOME/.local/bin/claude" --version
if [ -n "$repository" ]; then
 dotfiles="$HOME/.local/share/devwright/dotfiles"
 mkdir -p "$(dirname "$dotfiles")"
 if [ ! -e "$dotfiles" ]; then git clone -- "$repository" "$dotfiles"; else
  test "$(git -C "$dotfiles" remote get-url origin)" = "$repository"
  git -C "$dotfiles" pull --ff-only
 fi
 cd "$dotfiles"
 test -f "$dotfiles_installer" && test -x "$dotfiles_installer"
 "./$dotfiles_installer"
 cd "$HOME"
fi
if command -v gh >/dev/null 2>&1; then
 gh_path=$(command -v gh)
 git config --global --replace-all credential.https://github.com.helper ''
 git config --global --add credential.https://github.com.helper "!$gh_path auth git-credential"
 gh config set git_protocol https --host github.com
fi
`
	script = "repository=" + shellQuote(r.Repository) + "\ndotfiles_installer=" + shellQuote(r.Installer) + "\n" + script
	if _, err := a.asUser(s, bytes.NewReader(config), a.verifierPath(), "user-config", s.Account, s.Home, fmt.Sprint(r.ReplaceConfig)); err != nil {
		return err
	}
	if _, err := a.asUser(s, bytes.NewReader(claudeConfig), a.verifierPath(), "user-claude-config", s.Account, s.Home, fmt.Sprint(r.ReplaceClaudeConfig)); err != nil {
		return err
	}

	if _, err := a.asUser(s, strings.NewReader(script), "/bin/bash", "-s"); err != nil {
		return err
	}
	// Portable Go startup-file handling runs under the target UID, including all
	// symlink resolution and writes. No account-owned code is evaluated as root.
	_, err := a.asUser(s, bytes.NewReader(nil), a.verifierPath(), "setup-user", s.Account, s.Home)
	return err
}

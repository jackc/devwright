#!/bin/bash
# Shared by Lima and Incus; sent over admin SSH during create/configure only.
set -euo pipefail
umask 022
export DEBIAN_FRONTEND=noninteractive
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
unset SSH_AUTH_SOCK GH_TOKEN GITHUB_TOKEN OPENAI_API_KEY

test "$(id -u)" = 0
test -s /root/.ssh/authorized_keys
chmod 700 /root
policy_dir=/usr/local/share/dev-sandbox
install -d -m 755 "$policy_dir" /etc/codex
rm -f "$policy_dir/managed"

test "$(getent passwd dev | cut -d: -f6)" = /home/dev
test "$(id -u dev)" != 0
# This recipe owns the dev account's supplementary group membership.
usermod -G '' dev
passwd -l dev >/dev/null
chmod 700 /home/dev
# install -d follows an existing symlink when applying ownership and mode, so a
# link planted by dev could redirect root at a protected directory. Refuse it.
for dev_dir in /home/dev/.ssh /home/dev/.codex /home/dev/.claude /home/dev/.config /home/dev/projects; do
  if [ -L "$dev_dir" ] || { [ -e "$dev_dir" ] && [ ! -d "$dev_dir" ]; }; then
    echo "Refusing to manage $dev_dir: expected a directory, not a symlink or file" >&2
    exit 1
  fi
done
install -d -o dev -g dev -m 700 /home/dev/.ssh /home/dev/.codex /home/dev/.claude /home/dev/.config
install -d -o dev -g dev -m 755 /home/dev/projects
printf '%s\n' 'dev ALL=(ALL:ALL) !ALL' > /etc/sudoers.d/99-dev-sandbox-dev
chmod 440 /etc/sudoers.d/99-dev-sandbox-dev
visudo -cf /etc/sudoers >/dev/null

cat > /etc/ssh/sshd_config.d/00-dev-sandbox.conf <<'SSH'
PermitRootLogin prohibit-password
PasswordAuthentication no
KbdInteractiveAuthentication no
AllowAgentForwarding no
X11Forwarding no
SSH
# Replace the creation-only SSH drop-in with the complete managed settings.
rm -f /etc/ssh/sshd_config.d/00-dev-sandbox-root.conf
/usr/sbin/sshd -t
systemctl reload ssh

apt-get update -qq
apt-get install -y --no-install-recommends \
  ca-certificates curl git gh jq ripgrep build-essential gzip \
  zsh unzip bubblewrap apparmor gnupg socat

# BEGIN AGENT FUNCTIONS
# install_managed_policy MODE STEM EXT TARGET DEFAULT_B64 CUSTOM_B64
# Keep the operator's selection separate from the effective policy so configure
# restores it after manual edits. Omission updates embedded defaults only.
install_managed_policy() {
  local mode=$1 stem=$2 ext=$3 target=$4 default_b64=$5 custom_b64=$6
  local default_file="$policy_dir/default-$stem.$ext" custom_file="$policy_dir/custom-$stem.$ext" tmp selected
  printf '%s' "$default_b64" | base64 -d > "$default_file"
  case "$mode" in
    custom)
      tmp=$(mktemp "$policy_dir/$stem.XXXXXX")
      printf '%s' "$custom_b64" | base64 -d > "$tmp"
      chmod 644 "$tmp"
      mv -fT "$tmp" "$custom_file"
      ;;
    default) rm -f "$custom_file" ;;
    preserve) ;;
    *) echo "Invalid policy mode: $mode" >&2; exit 1 ;;
  esac
  selected=$default_file
  if [ -f "$custom_file" ]; then
    selected=$custom_file
  fi
  tmp=$(mktemp "$(dirname "$target")/$(basename "$target").XXXXXX")
  cat "$selected" > "$tmp"
  chmod 644 "$tmp"
  mv -fT "$tmp" "$target"
  sha256sum "$target" > "$policy_dir/$stem.sha256"
}
# install_user_config REPLACE B64 TARGET: seed dev's file once; replace on request.
install_user_config() {
  local replace=$1 b64=$2 target=$3 tmp
  if [ "$replace" = true ] || { [ ! -e "$target" ] && [ ! -L "$target" ]; }; then
    # Stage outside dev's directories; rename rather than follow a config symlink.
    tmp=$(mktemp "$policy_dir/$(basename "$target").XXXXXX")
    printf '%s' "$b64" | base64 -d > "$tmp"
    chown dev:dev "$tmp"
    chmod 600 "$tmp"
    mv -fT "$tmp" "$target"
  fi
}
# END AGENT FUNCTIONS

# BEGIN CODEX FILES
install_managed_policy "$codex_policy_mode" requirements toml /etc/codex/requirements.toml \
  '__DEFAULT_REQUIREMENTS_B64__' '__REQUIREMENTS_B64__'
install_user_config "$replace_codex_config" '__CONFIG_B64__' /home/dev/.codex/config.toml
# END CODEX FILES

# BEGIN CLAUDE FILES
install -d -m 755 /etc/claude-code
install_managed_policy "$claude_policy_mode" managed-settings json /etc/claude-code/managed-settings.json \
  '__DEFAULT_MANAGED_SETTINGS_B64__' '__MANAGED_SETTINGS_B64__'
# Drop-ins replace single values and a managed MCP file adds servers; the
# recipe installs neither, and configure removes any left behind.
rm -rf /etc/claude-code/managed-settings.d /etc/claude-code/managed-mcp.json
install_user_config "$replace_claude_config" '__CLAUDE_CONFIG_B64__' /home/dev/.claude/settings.json
# END CLAUDE FILES

# Keep the standalone package root-owned and accessible to dev, outside /root.
install -d -m 755 /usr/local/share/codex
codex_installer=$(mktemp)
trap 'rm -f "$codex_installer"' EXIT
curl -fsSL https://chatgpt.com/codex/install.sh -o "$codex_installer"
CODEX_HOME=/usr/local/share/codex CODEX_INSTALL_DIR=/usr/local/bin \
  CODEX_NON_INTERACTIVE=1 sh "$codex_installer" --release latest
rm -f "$codex_installer"
trap - EXIT
codex_version=$(/usr/local/bin/codex --version)
printf '%s\n' "$codex_version"
sudo -u dev -H /usr/local/bin/codex --version

# Ubuntu's stock bubblewrap profile confines the commands bubblewrap runs to a
# child profile that denies capabilities, which blocks the nested user namespace
# Claude Code's seccomp filter creates. Disable it through the standard disable
# directory and install Anthropic's documented profile. The profile is
# unconfined and inherited on exec, so bubblewrap and the commands it runs may
# create user namespaces; those commands rely on bubblewrap's namespaces and
# each agent's own sandbox instead of the stock child profile. The global
# unprivileged user namespace restriction stays enabled for everything else.
# Without AppArmor there is no restriction to lift; verification reports the
# actual sandbox behavior either way.
rm -f "$policy_dir/bwrap-profile.sha256"
if [ -d /sys/kernel/security/apparmor ]; then
  mkdir -p /etc/apparmor.d/disable
  if [ -f /etc/apparmor.d/bwrap-userns-restrict ]; then
    ln -sf /etc/apparmor.d/bwrap-userns-restrict /etc/apparmor.d/disable/bwrap-userns-restrict
    apparmor_parser -R /etc/apparmor.d/bwrap-userns-restrict 2>/dev/null || true
  fi
  cat > /etc/apparmor.d/bwrap <<'PROFILE'
abi <abi/5.0>,
include <tunables/global>

# Installed by dev-sandbox from Anthropic's Claude Code sandboxing guidance.
profile bwrap /usr/bin/bwrap flags=(unconfined) {
  userns,

  include if exists <local/bwrap>
}
PROFILE
  if apparmor_parser -r /etc/apparmor.d/bwrap; then
    sha256sum /etc/apparmor.d/bwrap > "$policy_dir/bwrap-profile.sha256"
  else
    echo 'Warning: could not load the bwrap AppArmor profile; sandbox verification will report the consequence' >&2
  fi
fi

# Anthropic's signed apt repository provides a root-owned /usr/bin/claude.
# Trust the release key only after checking its fingerprint.
install -d -m 755 /etc/apt/keyrings
claude_key=$(mktemp)
trap 'rm -f "$claude_key"' EXIT
curl -fsSL https://downloads.claude.ai/keys/claude-code.asc -o "$claude_key"
claude_fingerprint=$(gpg --batch --with-colons --show-keys "$claude_key" | awk -F: '$1 == "fpr" {print $10; exit}')
test "$claude_fingerprint" = 31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE
install -m 644 "$claude_key" /etc/apt/keyrings/claude-code.asc
rm -f "$claude_key"
trap - EXIT
# The latest channel matches the Codex install and carries the posture report
# and session flags the verifier uses; stable lagged a month behind them.
printf '%s\n' 'deb [signed-by=/etc/apt/keyrings/claude-code.asc] https://downloads.claude.ai/claude-code/apt/latest latest main' \
  > /etc/apt/sources.list.d/claude-code.list
apt-get update -qq
apt-get install -y --no-install-recommends claude-code
claude_version=$(/usr/bin/claude --version)
printf '%s\n' "$claude_version"
sudo -u dev -H /usr/bin/claude --version

git config --system --replace-all credential.https://github.com.helper ''
git config --system --add credential.https://github.com.helper '!/usr/bin/gh auth git-credential'
git config --system init.defaultBranch main
sudo -u dev -H /usr/bin/gh config set git_protocol https --host github.com

printf '%s' '__DOTFILES_B64__' | base64 -d > /usr/local/share/dev-sandbox/dotfiles.sh
chmod 755 /usr/local/share/dev-sandbox/dotfiles.sh
if [ -n "$dotfiles_repository" ]; then
  /bin/bash /usr/local/share/dev-sandbox/dotfiles.sh "$dotfiles_repository" "$dotfiles_install"
fi

# Install hooks after personal dotfiles so their early returns or replacements
# cannot bypass the loader. No dev-owned shell code is evaluated as root.
printf '%s' '__CREDENTIALS_B64__' | base64 -d > /usr/local/share/dev-sandbox/setup-credentials.sh
chmod 755 /usr/local/share/dev-sandbox/setup-credentials.sh
sudo -u dev -H env -i HOME=/home/dev USER=dev LOGNAME=dev PATH=/usr/bin:/bin \
  /bin/bash /usr/local/share/dev-sandbox/setup-credentials.sh

# Select the verifier for the guest architecture, independently of the host.
verify_tmp=$(mktemp /usr/local/share/dev-sandbox/verify.XXXXXX)
trap 'rm -f "$verify_tmp"' EXIT
case "$(uname -m)" in
  aarch64|arm64) printf '%s' '__VERIFY_ARM64_B64__' ;;
  x86_64|amd64) printf '%s' '__VERIFY_AMD64_B64__' ;;
  *) echo 'Unsupported guest architecture for verification' >&2; exit 1 ;;
esac | base64 -d | gzip -d > "$verify_tmp"
chmod 755 "$verify_tmp"
mv -f "$verify_tmp" /usr/local/share/dev-sandbox/verify
trap - EXIT
printf '%s\n' 'dev-sandbox-v1' > /usr/local/share/dev-sandbox/managed
printf '%s\n' "$codex_version" > /usr/local/share/dev-sandbox/codex-version
printf '%s\n' "$claude_version" > /usr/local/share/dev-sandbox/claude-version
echo 'Provisioned: root administers; dev develops. No credentials were copied.'

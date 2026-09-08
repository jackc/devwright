#!/bin/bash
# This project owns this recipe. Significant changes require a new VM.
set -euo pipefail
marker=/usr/local/share/devwright/system-setup-complete
[ ! -f "$marker" ] || exit 0
export DEBIAN_FRONTEND=noninteractive
export HOME=/root USER=root LOGNAME=root
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
# Creation only: establish root SSH before revoking dev's initial sudo access.

test "$(id -u)" = 0
test "$(getent passwd dev | cut -d: -f6)" = /home/dev
install -d -o root -g root -m 700 /root/.ssh
# Copy only the public login keys Lima installed; private keys stay on the host.
install -o root -g root -m 600 /home/dev/.ssh/authorized_keys /root/.ssh/authorized_keys
cat > /etc/ssh/sshd_config.d/00-devwright-root.conf <<'SSH'
PermitRootLogin prohibit-password
PasswordAuthentication no
KbdInteractiveAuthentication no
AllowAgentForwarding no
X11Forwarding no
SSH
/usr/sbin/sshd -t
systemctl reload ssh
install -d -m 755 /usr/local/share/devwright
policy_dir=/usr/local/share/devwright
chmod 700 /root /home/dev
usermod -G '' dev
passwd -l dev >/dev/null
printf '%s\n' 'dev ALL=(ALL:ALL) !ALL' > /etc/sudoers.d/99-devwright-dev
chmod 440 /etc/sudoers.d/99-devwright-dev
visudo -cf /etc/sudoers >/dev/null
apt-get update -qq
apt-get install -y --no-install-recommends ca-certificates curl git gh jq ripgrep build-essential gzip zsh unzip bubblewrap apparmor gnupg socat
# Add project system packages and services here.
install -d -o dev -g dev -m 700 /home/dev/.codex /home/dev/.claude
if [ ! -e /home/dev/.codex/config.toml ]; then
  install -o dev -g dev -m 600 "$policy_dir/codex-config.toml" /home/dev/.codex/config.toml
fi
if [ ! -e /home/dev/.claude/settings.json ]; then
  install -o dev -g dev -m 600 "$policy_dir/claude-settings.json" /home/dev/.claude/settings.json
fi
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

# Installed by devwright from Anthropic's Claude Code sandboxing guidance.
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

touch "$marker"
echo 'System setup complete.'

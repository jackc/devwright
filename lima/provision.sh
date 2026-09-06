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
install -d -m 755 /usr/local/share/dev-sandbox /etc/codex
rm -f /usr/local/share/dev-sandbox/managed

test "$(getent passwd dev | cut -d: -f6)" = /home/dev
test "$(id -u dev)" != 0
# This recipe owns the dev account's supplementary group membership.
usermod -G '' dev
passwd -l dev >/dev/null
chmod 700 /home/dev
install -d -o dev -g dev -m 700 /home/dev/.ssh /home/dev/.codex /home/dev/.config
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
  zsh unzip bubblewrap apparmor

# BEGIN CODEX FILES
# Keep the operator's selection separate from the effective policy so configure
# restores it after manual edits. Omission updates embedded defaults only.
policy_dir=/usr/local/share/dev-sandbox
printf '%s' '__DEFAULT_REQUIREMENTS_B64__' | base64 -d > "$policy_dir/default-requirements.toml"
case "$codex_policy_mode" in
  custom)
    policy_tmp=$(mktemp "$policy_dir/requirements.XXXXXX")
    printf '%s' '__REQUIREMENTS_B64__' | base64 -d > "$policy_tmp"
    chmod 644 "$policy_tmp"
    mv -fT "$policy_tmp" "$policy_dir/custom-requirements.toml"
    ;;
  default) rm -f "$policy_dir/custom-requirements.toml" ;;
  preserve) ;;
  *) echo 'Invalid Codex policy mode' >&2; exit 1 ;;
esac
selected_policy="$policy_dir/default-requirements.toml"
if [ -f "$policy_dir/custom-requirements.toml" ]; then
  selected_policy="$policy_dir/custom-requirements.toml"
fi
policy_tmp=$(mktemp /etc/codex/requirements.XXXXXX)
cat "$selected_policy" > "$policy_tmp"
chmod 644 "$policy_tmp"
mv -fT "$policy_tmp" /etc/codex/requirements.toml
sha256sum /etc/codex/requirements.toml > "$policy_dir/requirements.sha256"
if [ "$replace_codex_config" = true ] || { [ ! -e /home/dev/.codex/config.toml ] && [ ! -L /home/dev/.codex/config.toml ]; }; then
  # Stage outside dev's directories; rename rather than follow a config symlink.
  config_tmp=$(mktemp "$policy_dir/config.XXXXXX")
  printf '%s' '__CONFIG_B64__' | base64 -d > "$config_tmp"
  chown dev:dev "$config_tmp"
  chmod 600 "$config_tmp"
  mv -fT "$config_tmp" /home/dev/.codex/config.toml
fi
# END CODEX FILES

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
echo 'Provisioned: root administers; dev develops. No credentials were copied.'

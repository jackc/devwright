#!/bin/bash
# Rendered by scripts/vm.py; also used for explicit updates over admin SSH.
# Lima recipe marker: {{.Param.agentSandboxConfig}}
set -euo pipefail
umask 022
export DEBIAN_FRONTEND=noninteractive
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
unset SSH_AUTH_SOCK GH_TOKEN GITHUB_TOKEN OPENAI_API_KEY

test "$(id -u)" = 0
test "$(getent passwd jack | cut -d: -f6)" = /home/jack
chmod 700 /home/jack
install -d -m 755 /usr/local/share/agent-vm /etc/codex
rm -f /usr/local/share/agent-vm/managed

apt-get update -qq
apt-get install -y --no-install-recommends \
  ca-certificates curl git gh jq ripgrep build-essential python3 python3-venv \
  nodejs npm zsh unzip bubblewrap apparmor

printf '%s' '__APPARMOR_B64__' | base64 -d > /etc/apparmor.d/agent-vm-bwrap
chmod 644 /etc/apparmor.d/agent-vm-bwrap
apparmor_parser -r /etc/apparmor.d/agent-vm-bwrap

if ! id dev >/dev/null 2>&1; then
  useradd --create-home --user-group --shell /bin/bash dev
fi
test "$(getent passwd dev | cut -d: -f6)" = /home/dev
test "$(id -u dev)" != 0
# This recipe owns the dev account's supplementary group membership.
usermod -G '' dev
passwd -l dev >/dev/null
chmod 700 /home/dev
install -d -o dev -g dev -m 700 /home/dev/.ssh /home/dev/.codex /home/dev/.config /home/dev/.config/agent-vm
install -d -o dev -g dev -m 755 /home/dev/projects
# Public login key only. The private Lima key stays on the host.
install -o dev -g dev -m 600 /home/jack/.ssh/authorized_keys /home/dev/.ssh/authorized_keys
printf '%s\n' 'dev ALL=(ALL:ALL) !ALL' > /etc/sudoers.d/99-agent-vm-dev
chmod 440 /etc/sudoers.d/99-agent-vm-dev
visudo -cf /etc/sudoers >/dev/null

cat > /etc/ssh/sshd_config.d/00-agent-vm.conf <<'SSH'
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
AllowAgentForwarding no
X11Forwarding no
SSH
/usr/sbin/sshd -t
systemctl reload ssh

printf '%s' '__REQUIREMENTS_B64__' | base64 -d > /etc/codex/requirements.toml
chmod 644 /etc/codex/requirements.toml
if [ ! -e /home/dev/.codex/config.toml ]; then
  printf '%s' '__CONFIG_B64__' | base64 -d > /home/dev/.codex/config.toml
  chown dev:dev /home/dev/.codex/config.toml
  chmod 600 /home/dev/.codex/config.toml
fi

codex_version='__CODEX_VERSION__'
if [ "$(/usr/local/bin/codex --version 2>/dev/null || true)" != "codex-cli $codex_version" ]; then
  npm install --global --prefix /usr/local --ignore-scripts "@openai/codex@$codex_version"
fi
/usr/local/bin/codex --version

# gh gets this VM's token for login and non-login shells, including Git helpers.
# No token is embedded in this script, Lima config, or logs.
cat > /usr/local/bin/gh <<'GH'
#!/bin/sh
if [ -z "${GH_TOKEN:-}" ] && [ -f "$HOME/.config/agent-vm/github-token" ]; then
  GH_TOKEN=$(cat "$HOME/.config/agent-vm/github-token") || exit
  export GH_TOKEN
fi
exec /usr/bin/gh "$@"
GH
chmod 755 /usr/local/bin/gh
git config --system --replace-all credential.https://github.com.helper ''
git config --system --add credential.https://github.com.helper '!/usr/local/bin/gh auth git-credential'
git config --system init.defaultBranch main
sudo -u dev -H /usr/local/bin/gh config set git_protocol https --host github.com

printf '%s' '__VERIFY_B64__' | base64 -d > /usr/local/share/agent-vm/verify.py
chmod 755 /usr/local/share/agent-vm/verify.py
printf '%s' '__CHECK_CODEX_B64__' | base64 -d > /usr/local/share/agent-vm/check_codex.py
chmod 755 /usr/local/share/agent-vm/check_codex.py
printf '%s\n' 'agent-sandbox-config-v1' > /usr/local/share/agent-vm/managed
printf '%s\n' "$codex_version" > /usr/local/share/agent-vm/codex-version
echo 'Provisioned: jack administers; dev develops. No credentials were copied.'

#!/bin/bash
# Creation only, run as root through Incus. Private keys stay on the host.
set -euo pipefail
export PATH=/usr/sbin:/usr/bin:/sbin:/bin
export DEBIAN_FRONTEND=noninteractive
test "$(id -u)" = 0
. /etc/os-release
test "$ID" = ubuntu && test "$VERSION_ID" = 26.04

# The default (non-cloud) Incus image needs no cloud-init boot provisioning.
apt-get update -qq
apt-get install -y --no-install-recommends openssh-server sudo netcat-openbsd ca-certificates
if ! id dev >/dev/null 2>&1; then
  useradd --create-home --user-group --shell /bin/bash dev
fi
test "$(getent passwd dev | cut -d: -f6)" = /home/dev
test "$(id -u dev)" != 0
usermod -G '' dev
passwd -l dev >/dev/null
install -d -o root -g root -m 700 /root/.ssh
install -d -o dev -g dev -m 700 /home/dev/.ssh
printf '%s' "$public_key_b64" | base64 -d > /root/.ssh/authorized_keys
chmod 600 /root/.ssh/authorized_keys
install -o dev -g dev -m 600 /root/.ssh/authorized_keys /home/dev/.ssh/authorized_keys
cat > /etc/ssh/sshd_config.d/00-dev-sandbox-root.conf <<'SSH'
PermitRootLogin prohibit-password
PasswordAuthentication no
KbdInteractiveAuthentication no
AllowAgentForwarding no
SSH
ssh-keygen -A
/usr/sbin/sshd -t
systemctl enable --now ssh
systemctl restart ssh

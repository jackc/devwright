#!/bin/bash
# Creation only: establish root SSH before revoking dev's initial sudo access.
set -euo pipefail
export PATH=/usr/sbin:/usr/bin:/sbin:/bin

test "$(id -u)" = 0
test "$(getent passwd dev | cut -d: -f6)" = /home/dev
install -d -o root -g root -m 700 /root/.ssh
# Copy only the public login keys Lima installed; private keys stay on the host.
install -o root -g root -m 600 /home/dev/.ssh/authorized_keys /root/.ssh/authorized_keys
cat > /etc/ssh/sshd_config.d/00-dev-sandbox-root.conf <<'SSH'
PermitRootLogin prohibit-password
PasswordAuthentication no
KbdInteractiveAuthentication no
SSH
/usr/sbin/sshd -t
systemctl reload ssh

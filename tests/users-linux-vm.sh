#!/bin/bash
# Run from a checkout with mise and Lima installed. A stock disposable VM uses
# its own Lima state and no host mounts. Existing Lima instances are untouched.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
command -v limactl >/dev/null
command -v mise >/dev/null
mkdir -p "$root/.build"
work=$(mktemp -d "$root/.build/users-vm.XXXXXX")
export LIMA_HOME="$work/lima"
keep=${1:-}
finish() {
 status=$?
 trap - EXIT
 if [ "$keep" = --keep ]; then echo "Retained VM: LIMA_HOME=$LIMA_HOME limactl shell users-test"; else limactl delete --force users-test || status=1; fi
 echo "VM test artifacts: $work"
 exit "$status"
}
trap finish EXIT
mise run guest
case "$(uname -m)" in arm64|aarch64) arch=arm64 ;; x86_64) arch=amd64 ;; *) exit 1 ;; esac
mise exec -- env CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -o "$work/devwright" ./cmd/devwright
limactl start --name=users-test --mount-none --containerd=none --cpus=2 --memory=4 --disk=20 --tty=false template:ubuntu-26.04
limactl shell users-test sudo /bin/bash -c 'apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates curl git openssh-server sudo bubblewrap apparmor socat zsh gh && systemctl enable --now ssh'
# The native backend changes no host security policy: install the bwrap
# AppArmor profile the README lists as a Linux prerequisite for Claude Code.
limactl shell users-test sudo /bin/bash -c 'set -eu; mkdir -p /etc/apparmor.d/disable; if [ -f /etc/apparmor.d/bwrap-userns-restrict ]; then ln -sf /etc/apparmor.d/bwrap-userns-restrict /etc/apparmor.d/disable/bwrap-userns-restrict; apparmor_parser -R /etc/apparmor.d/bwrap-userns-restrict 2>/dev/null || true; fi; printf "%s\n" "abi <abi/5.0>," "include <tunables/global>" "" "profile bwrap /usr/bin/bwrap flags=(unconfined) {" "  userns," "" "  include if exists <local/bwrap>" "}" > /etc/apparmor.d/bwrap; apparmor_parser -r /etc/apparmor.d/bwrap'
# Lima's boot setup replaces the image's default host keys. Give this disposable
# host an explicit persistent key before snapshotting/testing the native backend.
limactl shell users-test sudo /bin/bash -c 'set -eu; install -d -m 700 /var/lib/devwright-test; ssh-keygen -q -t ed25519 -N "" -f /var/lib/devwright-test/ssh_host_ed25519_key; printf "HostKey /var/lib/devwright-test/ssh_host_ed25519_key\n" > /etc/ssh/sshd_config.d/01-native-test-hostkey.conf; /usr/sbin/sshd -t && systemctl reload ssh'
limactl copy "$work/devwright" users-test:/var/tmp/devwright-users-test
limactl copy tests/users-macos.sh users-test:/var/tmp/devwright-users-acceptance.sh
limactl shell users-test chmod 755 /var/tmp/devwright-users-test
limactl shell users-test env TMPDIR=/var/tmp bash /var/tmp/devwright-users-acceptance.sh /var/tmp/devwright-users-test --keep 2>&1 | tee "$work/acceptance.log"
fixture=$(sed -n 's/^Retained fixtures: //p' "$work/acceptance.log" | tail -1)
case "$fixture" in /var/tmp/devwright-users.*) ;; *) echo 'Missing fixture identity for reboot test' >&2; exit 1 ;; esac
limactl stop users-test
limactl start --tty=false users-test
limactl shell users-test bash -c 'fixture=$1; name="$(cat "$fixture/run-id")a"; HOME="$fixture/client" /var/tmp/devwright-users-test verify "$name" --backend user' -- "$fixture" 2>&1 | tee "$work/reboot.log"
if [ "$keep" != --keep ]; then limactl shell users-test bash /var/tmp/devwright-users-acceptance.sh --cleanup "$fixture"; fi

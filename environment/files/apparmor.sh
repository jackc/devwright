#!/bin/bash
set -euo pipefail
umask 022
policy_dir=/usr/local/share/devwright
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


#!/bin/bash
# Optional: run each repository installer as the account being configured.
set -euo pipefail
repository=$1
installer=$2

for account in root dev; do
  account_home=$(getent passwd "$account" | cut -d: -f6)
  account_shell=$(getent passwd "$account" | cut -d: -f7)
  sudo -u "$account" -H env -i HOME="$account_home" USER="$account" \
    LOGNAME="$account" SHELL="$account_shell" PATH=/usr/local/bin:/usr/bin:/bin \
    /bin/bash -s -- "$repository" "$installer" <<'USER_SETUP'
set -euo pipefail
cd "$HOME"
repository=$1
installer=$2
# Each account owns its checkout; root never executes dev's copy.
dotfiles="$HOME/.local/share/agent-vm/dotfiles"
if [ ! -e "$dotfiles" ]; then
  mkdir -p "$(dirname "$dotfiles")"
  git clone -- "$repository" "$dotfiles"
else
  test "$(git -C "$dotfiles" remote get-url origin)" = "$repository"
  git -C "$dotfiles" pull --ff-only
fi
cd "$dotfiles"
test -f "$installer"
test -x "$installer"
"./$installer"
# Preserve the VM's GitHub HTTPS token helper after personal Git changes.
git config --global --replace-all credential.https://github.com.helper ''
git config --global --add credential.https://github.com.helper '!/usr/local/bin/gh auth git-credential'
USER_SETUP
done

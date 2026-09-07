#!/bin/bash
# Optional: run each repository installer as the account being configured.
set -euo pipefail
repository=$1
installer=$2
bundle=$3
# The transport directory is root-owned; only root and dev may read the bundle.
chgrp dev "$(dirname "$bundle")" "$bundle"
chmod 750 "$(dirname "$bundle")"
chmod 640 "$bundle"

for account in root dev; do
  account_home=$(getent passwd "$account" | cut -d: -f6)
  account_shell=$(getent passwd "$account" | cut -d: -f7)
  sudo -u "$account" -H env -i HOME="$account_home" USER="$account" \
    LOGNAME="$account" SHELL="$account_shell" PATH=/usr/local/bin:/usr/bin:/bin \
    /bin/bash -s -- "$repository" "$installer" "$bundle" <<'USER_SETUP'
set -euo pipefail
cd "$HOME"
repository=$1
installer=$2
bundle=$3
# Each account owns its checkout; root never executes dev's copy.
dotfiles="$HOME/.local/share/devwright/dotfiles"
if [ ! -e "$dotfiles" ]; then
  mkdir -p "$(dirname "$dotfiles")"
  git clone -- "$bundle" "$dotfiles"
  git -C "$dotfiles" remote set-url origin "$repository"
else
  test "$(git -C "$dotfiles" remote get-url origin)" = "$repository"
  git -C "$dotfiles" fetch -- "$bundle" HEAD
  git -C "$dotfiles" merge --ff-only FETCH_HEAD
fi
cd "$dotfiles"
test -f "$installer"
test -x "$installer"
"./$installer"
# Preserve GitHub HTTPS authentication through the packaged gh after Git changes.
git config --global --replace-all credential.https://github.com.helper ''
git config --global --add credential.https://github.com.helper '!/usr/bin/gh auth git-credential'
USER_SETUP
done

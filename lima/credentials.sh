#!/bin/bash
# Install dev's credential convention after optional personal dotfiles. Run as
# dev with a clean environment, never source development files as root.
set -euo pipefail
umask 077
test "$(id -un)" = dev
test "$HOME" = /home/dev

directory="$HOME/.config/dev-sandbox"
mkdir -p "$directory"
chmod 700 "$directory"
credentials="$directory/credentials.sh"
if [ ! -e "$credentials" ] && [ ! -L "$credentials" ]; then
  (set -o noclobber; cat > "$credentials" <<'CREDENTIALS'
# Private environment for dev's shells and their child processes.
# Use shell-quoted export assignments, for example:
# export GH_TOKEN='replace-me'
# export OTHER_API_KEY='replace-me'
CREDENTIALS
  )
fi
test -f "$credentials" && test -O "$credentials"
chmod 600 "$credentials"

hook=$(cat <<'HOOK'
# BEGIN DEV-SANDBOX CREDENTIALS
if [ -r "$HOME/.config/dev-sandbox/credentials.sh" ]; then
  . "$HOME/.config/dev-sandbox/credentials.sh"
fi
# END DEV-SANDBOX CREDENTIALS
HOOK
)
temporary=
trap 'if [ -n "$temporary" ]; then rm -f -- "$temporary"; fi' EXIT
for name in .profile .bashrc .bash_profile .bash_login .zshenv; do
  file="$HOME/$name"
  # Creating either of these would shadow an existing Bash login file.
  if [[ "$name" = .bash_profile || "$name" = .bash_login ]] && [ ! -e "$file" ] && [ ! -L "$file" ]; then
    continue
  fi
  # Follow valid startup symlinks, updating their targets atomically as dev.
  # A broken link is an operator error; don't replace the link or lose content.
  if [ -L "$file" ] && [ ! -e "$file" ]; then
    echo "Cannot install credential hook in dangling startup symlink: $name" >&2
    exit 1
  fi
  target=$(readlink -f -- "$file")
  temporary=$(mktemp "$(dirname "$target")/.dev-sandbox-startup.XXXXXX")
  source_file=/dev/null
  if [ -e "$target" ]; then
    test -f "$target"
    chmod --reference="$target" "$temporary"
    source_file=$target
  fi
  # Move our block to the beginning, ahead of noninteractive early returns.
  # Keep a shebang first and preserve all other content. Reject broken markers
  # before replacing the file, rather than accidentally discarding its tail.
  if ! awk -v hook="$hook" '
    /^# BEGIN DEV-SANDBOX CREDENTIALS$/ { if (inside) exit 1; inside=1; next }
    /^# END DEV-SANDBOX CREDENTIALS$/ { if (!inside) exit 1; inside=0; next }
    inside { next }
    !printed && /^#!/ && NR == 1 { print; next }
    !printed { print hook; printed=1 }
    { print }
    END { if (inside) exit 1; if (!printed) print hook }
  ' "$source_file" > "$temporary"; then
    echo "Cannot install credential hook in malformed startup block: $name" >&2
    exit 1
  fi
  mv -fT -- "$temporary" "$target"
  temporary=
done

#!/bin/bash
# Run as dev after the shared credential loader; no root evaluation of user code.
set -euo pipefail
umask 077
test "$(id -un)" = dev
test "$HOME" = /home/dev
directory="$HOME/.config/devwright"
mkdir -p "$directory/credentials.d"
chmod 700 "$directory" "$directory/credentials.d"
tmp=$(mktemp "$directory/environment.XXXXXXXX")
trap 'rm -f -- "$tmp"' EXIT
printf '%s' '__ENVIRONMENT_B64__' | base64 -d > "$tmp"
mv -fT "$tmp" "$directory/environment.sh"
# Keep manual credential assignments and startup hooks from the shared recipe.
if ! grep -Fqx '# BEGIN LIMA ENVIRONMENT' "$directory/credentials.sh"; then
  cat >> "$directory/credentials.sh" <<'HOOK'
# BEGIN LIMA ENVIRONMENT
. "$HOME/.config/devwright/environment.sh"
for lima_credential in "$HOME/.config/devwright/credentials.d/"*.sh; do
  [ ! -f "$lima_credential" ] || . "$lima_credential"
done
unset lima_credential
# END LIMA ENVIRONMENT
HOOK
fi

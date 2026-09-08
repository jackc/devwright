#!/bin/bash
# lima-environment-v1: saved creation recipe, never updated in place.
set -euo pipefail
umask 077
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
state=/usr/local/share/devwright
mkdir -p "$state"
chmod 755 "$state"
# Lima invokes system provisioners on every boot. A completed recipe does no work.
if [ -f "$state/lima-setup-complete" ]; then
  exit 0
fi
staging=$(mktemp -d "$state/lima-setup.XXXXXXXX")
trap 'rm -rf -- "$staging"' EXIT
printf '%s' '__SETUP_B64__' | base64 -d > "$staging/setup.sh"
/bin/bash "$staging/setup.sh"
# Only success marks completion. A failed run retries this same snapshot on boot.
date -u +%FT%TZ > "$state/lima-setup-complete"
echo 'Lima development setup complete.'

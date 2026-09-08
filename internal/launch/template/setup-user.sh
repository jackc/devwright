#!/bin/bash
set -euo pipefail
marker="$HOME/.local/state/devwright/user-setup-complete"
[ ! -f "$marker" ] || exit 0
# Add user setup here. The project checkout arrives after Lima provisioning.
mkdir -p "$(dirname "$marker")" "$HOME/projects"
touch "$marker"

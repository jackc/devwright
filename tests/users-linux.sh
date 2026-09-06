#!/bin/bash
set -euo pipefail
test "$(uname -s)" = Linux
exec /bin/bash "$(dirname "$0")/users-macos.sh" "$@"

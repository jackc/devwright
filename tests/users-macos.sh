#!/bin/bash
# Standalone native-account acceptance. Run as your administrator login, not root:
#   bash users-macos.sh /absolute/path/devwright [--keep]
#   bash users-macos.sh --cleanup /path/printed/by/the/test
# Also runs on Linux; users-linux.sh is a convenience entry point.
# Uses synthetic data, a private client HOME, and only two named test accounts.
set -euo pipefail
case "$(uname -s)" in Darwin) base=/private/var/db/devwright; homes=/Users ;; Linux) base=/var/lib/devwright; homes=/home ;; *) echo 'Requires macOS or Linux' >&2; exit 1 ;; esac
if [ "$(id -u)" = 0 ]; then echo 'Run as your administrator login; this script invokes sudo when needed.' >&2; exit 1; fi
cleanup_only=false
keep=false
if [ "${1:-}" = --cleanup ]; then
  cleanup_only=true
  fixture=${2:?fixture directory required}
  test -d "$fixture" && test ! -L "$fixture" && test -O "$fixture"
  binary=$(cat "$fixture/binary")
  run_id=$(cat "$fixture/run-id")
else
  binary=${1:?Usage: bash users-macos.sh /absolute/path/devwright [--keep]}
  case "$binary" in /*) ;; *) binary="$(cd "$(dirname "$binary")" && pwd)/$(basename "$binary")" ;; esac
  test -x "$binary"
  if [ "${2:-}" = --keep ]; then keep=true; fi
  fixture=$(mktemp -d "${TMPDIR:-/tmp}/devwright-users.XXXXXX")
  run_id="nt$(date +%s)$$"
  printf '%s\n' "$binary" > "$fixture/binary"
  printf '%s\n' "$run_id" > "$fixture/run-id"
  mkdir -m 700 "$fixture/client"
fi
case "$run_id" in nt*[!0-9]*|nt) echo 'Invalid fixture identifier' >&2; exit 1 ;; nt*) ;; *) exit 1 ;; esac
names=("${run_id}a" "${run_id}b")
cli() { HOME="$fixture/client" "$binary" "$@" --backend user; }
account_exists() { id "devwright-$1" >/dev/null 2>&1; }
as_user() {
  local name=$1; shift
  (cd / && sudo -n -u "devwright-$name" -H env -i HOME="$homes/devwright-$name" USER="devwright-$name" LOGNAME="devwright-$name" PATH=/usr/bin:/bin /bin/sh -c 'cd "$HOME" || exit; exec "$@"' devwright-acceptance "$@")
}
# Only these two recorded fixtures may have their macOS user domain removed.
# Check the root-owned registry's owner and immutable account identity first,
# including when cleaning fixtures retained by an older version of this script.
macos_fixture_uid() {
  local name=$1 registry account uid gid guid owner actual_guid actual_home
  case "$name" in "${names[0]}"|"${names[1]}") ;; *) return 1 ;; esac
  account="devwright-$name"
  registry="$base/users/$name.json"
  sudo -n test -f "$registry" && sudo -n test ! -L "$registry" || return 1
  test "$(sudo -n /usr/bin/stat -f '%u %Lp' "$registry")" = '0 600' || return 1
  uid=$(sudo -n /usr/bin/plutil -extract uid raw -o - "$registry") || return 1
  gid=$(sudo -n /usr/bin/plutil -extract gid raw -o - "$registry") || return 1
  guid=$(sudo -n /usr/bin/plutil -extract guid raw -o - "$registry") || return 1
  owner=$(sudo -n /usr/bin/plutil -extract owner raw -o - "$registry") || return 1
  case "$uid" in ''|*[!0-9]*) return 1 ;; esac
  test "$uid" -ge 1000 && test "$uid" -lt 60000 || return 1
  test "$owner" = "$(id -u)" || return 1
  test "$uid" = "$(id -u "$account")" && test "$gid" = "$(id -g "$account")" || return 1
  test "$(sudo -n /usr/bin/plutil -extract version raw -o - "$registry")" = 1 || return 1
  test "$(sudo -n /usr/bin/plutil -extract name raw -o - "$registry")" = "$name" || return 1
  test "$(sudo -n /usr/bin/plutil -extract account raw -o - "$registry")" = "$account" || return 1
  test "$(sudo -n /usr/bin/plutil -extract home raw -o - "$registry")" = "$homes/$account" || return 1
  actual_guid=$(/usr/bin/dscl . -read "/Users/$account" GeneratedUID | awk '$1 == "GeneratedUID:" {print $2}') || return 1
  actual_home=$(/usr/bin/dscl . -read "/Users/$account" NFSHomeDirectory | awk '$1 == "NFSHomeDirectory:" {print $2}') || return 1
  test -n "$guid" && test "$guid" = "$actual_guid" && test "$actual_home" = "$homes/$account" || return 1
  printf '%s\n' "$uid"
}
only_macos_helpers() {
  # Input is ps uid,pid,ppid,comm, already scoped to the fixture UID. An active
  # shell, installer, editor, or unfamiliar service must prevent domain shutdown.
  awk -v uid="$1" '
    NF {seen=1}
    NF != 4 || $1 != uid || $2 !~ /^[0-9]+$/ || $3 != 1 {bad=1}
    $4 != "/usr/sbin/distnoted" && $4 != "/usr/sbin/cfprefsd" {bad=1}
    END {exit !seen || bad}
  '
}
wait_idle() {
  local name=$1 uid attempt processes booted=false
  if [ "$(uname -s)" = Darwin ]; then
    uid=$(macos_fixture_uid "$name") || { echo "Refusing service cleanup: fixture identity does not match its protected registry: $name" >&2; return 1; }
  else uid=$(id -u "devwright-$name") || return 1; fi
  for attempt in $(seq 1 20); do
    processes=$(ps -axo uid=,pid=,ppid=,comm= | awk -v uid="$uid" '$1 == uid') || return 1
    if [ -z "$processes" ]; then return 0; fi
    if [ "$(uname -s)" = Darwin ] && [ "$booted" = false ] && printf '%s\n' "$processes" | only_macos_helpers "$uid"; then
      printf 'Stopping macOS background services for recorded fixture devwright-%s (UID %s)\n' "$name" "$uid"
      if ! sudo -n /bin/launchctl bootout "user/$uid" > "$fixture/$name-bootout.log" 2>&1; then
        cat "$fixture/$name-bootout.log" >&2
        return 1
      fi
      booted=true
    fi
    sleep 1
  done
  printf '%s\n' "$processes" > "$fixture/$name-processes.log"
  printf '%s\n' "$processes" >&2
  echo "Fixture devwright-$name still has processes; close them before retrying cleanup." >&2
  return 1
}
restore_admin_fixtures() {
  # Backups stay root-owned at their original administrative locations. Restore
  # only the first recorded account's two explicitly exercised files.
  local path
  for path in "$base/users/${names[0]}.json" "/etc/ssh/devwright/devwright-${names[0]}.keys"; do
    if sudo -n test -f "$path.acceptance-backup"; then sudo -n mv -f "$path.acceptance-backup" "$path"; fi
  done
}
check_system_ssh() {
  # Remote Login's socket-activated wrapper creates missing host keys on the
  # first connection. Probe before running sshd directly. Scanned keys are only
  # a readiness check; the product pins keys read through its root helper.
  if ! ssh-keyscan -T 15 -p 22 localhost > "$fixture/ssh-readiness.log" 2>&1; then
    cat "$fixture/ssh-readiness.log" >&2
    echo 'System SSH is not ready on localhost:22. Enable Remote Login on macOS (or system SSH on Linux), then rerun this test.' >&2
    return 1
  fi
  if ! sudo -n /usr/sbin/sshd -t > "$fixture/sshd-preflight.log" 2>&1; then
    cat "$fixture/sshd-preflight.log" >&2
    echo 'The existing SSH service responded, but its configuration or host keys failed validation. Repair the host SSH setup before rerunning.' >&2
    return 1
  fi
}
cleanup() {
  local failed=false name
  restore_admin_fixtures || return 1
  if [ -f "$fixture/child-pid" ]; then
    local pid; pid=$(cat "$fixture/child-pid")
    case "$pid" in ''|*[!0-9]*) ;; *)
      if [ "$(ps -o uid= -p "$pid" 2>/dev/null | tr -d ' ')" = "$(id -u "devwright-${names[0]}" 2>/dev/null)" ]; then
        as_user "${names[0]}" /usr/bin/touch "$homes/devwright-${names[0]}/.devwright-test-stop" || true
      fi ;;
    esac
    rm -f "$fixture/child-pid"
  fi
  for name in "${names[@]}"; do
    if account_exists "$name"; then
      # Wait for test children; on macOS stop only verified fixture OS helpers.
      if ! wait_idle "$name" || ! cli delete "$name" --remove-home; then failed=true; fi
    fi
  done
  if [ "$failed" = true ]; then echo "Cleanup incomplete. Close fixture sessions, then run: bash $0 --cleanup '$fixture'" >&2; return 1; fi
  # Account identity tombstones deliberately remain reserved by the product.
  echo "Fixtures removed. Logs remain at: $fixture"
}
sudo -v
if [ "$cleanup_only" = true ]; then cleanup; exit; fi
finish() {
  status=$?
  trap - EXIT
  restore_admin_fixtures || status=1
  if [ "$keep" = true ]; then
    echo "Retained fixtures: $fixture"
    echo "Cleanup: bash $0 --cleanup '$fixture'"
  else cleanup || status=1; fi
  exit "$status"
}
trap finish EXIT
printf 'Test logs: %s\n' "$fixture"
printf synthetic-operator-private > "$fixture/private-operator"
# Keep real credentials out of every target session; synthetic host markers must
# not pass through the native frontend, helper, or provisioning environment.
export GH_TOKEN=synthetic-host-only OTHER_API_KEY=synthetic-host-only
export SSH_AUTH_SOCK="$fixture/nonexistent-host-agent"
for name in "${names[@]}"; do if account_exists "$name"; then echo 'Fixture account collision' >&2; exit 1; fi; done
check_system_ssh
sudo -n cat /etc/ssh/sshd_config > "$fixture/sshd.before"
sudo -n /usr/sbin/sshd -T -C "user=$(id -un),host=localhost,addr=127.0.0.1" > "$fixture/operator-ssh.before"
for file in /etc/gitconfig /etc/codex/requirements.toml /etc/sudoers; do
  if sudo -n test -f "$file"; then sudo -n cat "$file" > "$fixture/$(basename "$file").before"; else : > "$fixture/$(basename "$file").absent"; fi
done
cli render "${names[0]}" > "$fixture/render.json"
if cli create "${names[0]}" --cpus 2 > "$fixture/invalid.log" 2>&1; then echo 'Accepted VM resource flag' >&2; exit 1; fi
if cli create "${names[0]}" --codex-requirements /nonexistent > "$fixture/invalid-policy.log" 2>&1; then exit 1; fi
for name in "${names[@]}"; do
  # Exercise privilege changes from a directory inaccessible to the target UID.
  (cd "$fixture/client" && cli create "$name") > "$fixture/create-$name.log" 2>&1 || { cat "$fixture/create-$name.log"; exit 1; }
  if grep -q 'error retrieving current directory' "$fixture/create-$name.log"; then cat "$fixture/create-$name.log"; exit 1; fi
  cli install-ssh "$name"
  cli ssh-config "$name" > "$fixture/$name.ssh"
  ssh -F "$fixture/$name.ssh" "user-$name" 'test -z "${GH_TOKEN+x}" && test -z "${OTHER_API_KEY+x}" && test -z "${SSH_AUTH_SOCK+x}"'
  test "$(as_user "$name" /bin/pwd -P)" = "$homes/devwright-$name"
  as_user "$name" /bin/sh -c 'umask 077; printf synthetic-private > "$HOME/private-fixture"'
done
a=${names[0]}; b=${names[1]}
# Registry and administrative-file tampering must fail before privileged writes
# can follow attacker-selected identities or symlinks. Restore even on failure.
registry="$base/users/$a.json"
if as_user "$a" /bin/cat "$registry" 2>/dev/null; then echo 'Registry readable by target' >&2; exit 1; fi
sudo -n mv "$registry" "$registry.acceptance-backup"
sudo -n cat "$registry.acceptance-backup" | sed 's/"uid": [0-9][0-9]*/"uid": 1/' | sudo -n tee "$registry" >/dev/null
if cli configure "$a" > "$fixture/tampered-registry.log" 2>&1; then echo 'Tampered identity accepted' >&2; exit 1; fi
grep -q 'invalid managed account registry' "$fixture/tampered-registry.log"
restore_admin_fixtures
keys="/etc/ssh/devwright/devwright-$a.keys"
printf synthetic-admin-symlink-target > "$fixture/admin-symlink-canary"
cp "$fixture/admin-symlink-canary" "$fixture/admin-symlink-canary.before"
sudo -n mv "$keys" "$keys.acceptance-backup"
sudo -n ln -s "$fixture/admin-symlink-canary" "$keys"
if cli configure "$a" > "$fixture/admin-symlink.log" 2>&1; then echo 'Administrative symlink accepted' >&2; exit 1; fi
grep -q 'unsafe administrative file' "$fixture/admin-symlink.log"
cmp "$fixture/admin-symlink-canary.before" "$fixture/admin-symlink-canary"
restore_admin_fixtures
if as_user "$a" /bin/cat "$homes/devwright-$b/private-fixture" 2>/dev/null; then echo 'Cross-account read allowed' >&2; exit 1; fi
if as_user "$b" /bin/sh -c "printf changed > '$homes/devwright-$a/private-fixture'" 2>/dev/null; then exit 1; fi
if as_user "$a" /usr/bin/sudo -n true 2>/dev/null; then echo 'Account has sudo' >&2; exit 1; fi
if as_user "$a" /bin/cat "$fixture/private-operator" 2>/dev/null; then echo 'Operator fixture readable' >&2; exit 1; fi
if as_user "$a" /bin/sh -c "test -w '$base/users'"; then exit 1; fi
printf '%s\n' "export GH_TOKEN='synthetic-restricted-token'" "export OTHER_API_KEY='synthetic value'" | as_user "$a" /bin/sh -c 'cat > "$HOME/.config/devwright/credentials.sh"'
ssh -F "$fixture/$a.ssh" "user-$a" 'test "$GH_TOKEN" = synthetic-restricted-token && test "$OTHER_API_KEY" = "synthetic value" && /bin/sh -c '\''test "$GH_TOKEN" = synthetic-restricted-token'\'''
# Interactive entry uses exactly the shell action exposed to humans.
printf 'test "$GH_TOKEN" = synthetic-restricted-token && echo PASS_INTERACTIVE\nexit\n' | cli shell "$a" > "$fixture/shell.log" 2>&1
grep -q PASS_INTERACTIVE "$fixture/shell.log"
# Startup symlinks and early returns must remain intact across configure.
as_user "$a" /bin/bash -c 'mkdir -p "$HOME/dotfiles"; mv "$HOME/.bashrc" "$HOME/dotfiles/bashrc"; ln -s dotfiles/bashrc "$HOME/.bashrc"; printf "# preserve-native-config\n" >> "$HOME/.codex/config.toml"'
as_user "$a" /bin/cat "$homes/devwright-$a/.config/devwright/credentials.sh" > "$fixture/credentials.before"
as_user "$a" /bin/cat "$homes/devwright-$a/.codex/config.toml" > "$fixture/config.before"
cli configure "$a" > "$fixture/configure.log" 2>&1 || { cat "$fixture/configure.log"; exit 1; }
as_user "$a" test -L "$homes/devwright-$a/.bashrc"
as_user "$a" /bin/cat "$homes/devwright-$a/.config/devwright/credentials.sh" > "$fixture/credentials.after"
as_user "$a" /bin/cat "$homes/devwright-$a/.codex/config.toml" > "$fixture/config.after"
cmp "$fixture/credentials.before" "$fixture/credentials.after"
cmp "$fixture/config.before" "$fixture/config.after"
if command -v zsh >/dev/null 2>&1; then as_user "$a" "$(command -v zsh)" -c 'test "$GH_TOKEN" = synthetic-restricted-token'; fi
# Explicit config replacement replaces a symlink, preserving its former target.
as_user "$a" /bin/sh -c 'mv "$HOME/.codex/config.toml" "$HOME/config-preserve-target"; ln -s ../config-preserve-target "$HOME/.codex/config.toml"'
cp "$fixture/config.before" "$fixture/replacement.toml"
printf '# explicit replacement\n' >> "$fixture/replacement.toml"
cli configure "$a" --codex-config "$fixture/replacement.toml" --replace-codex-config > "$fixture/replace.log" 2>&1 || { cat "$fixture/replace.log"; exit 1; }
as_user "$a" test ! -L "$homes/devwright-$a/.codex/config.toml"
as_user "$a" /bin/cat "$homes/devwright-$a/config-preserve-target" > "$fixture/config-target.after"
cmp "$fixture/config.before" "$fixture/config-target.after"
# A failed setup preserves the malformed startup file and remains recoverable.
as_user "$a" /bin/cat "$homes/devwright-$a/.zshenv" > "$fixture/zshenv.before"
printf '# BEGIN DEVWRIGHT CREDENTIALS\nkeep-this-content\n' | as_user "$a" /bin/sh -c 'cat > "$HOME/.zshenv"'
if cli configure "$a" > "$fixture/malformed.log" 2>&1; then echo 'Malformed startup block accepted' >&2; exit 1; fi
grep -q 'malformed startup block' "$fixture/malformed.log"
as_user "$a" /bin/cat "$homes/devwright-$a/.zshenv" > "$fixture/zshenv.malformed"
grep -q keep-this-content "$fixture/zshenv.malformed"
as_user "$a" /bin/sh -c 'cat > "$HOME/.zshenv"' < "$fixture/zshenv.before"
cli configure "$a" > "$fixture/recovered.log" 2>&1 || { cat "$fixture/recovered.log"; exit 1; }
if cli create "$a" > "$fixture/collision.log" 2>&1; then echo 'Adopted existing account' >&2; exit 1; fi
cli list > "$fixture/list.log"
grep -q "$a" "$fixture/list.log"
# Use an unrelated synthetic key; deliberately ignore the generated client key.
ssh-keygen -q -t ed25519 -N '' -f "$fixture/wrong-key"
if ssh -F /dev/null -o BatchMode=yes -o ConnectTimeout=5 -o IdentityAgent=none -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o HostKeyAlias="user-$a" -o UserKnownHostsFile="$fixture/client/.ssh/devwright/user/$a/known_hosts" -i "$fixture/wrong-key" "devwright-$a@localhost" true 2> "$fixture/wrong-key.log"; then echo 'Wrong key accepted' >&2; exit 1; fi
grep -q 'Permission denied' "$fixture/wrong-key.log"
# An active account must be refused without terminating its process.
as_user "$a" /bin/sh -c 'echo $$; attempt=0; while [ "$attempt" -lt 120 ] && [ ! -f "$HOME/.devwright-test-stop" ]; do sleep 1; attempt=$((attempt + 1)); done' > "$fixture/child-pid" &
child_wrapper=$!
for attempt in 1 2 3 4 5; do test -s "$fixture/child-pid" && break; sleep 1; done
if as_user "$b" /bin/kill -0 "$(cat "$fixture/child-pid")" 2>/dev/null; then echo 'Cross-account process access allowed' >&2; exit 1; fi
if cli delete "$a" --remove-home > "$fixture/active-delete.log" 2>&1; then echo 'Deleted active account' >&2; exit 1; fi
grep -q 'running process' "$fixture/active-delete.log"
grep -q "$(cat "$fixture/child-pid") (" "$fixture/active-delete.log"
sudo -n kill -0 "$(cat "$fixture/child-pid")"
as_user "$a" /usr/bin/touch "$homes/devwright-$a/.devwright-test-stop"
wait "$child_wrapper" || true
rm -f "$fixture/child-pid"
# Preserve-home deletion must archive and reserve the former identity.
wait_idle "$b"
cli delete "$b" > "$fixture/archive.log"
archive=$(sed -n 's/^Home archived at //p' "$fixture/archive.log")
test -n "$archive"
sudo -n test -f "$archive/private-fixture"
if as_user "$a" /bin/cat "$archive/private-fixture" 2>/dev/null; then echo 'Archive accessible' >&2; exit 1; fi
# Explicit removal also works after an earlier preserve-home deletion.
cli delete "$b" --remove-home
sudo -n test ! -e "$archive"
sudo -n cat /etc/ssh/sshd_config > "$fixture/sshd.after"
sudo -n /usr/sbin/sshd -T -C "user=$(id -un),host=localhost,addr=127.0.0.1" > "$fixture/operator-ssh.after"
cmp "$fixture/sshd.before" "$fixture/sshd.after"
cmp "$fixture/operator-ssh.before" "$fixture/operator-ssh.after"
for file in /etc/gitconfig /etc/codex/requirements.toml /etc/sudoers; do
  if [ -f "$fixture/$(basename "$file").absent" ]; then sudo -n test ! -e "$file"; else sudo -n cat "$file" > "$fixture/$(basename "$file").after"; cmp "$fixture/$(basename "$file").before" "$fixture/$(basename "$file").after"; fi
done
cli verify "$a" > "$fixture/verify.log" 2>&1 || { cat "$fixture/verify.log"; exit 1; }
printf '%s\n' 'PASS native create/configure/list, shell/SSH, private accounts, credential preservation, wrong-key rejection, deletion refusal, private archive, and unrelated host configuration'

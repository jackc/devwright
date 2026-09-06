#!/bin/bash
# Root on Ubuntu 26.04: bash tests/environment-linux.sh lima/provision.sh
# Private mount/network/PID namespaces; only synthetic accounts and credentials.
set -euo pipefail
if [ "${1:-}" != --inside ]; then
  test "$(id -u)" = 0
  exec unshare --mount --net --pid --fork --mount-proc /bin/bash "$0" --inside "${1:?provision.sh path required}"
fi
provision=$2
fixture=$(mktemp -d /tmp/dev-sandbox-environment.XXXXXX)
trap 'rm -rf "$fixture"' EXIT
chmod 755 "$fixture"
mkdir -p "$fixture/home/dev" "$fixture/home/other" "$fixture/root" "$fixture/bin" "$fixture/environment.d"
mkdir "$fixture/etc-upper" "$fixture/etc-work"
mount -t overlay overlay -o "lowerdir=/etc,upperdir=$fixture/etc-upper,workdir=$fixture/etc-work" /etc
mount -t tmpfs tmpfs /run
# An unprovisioned test VM may supply extracted Ubuntu packages, used only
# in this namespace. The normal recipe installs gh and zsh as dependencies.
if [ -n "${DEV_SANDBOX_TEST_PACKAGES:-}" ]; then
  mkdir "$fixture/usr-upper" "$fixture/usr-work"
  mount -t overlay overlay -o "lowerdir=$DEV_SANDBOX_TEST_PACKAGES/usr:/usr,upperdir=$fixture/usr-upper,workdir=$fixture/usr-work" /usr
  if [ -d "$DEV_SANDBOX_TEST_PACKAGES/etc/zsh" ]; then
    cp -a "$DEV_SANDBOX_TEST_PACKAGES/etc/zsh" /etc/
  fi
fi
test -x /usr/bin/gh && test -x /usr/bin/zsh
awk -F: '$1 != "dev" && $1 != "other" && $1 != "root"' /etc/passwd > "$fixture/passwd"
printf 'root:x:0:0:Root:/root:/bin/bash\ndev:x:47123:47123:Development:/home/dev:/bin/bash\nother:x:47124:47124:Other:/home/other:/bin/bash\n' >> "$fixture/passwd"
awk -F: '$1 != "dev" && $1 != "other"' /etc/group > "$fixture/group"
printf 'dev:x:47123:\nother:x:47124:\n' >> "$fixture/group"
mount --bind "$fixture/passwd" /etc/passwd
mount --bind "$fixture/group" /etc/group
mount --bind "$fixture/home" /home
mount --bind "$fixture/root" /root
mount --bind "$fixture/bin" /usr/local/bin
chown dev:dev /home/dev
chown other:other /home/other
chmod 700 /home/dev /home/other /root
printf 'SITE_SETTING=preserve-this\n' > /etc/environment
chmod 644 /etc/environment
mkdir -p /etc/environment.d
mount --bind "$fixture/environment.d" /etc/environment.d
mount --bind "$fixture/environment.d" /usr/lib/environment.d
: > /etc/security/pam_env.conf
global_before=$(sha256sum /etc/environment)
cp "$(dirname "$provision")/credentials.sh" "$fixture/setup-credentials.sh"
chmod 644 "$fixture/setup-credentials.sh"
setup() {
  runuser -u dev -- env -i HOME=/home/dev USER=dev LOGNAME=dev PATH=/usr/bin:/bin \
    /bin/bash "$fixture/setup-credentials.sh"
}
# Realistic Bash defaults: the noninteractive return used to hide credentials.
cat > /home/dev/.bashrc <<'RC'
#!/bin/bash
case $- in *i*) ;; *) return ;; esac
export INTERACTIVE_SETTING=preserved
RC
printf '. "$HOME/.bashrc"\nexport PROFILE_SETTING=preserved\n' > /home/dev/.profile
chown dev:dev /home/dev/.bashrc /home/dev/.profile
setup
credentials=/home/dev/.config/dev-sandbox/credentials.sh
test "$(stat -c '%U:%G %a' "$credentials")" = 'dev:dev 600'
test ! -e /home/dev/.bash_profile && test ! -e /home/dev/.bash_login
cat > "$credentials" <<'ENV'
export GH_TOKEN='synthetic-github-token'
export OTHER_API_KEY='synthetic-other-key'
export DATABASE_URL='postgresql://agent:synthetic@localhost/db?sslmode=require'
export WITH_SPACES='synthetic value'
export SPECIAL_SECRET='literal $HOME # "quotes" backslash\ and a '\''quote'
# A fixture marker proves setup itself never sources this file.
touch "$HOME/credentials-sourced"
ENV
before=$(sha256sum "$credentials" /home/dev/.profile /home/dev/.bashrc /home/dev/.zshenv)
setup
setup
test "$before" = "$(sha256sum "$credentials" /home/dev/.profile /home/dev/.bashrc /home/dev/.zshenv)"
test ! -e /home/dev/credentials-sourced
test "$global_before" = "$(sha256sum /etc/environment)"
test "$(stat -c '%U:%G %a' /etc/environment)" = 'root:root 644'
runuser -u dev -- test -w "$credentials"
if runuser -u other -- test -r "$credentials"; then exit 1; fi
printf '%s\n' 'PASS private credential ownership, content preservation, and untouched system environment'

# Dotfiles may use symlinks and may replace/prepend startup content on updates.
mkdir /home/dev/dotfiles
mv /home/dev/.bashrc /home/dev/dotfiles/bashrc
ln -s dotfiles/bashrc /home/dev/.bashrc
printf 'export BASH_PROFILE_SETTING=preserved\n' > /home/dev/.bash_profile
printf 'export BASH_LOGIN_SETTING=preserved\n' > /home/dev/.bash_login
chown -R dev:dev /home/dev/dotfiles /home/dev/.bashrc /home/dev/.bash_profile /home/dev/.bash_login
setup
test -L /home/dev/.bashrc
test "$(head -1 /home/dev/dotfiles/bashrc)" = '#!/bin/bash'
# Prepending an early return moves the existing hook out of reach; reconfigure
# must move it back to the start without duplicating or deleting user content.
printf 'case $- in *i*) ;; *) return ;; esac\n' > "$fixture/prepend"
cat /home/dev/dotfiles/bashrc >> "$fixture/prepend"
cat "$fixture/prepend" > /home/dev/dotfiles/bashrc
setup
test "$(grep -c '^# BEGIN DEV-SANDBOX CREDENTIALS$' /home/dev/.bashrc)" = 1
grep -q 'INTERACTIVE_SETTING=preserved' /home/dev/.bashrc
printf '%s\n' 'PASS startup symlinks, existing login files, and hook repair before early returns'

cat > /etc/pam.d/sshd <<'PAM'
auth required pam_permit.so
account required pam_permit.so
session required pam_env.so
PAM
ssh-keygen -q -t ed25519 -N '' -f "$fixture/key"
chmod 644 "$fixture/key.pub"
cat > "$fixture/sshd_config" <<SSH
Port 22222
ListenAddress 127.0.0.1
HostKey $fixture/key
PidFile $fixture/sshd.pid
AuthorizedKeysFile $fixture/key.pub
StrictModes no
UsePAM yes
PermitRootLogin prohibit-password
PasswordAuthentication no
KbdInteractiveAuthentication no
AllowUsers dev other root
SSH
ip link set lo up
mkdir -p /run/sshd
env -i PATH=/usr/bin:/bin /usr/sbin/sshd -f "$fixture/sshd_config" -E "$fixture/sshd.log"
ssh_args=(-F /dev/null -i "$fixture/key" -o IdentitiesOnly=yes -o IdentityAgent=none -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -p 22222)
cat > "$fixture/check" <<'CHECK'
set -eu
test "$GH_TOKEN" = synthetic-github-token
test "$OTHER_API_KEY" = synthetic-other-key
test "$DATABASE_URL" = 'postgresql://agent:synthetic@localhost/db?sslmode=require'
test "$WITH_SPACES" = 'synthetic value'
test "$SPECIAL_SECRET" = 'literal $HOME # "quotes" backslash\ and a '\''quote'
/bin/sh -c 'test "$OTHER_API_KEY" = synthetic-other-key'
# A shell that skips startup files must still inherit from the SSH launcher.
/bin/bash --noprofile --norc -c 'test "$GH_TOKEN" = synthetic-github-token'
credential=$(printf 'protocol=https\nhost=github.com\n\n' | /usr/bin/gh auth git-credential get)
printf '%s\n' "$credential" | grep -qx 'password=synthetic-github-token'
printf '%s\n' 'PASS development environment and child inheritance'
CHECK
chmod 644 "$fixture/check"
for shell in /bin/bash /usr/bin/zsh; do
  # The namespace's passwd file is synthetic and mounted from this fixture.
  sed "s|^dev:.*|dev:x:47123:47123:Development:/home/dev:$shell|" /etc/passwd > "$fixture/passwd-next"
  cat "$fixture/passwd-next" > /etc/passwd
  ssh "${ssh_args[@]}" dev@127.0.0.1 "/bin/bash '$fixture/check'"
  printf '/bin/bash %s/check\nexit\n' "$fixture" | ssh -tt "${ssh_args[@]}" dev@127.0.0.1 > "$fixture/interactive" 2>&1
  grep -q 'PASS development environment' "$fixture/interactive"
  printf 'PASS %s interactive login and noninteractive SSH command\n' "$shell"
done
for account in root other; do
  ssh "${ssh_args[@]}" "$account@127.0.0.1" 'test -z "${GH_TOKEN+x}" && test -z "${OTHER_API_KEY+x}" && test -z "${SPECIAL_SECRET+x}"'
done
printf '%s\n' 'PASS root and other SSH accounts do not receive development credentials'
kill "$(cat "$fixture/sshd.pid")"

# Malformed managed blocks must not truncate or replace an existing startup file.
printf '# BEGIN DEV-SANDBOX CREDENTIALS\nkeep-this-content\n' > /home/dev/.zshenv
broken_before=$(sha256sum /home/dev/.zshenv)
if setup 2> "$fixture/broken-error"; then exit 1; fi
test "$broken_before" = "$(sha256sum /home/dev/.zshenv)"
grep -q 'malformed startup block' "$fixture/broken-error"
printf '%s\n' 'PASS malformed startup block leaves original file intact'

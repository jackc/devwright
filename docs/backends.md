# Backend setup

[Back to the README](../README.md)

- [Lima](#lima)
- [Incus on Linux](#incus-on-linux)
- [Restricted native users](#restricted-native-users-on-linux-and-macos)
- [Environments created as dev-sandbox](#environments-created-as-dev-sandbox)

## VM and container recipe

This project implements a reusable **Ubuntu 26.04 + Codex + Claude Code** environment with
**Lima VMs** and **Incus VMs or system containers**. Humans and agents develop as `dev`,
without sudo, in `/home/dev/projects`. Projects in one VM share that account's
files and credentials. Use another VM when they need separate access.
Administration uses key-only SSH as `root`.

Choose a backend: Lima **2.2+** and OpenSSH on macOS/Linux, or a local Incus
server and OpenSSH (including `ssh-keygen`) on Linux. Lima remains the default;
pass `--backend incus` on every Incus command. Incus requires no Lima installation.
The `devwright` Go executable embeds the complete recipe and runs from any directory.
Users of a prebuilt executable need no Go compiler on the host or guest. The CLI
embeds compiled Linux verifiers for arm64 and amd64 and installs the matching
one inside the guest. Before operating on an instance,
the CLI checks Lima's version or access to Incus, plus OpenSSH's required options.
Creation downloads an Ubuntu image and installs packages. Provisioning
installs the latest stable Codex release with the official standalone installer,
without requiring Node.js or npm. The root-owned package lives under
`/usr/local/share/codex`, with its command at `/usr/local/bin/codex`. Claude Code
comes from Anthropic's signed apt repository on its `latest` channel, as the
root-owned `/usr/bin/claude`; the release key is accepted only after its
fingerprint matches the documented value. See
[CLAUDE-CODE-DESIGN.md](../CLAUDE-CODE-DESIGN.md) for the design behind the Claude
Code controls and what has been validated.
The OS image selection comes
from the installed Lima Ubuntu 26.04 image template, or `images:ubuntu/26.04`
(the default, non-cloud variant) for Incus. OS package versions are not pinned.

## Lima

Follow the [README quick start](../README.md#quick-start) after installing
Lima 2.2+ and OpenSSH. For per-instance resources:

```sh
devwright create another-dev --cpus 8 --memory 8GiB --disk 100GiB
devwright install-ssh another-dev
```

Resource flags apply only to `create` and `render`; defaults are 4 CPUs, 4 GiB
memory, and a 60 GiB sparse disk. Sizes accept positive whole numbers with `MiB`,
`GiB`, or `TiB` units. Options may precede or follow the action/name.
`devwright render` prints the embedded recipe without contacting Lima or SSH.

`create` refuses an existing name and validates the generated Lima YAML. Before
using an instance, the launcher requires `dev` as its primary account and rejects
host mounts, agent forwarding, non-plain mode, and boot provisioning. It never
falls back to running development commands on the host.

During creation, Lima temporarily grants `dev` sudo so bootstrap can install
Lima's public login keys for root. The launcher confirms root SSH access, then
provisions as root and revokes `dev`'s sudo before installing development packages.
Private keys remain on the host. The template's `passwordlessSudo: true` is only
for bootstrap; completed VMs deny sudo to `dev`.

## Incus on Linux

Install and initialize Incus using [the Incus installation guide](https://linuxcontainers.org/incus/docs/main/installing/)
and `incus admin init`. Your Linux host account needs administration access to
Incus, usually through the `incus-admin` group; log in again after adding it.
This access belongs on the host, never in the guest's `dev` account.
The CLI always selects the **local server and default project**, independently
of the active Incus remote or project. Remote Incus servers and other projects
are not supported by this backend yet.

Create a managed network named `incusbr0` and storage pool named `default`
during initialization, or select existing ones with `--network` and `--storage`.
The storage driver must support root disk size limits, for example Btrfs or ZFS.
The directory driver on an ordinary ext4 filesystem cannot enforce container
disk quotas. Host networking and storage setup are explicit administrative steps;
`devwright` does not modify them.

```sh
# Hardware VM (requires working KVM and Incus's QEMU/firmware dependencies).
devwright create dev --backend incus

# Unprivileged system container (requires no hardware virtualization).
devwright create dev-container --backend incus --container

# Optional resources, existing storage pool, and managed network.
devwright create another-dev --backend incus --container \
  --cpus 8 --memory 8GiB --disk 100GiB --storage default --network incusbr0

devwright install-ssh dev --backend incus
ssh incus-dev
ssh root@incus-dev
devwright verify dev --backend incus
devwright configure dev --backend incus

incus --force-local --project default stop dev
incus --force-local --project default start dev
```

All actions, including optional dotfiles, use the same guest setup and acceptance
checks as Lima. `--container`, `--network`, and `--storage` apply only to Incus
`create` and `render`. Existing instance types are discovered from Incus; do not
repeat `--container` on `configure` or `verify`. A VM creation failure never
silently switches to a container. `render --backend incus` prints the JSON/YAML
configuration passed to `incus init`; the launcher supplies the image, name,
`--no-profiles`, and `--vm` (unless `--container`) separately.

Creation inherits no Incus profiles. The only devices are a root disk in the
selected pool and a NIC on the selected managed network. Containers explicitly
use `security.privileged=false`, `security.idmap.isolated=true`, and
`security.nesting=true`; nested namespaces allow Codex's bubblewrap sandbox to
run. Containers share the Linux host kernel, so they provide a different
isolation boundary from a hardware VM. The CLI rejects inherited profiles,
additional devices, host mounts/sockets, raw configuration, cloud-init scripts,
and other unsupported settings before running guest commands. CPU/memory/disk
changes should use Incus's own configuration tools.

Each Incus instance gets a new host-side Ed25519 key under
`~/.ssh/devwright/incus/NAME/`. Creation sends only its public key through Incus
to initialize root and dev SSH. The SSH host key is obtained through the local
Incus control plane and pinned in that directory's `known_hosts`. Provisioning,
verification, and token transfer then use SSH, with agent forwarding disabled.
Keep this directory to retain access; no personal host keys are imported.

The generated SSH entry uses `incus exec` and guest `nc` as a byte-stream proxy
to the guest SSH server. Connections therefore need no fixed guest IP or host
port, and the same entry survives stop/start. The account opening SSH must have
access to the local Incus daemon. `install-ssh` stores `NAME.incus.config`, which
can coexist with Lima entries, and preserves your existing SSH configuration.
For a desktop running on this Linux host, select `incus-NAME` as the SSH host.
When Incus runs inside Lima, these aliases live inside the Lima host VM.

If provisioning fails after SSH bootstrap, fix the cause and run `configure`.
A failure before SSH bootstrap may require deleting the incomplete instance
with Incus and creating it again. After deliberately deleting an instance, move
its `~/.ssh/devwright/incus/NAME/` directory aside before reusing the name. The
launcher refuses to reuse an old instance's identity. It does not automatically
delete failed instances, images, or host SSH state.

To test nested Incus VMs on a Mac, create a separate Lima Linux host with
`limactl start --nested-virt --mount-none --containerd=none --name=incus-host template:ubuntu-26.04`,
install Incus and the Linux `devwright` executable there, and follow the commands
above. Check `/dev/kvm` and an actual VM boot; the device alone does not prove
the complete nested VM stack works. Use `--container` when nested VMs cannot boot.
See [VALIDATION.md](../VALIDATION.md) for the tested environment and results.

## Restricted native users on Linux and macOS

Use `--backend user` to develop directly on the host as a separate OS account.
This backend requires no Lima, Incus, container, or new SSH daemon. The host's
existing system OpenSSH service supplies access; its network exposure remains
whatever the host administrator configured. The generated client alias connects
to localhost. Port 22 is the default; `--ssh-port` selects an **existing** SSH port,
never allocates one or creates a listener.

```sh
# Run as your normal administrator login; administrative steps invoke sudo.
devwright create project-a --backend user
devwright install-ssh project-a --backend user
ssh user-project-a
# Or enter an interactive shell without installing an SSH alias:
devwright shell project-a --backend user

devwright list --backend user
devwright verify project-a --backend user
devwright configure project-a --backend user

# An existing system SSH service using a different port:
devwright create project-b --backend user --ssh-port 2222
```

An environment named `project-a` creates account and private group
`devwright-project-a`, with home `/home/devwright-project-a` on Linux or
`/Users/devwright-project-a` on macOS. Native environment names have a 22-character
limit. Homes have mode 0700, development credentials have mode 0600, and the
accounts receive no sudo permissions or administrative groups. macOS accounts
are hidden from the login window and receive no Secure Token/FileVault setup.
macOS public-folder sharing groups are permitted only when their Directory
Services record directly includes `Everyone`. This is existing public access;
private sharing groups and inherited administrative memberships are rejected.
The standard `_lpoperator` printing group is likewise permitted only when its
record directly includes `localaccounts`; `_lpadmin` remains forbidden. Native
accounts share the host's ordinary local printing access. No printing or sharing
group policies are changed to grant them access.
An existing account, group, home, or unmanaged target file is never adopted.

Linux hosts need systemd, shadow account utilities, sudo, OpenSSH client/server,
Git, curl, Bash, bubblewrap, and socat. Codex's workspace sandbox and Claude
Code's Bash sandbox both need functioning Linux sandbox support. Ubuntu 24.04
and later restrict unprivileged user namespaces, and Ubuntu's stock
`bwrap-userns-restrict` profile confines the commands bubblewrap runs to a child
profile that denies capabilities, which blocks the nested user namespace Claude
Code's seccomp filter creates. The backend changes no host security policy, so
on such hosts it refuses to create accounts until an administrator installs the
profile Anthropic documents and disables the stock one, as the VM recipe does:

```sh
sudo mkdir -p /etc/apparmor.d/disable
sudo ln -sf /etc/apparmor.d/bwrap-userns-restrict /etc/apparmor.d/disable/bwrap-userns-restrict
sudo apparmor_parser -R /etc/apparmor.d/bwrap-userns-restrict
printf '%s\n' 'abi <abi/5.0>,' 'include <tunables/global>' '' \
  'profile bwrap /usr/bin/bwrap flags=(unconfined) {' '  userns,' '' \
  '  include if exists <local/bwrap>' '}' | sudo tee /etc/apparmor.d/bwrap >/dev/null
sudo apparmor_parser -r /etc/apparmor.d/bwrap
```

The profile is unconfined and inherited, so bubblewrap and every command it runs
may create user namespaces and hold capabilities inside them; the global
restriction stays enabled for everything else. Codex's sandbox runs under the
same profile. Other systemd/shadow distributions are supported by prerequisite
and behavior checks but have not received this repository's full acceptance run.
On macOS, install Command Line Tools for Git and enable **Remote Login** in System
Settings before creation. No host packages are installed automatically.
On a newly enabled Mac, Remote Login creates its SSH host keys on the first
connection. The CLI and acceptance script connect before inspecting the server
configuration so this normal macOS initialization can finish.

The main SSH server configuration must already have a global
`Include /etc/ssh/sshd_config.d/*.conf` (or `*`) directive. The tool installs a
scoped `Match User` drop-in and root-owned public-key file for each managed
account. It checks the effective settings, rejects conflicting host policy,
and reloads an already active Linux `ssh.service`/`sshd.service` when needed.
It never enables, starts, stops, or restarts the host service. macOS Remote Login
reads configuration for new connections; when its allowlist group exists,
only the new account's membership in that group is added. The tool does not
rewrite the main SSH configuration or change global authentication settings.

The managed SSH rules require public-key authentication and disable password,
keyboard-interactive, agent forwarding, X11 forwarding, and user SSH startup
commands. Local TCP forwarding remains available for development clients.
A host policy enabling `PermitUserEnvironment` is rejected; disable it
administratively before using this backend. Site-wide SSH allowlists and other
access policy may also need host-admin changes, which verification reports.

Each boundary gets a fresh operator-side key under
`~/.ssh/devwright/user/NAME/`. The system server's host keys are read through
the local administrative interface and pinned there. A host-key change requires
operator review; the CLI will not silently replace the pin. Use the same operator
login for subsequent commands and retain this directory. Generated aliases use
`NAME.user.config`, alongside existing Lima/Incus aliases. Direct SSH through
an installed alias requires no sudo; native administration and the `shell`
convenience action perform administrative checks through sudo.

Codex installs inside the restricted home, with its command at `~/.local/bin/codex`.
Configuration supplies editable workspace/on-request defaults, preserves inherited
development credentials, and initially disables integrations in the user config.
These are **user defaults, not enforced managed requirements**. Existing host-wide
Codex policy remains untouched. `--codex-config` and `--replace-codex-config` work;
`--codex-requirements`, `--reset-codex-requirements`, and VM resource flags are
rejected for this backend before provisioning.

Claude Code installs the same way, with the official installer on its `latest`
channel and its command at `~/.local/bin/claude`. The editable defaults in
`~/.claude/settings.json` turn the Bash sandbox on in strict mode, deny the same
secret paths to sandboxed commands and the file tools, and turn off connectors,
MCP servers, and the built-in browser and computer-use servers. Managed-only
keys cannot be set from a user file, so nothing here is enforced against the
account's own edits. `--claude-config` and `--replace-claude-config` work;
`--claude-managed-settings` and `--reset-claude-managed-settings` are rejected
because the host-wide managed settings file belongs to the host administrator.

Each account gets `~/.config/devwright/credentials.sh` and Bash/Zsh startup hooks,
using the [shared credential convention](configuration.md#credentials-and-sign-in). Configuration preserves
credential contents, projects, and personal Codex configuration. Optional
`--dotfiles-repo` / `--dotfiles-install` runs only as the restricted account.
Git configuration remains account-local; the GitHub helper is configured when
`gh` is already available. Host credentials and the administrator's dotfiles are
not copied or sourced. Configure while development programs are idle; existing
process environments do not update when credential files change.

```sh
# Close the account's shells and stop its processes first.
devwright delete project-a --backend user                # preserve its home
devwright delete project-b --backend user --remove-home  # explicitly erase it
```

Deletion requires an explicit name and refuses running processes. On Linux,
systemd's user manager can briefly remain after the last SSH session closes;
wait for it to exit before retrying. To remove an already retained home, repeat `delete NAME --backend user --remove-home`.
Retained homes move beneath the root-private
`/home/.devwright-archives` or `/Users/.devwright-archives` directory. The CLI
prints their exact path. Removal never recursively changes the ownership of
retained files. It removes the account's SSH/sudo entries and the operator's
managed client files, while leaving the shared SSH service running.
macOS can retain `distnoted` and `cfprefsd` for a user after its last SSH session
ends. These still count as running processes. After closing the account's work,
inspect `sudo launchctl print user/UID` and explicitly unload that user's domain
with `sudo launchctl bootout user/UID` before retrying deletion. Use the managed
account's recorded UID; the deletion command does not terminate processes itself.

Root-owned registry records live under `/var/lib/devwright/users` on Linux or
`/private/var/db/devwright/users` on macOS. Deleted records reserve their names
and numeric IDs; use another environment name when creating a replacement.
Interrupted configuration remains recorded and can be retried with `configure`;
interrupted deletion can be retried with `delete` using the same home-retention
choice. Identity mismatches and unrelated files are refused rather than repaired
by adopting them. The shared root-owned verifier and registry directories remain
installed after the last account is deleted.

The enforced boundary is the OS account: private account files, group membership,
and administrative access. Accounts still share the host kernel, networking,
world-readable files, writable public areas, and resources. Existing host secrets
must already have appropriate OS permissions; this tool does not change other
users' home permissions. Programs under one account can read that account's
credentials and change its tools and defaults. No filesystem allowlist, network
allowlist, CPU/memory quota, GUI isolation, or defense against kernel bugs is
claimed. Files created outside the managed home are not swept during deletion.

Native acceptance testing is documented in [Development](../DEVELOPMENT.md#native-acceptance-testing).

## Environments created as dev-sandbox

The project, CLI, Go module, managed paths, and native accounts now use
`devwright` (`devwright-NAME` for native accounts). Existing environments created
under the former `dev-sandbox` name are not automatically migrated. Retain the
previous executable to manage or remove them, and create fresh environments with
`devwright`. Existing `default-dev-vm` and `pgx-dev-vm` instances are not managed or
modified. Historical results in [VALIDATION.md](../VALIDATION.md) retain their
original names.

# Agent Sandbox Config

## Development VMs and containers

This project implements a reusable **Ubuntu 26.04 + Codex** environment with
**Lima VMs** and **Incus VMs or system containers**. Humans and agents develop as `dev`,
without sudo, in `/home/dev/projects`. Projects in one VM share that account's
files and credentials. Use another VM when they need separate access.
Administration uses key-only SSH as `root`.

Choose a backend: Lima **2.2+** and OpenSSH on macOS/Linux, or a local Incus
server and OpenSSH (including `ssh-keygen`) on Linux. Lima remains the default;
pass `--backend incus` on every Incus command. Incus requires no Lima installation.
The `agent-vm` Go executable embeds the complete recipe and runs from any directory.
Users of a prebuilt executable need neither Go nor Ruby on the host. Provisioning
installs Ruby inside the guest for verification. Before operating on an instance,
the CLI checks Lima's version or access to Incus, plus OpenSSH's required options.
Creation downloads an Ubuntu image and installs packages. Provisioning
installs the latest stable Codex release with the official standalone installer,
without requiring Node.js or npm. The root-owned package lives under
`/usr/local/share/codex`, with its command at `/usr/local/bin/codex`.
The OS image selection comes
from the installed Lima Ubuntu 26.04 image template, or `images:ubuntu/26.04`
(the default, non-cloud variant) for Incus. OS package versions are not pinned.

### Install

From a source checkout, install Go **1.25+**, then build the executable:

```sh
make build
mkdir -p ~/.local/bin
install -m 755 .build/agent-vm ~/.local/bin/agent-vm
export PATH="$HOME/.local/bin:$PATH"  # also add this to your shell startup file
```

On macOS, install Lima with `brew install lima`. For other hosts, follow
[Lima's installation instructions](https://lima-vm.io/docs/installation/).

Maintainers can build precompiled release archives and a Homebrew formula using
[the release process below](#development-and-releases). To install an archive,
extract the one matching your OS (`darwin` for macOS, `linux` for Linux) and CPU
(`arm64` for Apple Silicon/ARM, `amd64` for Intel/AMD), then install its `agent-vm`
executable into a directory on PATH. Verify its SHA-256 against the release's
`checksums.txt`. Release publication and a Homebrew tap must be set up in the
hosting repository; this source tree does not assume a particular GitHub owner.

```sh
agent-vm --version
agent-vm --help
agent-vm create agent-dev  # installs and verifies the setup
agent-vm install-ssh agent-dev

ssh lima-agent-dev           # dev: development, Codex, repositories
ssh root@lima-agent-dev   # root: VM administration
```

Create another isolated environment with the same recipe, optionally changing resources:

```sh
agent-vm create another-dev --cpus 8 --memory 8GiB --disk 100GiB
agent-vm install-ssh another-dev
```

Resource flags apply only to `create` and `render`; omitted values use 4 CPUs,
4 GiB memory, and a 60 GiB sparse disk. Sizes accept positive whole numbers with
`MiB`, `GiB`, or `TiB` units. `agent-vm render` prints the embedded Lima recipe
without contacting Lima or SSH. Options can precede or follow the action/name.

`create` refuses an existing name. The launcher checks that `dev` is the primary account
and rejects host mounts, agent forwarding, non-plain mode, or boot provisioning.
It never falls back to running development commands on the host. Existing
`default-dev-vm` and `pgx-dev-vm` instances are not managed or modified.

### Incus on Linux

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
`agent-vm` does not modify them.

```sh
# Hardware VM (requires working KVM and Incus's QEMU/firmware dependencies).
agent-vm create agent-dev --backend incus

# Unprivileged system container (requires no hardware virtualization).
agent-vm create agent-container --backend incus --container

# Optional resources, existing storage pool, and managed network.
agent-vm create another-dev --backend incus --container \
  --cpus 8 --memory 8GiB --disk 100GiB --storage default --network incusbr0

agent-vm install-ssh agent-dev --backend incus
ssh incus-agent-dev
ssh root@incus-agent-dev
agent-vm verify agent-dev --backend incus
agent-vm configure agent-dev --backend incus
agent-vm set-token agent-dev --backend incus

incus --force-local --project default stop agent-dev
incus --force-local --project default start agent-dev
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
`~/.ssh/agent-vms/incus/NAME/`. Creation sends only its public key through Incus
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
its `~/.ssh/agent-vms/incus/NAME/` directory aside before reusing the name. The
launcher refuses to reuse an old instance's identity. It does not automatically
delete failed instances, images, or host SSH state.

To test nested Incus VMs on a Mac, create a separate Lima Linux host with
`limactl start --nested-virt --mount-none --containerd=none --name=incus-host template:ubuntu-26.04`,
install Incus and the Linux `agent-vm` executable there, and follow the commands
above. Check `/dev/kvm` and an actual VM boot; the device alone does not prove
the complete nested VM stack works. Use `--container` when nested VMs cannot boot.
See [VALIDATION.md](VALIDATION.md) for the tested environment and results.

### Everyday use

```sh
limactl start agent-dev
ssh lima-agent-dev          # development as dev
ssh root@lima-agent-dev  # administration
limactl stop agent-dev
```

Lima manages starting, stopping, and deleting VMs. SSH provides interactive access;
there are no corresponding CLI wrapper commands. Our SSH entry includes Lima's
current connection file instead of copying its port, so a normal Lima restart
requires no SSH refresh. It disables agent forwarding and agent consultation.
Connection sharing uses `~/.ssh/control-%C`: OpenSSH's hash includes the remote
user, host, and port, so development and administration use separate connections.
Idle shared connections close after 60 seconds. These settings precede Lima's
included settings, overriding its single control socket per VM. Rerun
`install-ssh` to update a previously installed entry.

Restarting does not run our setup script or update Codex. Your VM's disk and
installed settings persist. Use `agent-vm verify agent-dev` to recheck
the restrictions without updating tools.

### GitHub and Codex sign-in

Supply a fine-grained GitHub token for each VM interactively from the host:

```sh
agent-vm set-token agent-dev
```

The prompt hides input. The token travels on SSH stdin and is stored with mode
0600 in `/home/dev/.config/agent-vm/github-token`. It is never included in the
recipe or passed as a process argument. `/usr/local/bin/gh` loads it for both
interactive and noninteractive use; system Git configuration uses that helper
for GitHub HTTPS. `dev` and its agents can read this token by design. An explicitly
supplied `GH_TOKEN` takes precedence. No primary host credential is imported.

Inside `ssh lima-agent-dev`:

```sh
cd ~/projects
gh auth status
# Replace OWNER/REPO with an approved repository.
gh repo clone OWNER/REPO
codex login --device-auth
```

Complete Codex sign-in in your browser. If device authentication is unavailable
for your account, use the authentication flow supported by your Codex client.
Set your Git author name/email as `dev` when needed, or use the optional
dotfiles installer below. Provisioning does not copy host Git configuration.

For the desktop, add **lima-agent-dev** as an SSH host in its remote connection
settings, then select a guest directory under `/home/dev/projects`. Use the
`dev` connection, not a connection with the username `root`. The app may
install a separate remote runtime; verify its version and effective managed
policy in a fresh task. Installation of the CLI does not authenticate the desktop.

### Change or update the setup

| File | Responsibility |
| --- | --- |
| `cli.go`, `vm.go`, `incus.go`, `process.go`, `terminal.go`, `ssh.go` | Host CLI, Lima/Incus orchestration, subprocesses, terminal input, and SSH configuration |
| `assets.go` | Embeds the recipe at build time |
| `lima/agent.json` | Lima template (JSON is valid YAML): image base, resources, primary account, plain mode |
| `lima/bootstrap.sh` | Creation-only setup of key-based root SSH |
| `incus/bootstrap.sh` | Creation-only SSH/account setup through Incus |
| `lima/provision.sh` | Shared repeatable OS, account, SSH, GitHub helper, and Codex installation for both backends |
| `lima/dotfiles.sh` | Optional per-account dotfiles installation for root and dev |
| `config/codex/requirements.toml` | Root-owned, VM-wide managed restrictions |
| `config/codex/config.toml` | Initial dev defaults, preserved after first installation |
| `scripts/verify_guest.rb` | Credential-free Linux and sandbox acceptance checks |

Install the updated executable, then apply its embedded recipe with `configure`.
Configuration also updates Codex to the latest stable release. When developing
the recipe, rebuild and reinstall after editing the shared source.
The version installed during provisioning is recorded
in `/usr/local/share/agent-vm/codex-version` for diagnostics; verification checks
actual policy behavior rather than requiring that exact version:

```sh
agent-vm configure agent-dev  # applies the recipe and verifies it
```

During creation, Lima initially grants `dev` sudo. The creation-only bootstrap
installs Lima's public login keys for root and enables key-only root SSH. The
launcher confirms root access, then runs provisioning as root, which revokes
`dev`'s sudo access before installing development packages. Private keys remain
on the host. The template's `passwordlessSudo: true` is only for this bootstrap;
completed VMs deny sudo to `dev`.

`configure` sends the current Bash setup script and embedded policy files
directly over root SSH. Lima stores no setup script to replay on boot. Recipe
edits take effect only after rebuilding the executable and explicitly running
`configure`. Updating the
executable alone does not modify existing VMs.

`configure` applies setup to a running VM without rebooting it; if stopped, it
starts the VM first. It installs the latest Codex, updates managed policy and
system settings, and runs the guest acceptance checks. It preserves `dev`'s
personal Codex config, credentials, and projects. Use it while development tools
are idle because it updates installed software and reloads SSH configuration.

### Optional dotfiles

Pass your own Git repository when creating or configuring a VM:

```sh
agent-vm create my-dev --dotfiles-repo https://github.com/OWNER/dotfiles.git
agent-vm configure my-dev --dotfiles-repo https://github.com/OWNER/dotfiles.git --dotfiles-install setup.sh
```

No dotfiles are installed by default. The default installer is `install`; use
`--dotfiles-install` for another executable path relative to the repository.
The installer must have a shebang and run without interaction. It runs with the
repository as its working directory and the target account's HOME, USER, LOGNAME,
and SHELL. It is responsible for its dependencies, backups, startup files, and
any shell preferences; the recipe does not assume Mise or Zsh.

Both `root` and `dev` get independent checkouts at
`~/.local/share/agent-vm/dotfiles`. The installer runs as each account, so it must
support `dev` without sudo. Only supply repositories you trust to run as root.
The repository must be accessible from both guest accounts; host credentials
and SSH agents are not forwarded. Public HTTPS repositories work without setup.
GitHub HTTPS is configured to use the VM token helper after installation.
Keep `/usr/local/bin` ahead of alternative Codex and `gh` installations in your
installer's PATH settings.

Repeat the options on `configure` to update and rerun the installer. Updates use
`git pull --ff-only`; local conflicts stop setup. A different repository URL is
rejected for an existing checkout; move that checkout aside in each account
before switching repositories. Omitting the options leaves installed dotfiles
alone and does not update or remove them.

### Configuration behavior

Explicit provisioning restores policy, SSH settings, and `dev`'s empty
supplementary group list. Manual changes to those settings survive normal
restarts but are overwritten by `configure`. Root's authorized keys are
initialized once during creation and are not recopied from development files
during configuration.
If setup fails, fix the cause and rerun `configure`; restarting does not retry it.
Full acceptance checks run during `create`, `configure`, and `verify`. Lima
startup does not run our verification.

Resource/image changes in `agent.json` apply to newly created VMs after rebuilding.
Use `--cpus`, `--memory`, and `--disk` for per-VM resource choices during creation.
Change existing VM resources with Lima's own stopped-instance editing workflow. Run Ubuntu
security upgrades administratively as needed; package installation is not a
substitute for a guest patching policy.

`install-ssh` adds an Include to `~/.ssh/config`, backs up that file before
changing it, and stores a small SSH entry under `~/.ssh/agent-vms/`. The entry includes
Lima's own SSH configuration and defaults to `dev`; `root@` overrides the user. It refuses
to overwrite an unrelated generated-file target or rewrite a symlinked SSH
config. Existing entries generated by the Ruby CLI are recognized and updated.
`ssh-config` prints the entry instead if you manage SSH configuration
through your own dotfiles tooling.
Create fresh VMs for the Ubuntu 26.04 recipe with `dev` as the primary user. Migrating VMs made
with earlier recipes, including boot provisioning, is not supported.

### Isolation and validation limits

* Plain mode disables host filesystem mounts, SSH-agent forwarding, automatic
  port forwarding, and bundled containerd. Use explicit SSH tunnels for previews,
  for example `ssh -N -L 3000:127.0.0.1:3000 lima-agent-dev`.
* Linux protects `/root` from `dev`; managed Codex policy additionally
  denies common sensitive paths, permits workspace writes and direct networking,
  and disables apps, plugins, browser/computer use and configured MCP servers.
* Ubuntu 26.04 supplies the bubblewrap AppArmor profile. No custom profile is
  installed; Ubuntu's global user-namespace restriction stays enabled, and Codex
  applies its own filesystem sandbox. Older Ubuntu releases are not supported.
* The helper and policy files are root-owned. root SSH permits public-key authentication only;
  do not expose an admin SSH connection or rootful Docker socket to agents.
* Anyone running as `dev` can modify that account's startup files, tools, and
  project code. Do not run such files as `root`. Guest policy controls
  supported Codex clients, not arbitrary replacement binaries.
* Read denial is tested with a synthetic file, including a conflicting config
  override. The test refuses to touch an existing `.pgpass`. No real secret or
  SSH-agent socket is probed.
* CLI/app-server checks do **not** prove that a desktop task lacks host-provided
  connectors, computer tools, or cross-task capabilities. Inspect a fresh task's
  tool inventory before treating that separation as verified.
* Authentication and GitHub repository scope need your own credentials. Test one
  allowed and one **disallowed private repository**; public repositories are not
  a valid negative test. No network allowlist or Claude setup is included.

`create` also validates the generated Lima YAML.
See [VALIDATION.md](VALIDATION.md) for the actual VM test results and research provenance.

### Development and releases

The host CLI uses Go's standard library plus `golang.org/x/term` and
`golang.org/x/sys` for hidden, interruptible token entry. Versions and checksums
are recorded in `go.mod` and `go.sum`. Source builds require Go 1.25+; guest
verification and terminal regression tests also require Ruby 3.1+ and Minitest
(`gem install minitest`). Tests use Git, Bash, and OpenSSH locally, with synthetic
data and temporary directories. Terminal regressions open temporary pseudo-terminals;
they need `/dev/tty` access when run inside a filesystem sandbox.

```sh
make check                 # Go tests/vet, guest and terminal tests, Bash syntax
make build                 # .build/agent-vm, recipe embedded at build time
go test -race ./...
make release VERSION=v0.1.0 # four OS/CPU archives and checksums
# Supply the actual hosting repository to also generate a Homebrew formula:
make release VERSION=v0.1.0 REPOSITORY=OWNER/REPO
```

Release files are written to `.build/releases/VERSION/`. Archives contain the
executable, documentation, and dependency license notices. `CGO_ENABLED=0` keeps
builds free of C library dependencies on Linux. macOS binaries still use OS
libraries. Builds cover macOS/Linux on arm64/amd64; full VM acceptance has been
exercised on macOS arm64. Cross-compilation alone does not validate other hosts'
virtualization setup.

GitHub Actions runs checks on macOS and Linux. Pushing a `vX.Y.Z` tag runs checks,
builds the four archives and Homebrew formula using the hosting repository's name,
and creates a **draft** GitHub release. Review and publish the draft, then copy
its generated `agent-vm.rb` into `Formula/agent-vm.rb` in your Homebrew tap. Users
can then install with `brew install OWNER/TAP/agent-vm`; the formula depends on
Lima on macOS; install your chosen backend separately on Linux. Replace OWNER/TAP with the actual tap name. No public release or tap is
created by a local build.

## Original project goals

The goal of this project is to better understand how to configure the sandbox and permission settings for Codex and Claude Code.

I have certain things I want my agents to be able to do and access and certain things I don't. I want to use this to experiment and ultimately come up with template settings for other projects.

## Goals

I want to limit access to my SSH key and key agent. Ideally, it would be limited to a key that can only access specified repositories on Github. The AI agent should not be able to directly use my primary SSH key agent.

I want to restrict access to Github, presumably with `gh` and a PAT that only has permissions for specified repositories. I don't want agents in one project able to read keys in another project.

I do not want agents to be able to read sensitive dotfiles like `.pgpass`.

I do not want agents to accidentally have access to apps, plugins, connectors, etc. that are defined on the web. e.g. I don't want a coding agent on a random application to be able to access my Gmail and Google Drive. But I also want the desktop apps to have that permission.

I would prefer to have safe by default settings. Forgetting to apply these settings to a project shouldn't fail open. Possibly, this means that global settings can be restrictive and project settings can loosen them. This is an ideal, if it can't be done I still want to set up the sandboxes.

## Related Possibility

Connecting with SSH to a VM or a restricted user on the same machine is a possibility. However, I prefer not do do this to avoid having to set up my shell environment there.

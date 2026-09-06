# Dev Sandbox

Isolated development environments for humans and coding agents.

## Development VMs and containers

This project implements a reusable **Ubuntu 26.04 + Codex** environment with
**Lima VMs** and **Incus VMs or system containers**. Humans and agents develop as `dev`,
without sudo, in `/home/dev/projects`. Projects in one VM share that account's
files and credentials. Use another VM when they need separate access.
Administration uses key-only SSH as `root`.

Choose a backend: Lima **2.2+** and OpenSSH on macOS/Linux, or a local Incus
server and OpenSSH (including `ssh-keygen`) on Linux. Lima remains the default;
pass `--backend incus` on every Incus command. Incus requires no Lima installation.
The `dev-sandbox` Go executable embeds the complete recipe and runs from any directory.
Users of a prebuilt executable need no Go compiler on the host or guest. The CLI
embeds compiled Linux verifiers for arm64 and amd64 and installs the matching
one inside the guest. Before operating on an instance,
the CLI checks Lima's version or access to Incus, plus OpenSSH's required options.
Creation downloads an Ubuntu image and installs packages. Provisioning
installs the latest stable Codex release with the official standalone installer,
without requiring Node.js or npm. The root-owned package lives under
`/usr/local/share/codex`, with its command at `/usr/local/bin/codex`.
The OS image selection comes
from the installed Lima Ubuntu 26.04 image template, or `images:ubuntu/26.04`
(the default, non-cloud variant) for Incus. OS package versions are not pinned.

### Install

From a source checkout, install [mise](https://mise.jdx.dev/getting-started.html)
(`brew install mise` on macOS), then install the pinned Go toolchain and build:

```sh
mise trust
mise install
mise run build
mkdir -p ~/.local/bin
install -m 755 .build/dev-sandbox ~/.local/bin/dev-sandbox
export PATH="$HOME/.local/bin:$PATH"  # also add this to your shell startup file
```

On macOS, install Lima with `brew install lima`. For other hosts, follow
[Lima's installation instructions](https://lima-vm.io/docs/installation/).

Maintainers can build precompiled release archives and a Homebrew formula using
[the release process below](#development-and-releases). To install an archive,
extract the one matching your OS (`darwin` for macOS, `linux` for Linux) and CPU
(`arm64` for Apple Silicon/ARM, `amd64` for Intel/AMD), then install its `dev-sandbox`
executable into a directory on PATH. Verify its SHA-256 against the release's
`checksums.txt`. Release publication and a Homebrew tap must be set up in the
hosting repository; this source tree does not assume a particular GitHub owner.

```sh
dev-sandbox --version
dev-sandbox --help
dev-sandbox create dev  # installs and verifies the setup
dev-sandbox install-ssh dev

ssh lima-dev           # dev: development, Codex, repositories
ssh root@lima-dev   # root: VM administration
```

Create another isolated environment with the same recipe, optionally changing resources:

```sh
dev-sandbox create another-dev --cpus 8 --memory 8GiB --disk 100GiB
dev-sandbox install-ssh another-dev
```

Resource flags apply only to `create` and `render`; omitted values use 4 CPUs,
4 GiB memory, and a 60 GiB sparse disk. Sizes accept positive whole numbers with
`MiB`, `GiB`, or `TiB` units. `dev-sandbox render` prints the embedded Lima recipe
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
`dev-sandbox` does not modify them.

```sh
# Hardware VM (requires working KVM and Incus's QEMU/firmware dependencies).
dev-sandbox create dev --backend incus

# Unprivileged system container (requires no hardware virtualization).
dev-sandbox create dev-container --backend incus --container

# Optional resources, existing storage pool, and managed network.
dev-sandbox create another-dev --backend incus --container \
  --cpus 8 --memory 8GiB --disk 100GiB --storage default --network incusbr0

dev-sandbox install-ssh dev --backend incus
ssh incus-dev
ssh root@incus-dev
dev-sandbox verify dev --backend incus
dev-sandbox configure dev --backend incus

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
`~/.ssh/dev-sandbox/incus/NAME/`. Creation sends only its public key through Incus
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
its `~/.ssh/dev-sandbox/incus/NAME/` directory aside before reusing the name. The
launcher refuses to reuse an old instance's identity. It does not automatically
delete failed instances, images, or host SSH state.

To test nested Incus VMs on a Mac, create a separate Lima Linux host with
`limactl start --nested-virt --mount-none --containerd=none --name=incus-host template:ubuntu-26.04`,
install Incus and the Linux `dev-sandbox` executable there, and follow the commands
above. Check `/dev/kvm` and an actual VM boot; the device alone does not prove
the complete nested VM stack works. Use `--container` when nested VMs cannot boot.
See [VALIDATION.md](VALIDATION.md) for the tested environment and results.

### Everyday use

```sh
limactl start dev
ssh lima-dev          # development as dev
ssh root@lima-dev  # administration
limactl stop dev
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
installed settings persist. Use `dev-sandbox verify dev` to recheck
the restrictions without updating tools.

### Credentials and sign-in

Development credentials live in `/home/dev/.config/dev-sandbox/credentials.sh`,
owned by `dev` with mode `0600`, inside a mode `0700` directory. Use shell-quoted
`export` assignments so child processes inherit the values:

```sh
export GH_TOKEN='github_pat_REPLACE_ME'
export OTHER_API_KEY='REPLACE_ME'
export DATABASE_URL='postgresql://agent:REPLACE_ME@localhost/app'
```

Edit the file as `dev` with your preferred editor:

```sh
ssh lima-dev  # or incus-dev
vi ~/.config/dev-sandbox/credentials.sh
```

Root can populate the same file administratively, for example by running
`sudo -u dev -H vi /home/dev/.config/dev-sandbox/credentials.sh`. Keep it owned by
`dev` with mode `0600` if another editor or deployment tool replaces it.

The recipe creates a commented template only when the file is absent and
preserves its contents during `configure`. Every process running as `dev` can
read these credentials by design. Other users receive no startup hook and cannot
read the private file; root retains normal administrative access. Host
credentials are not imported. There is no credential setter or per-command
wrapper in the host CLI. Keep the credential file out of version control.

After any optional dotfiles installer, configuration adds a small managed
source block to `dev`'s `.profile`, `.bashrc`, and `.zshenv`, and to `.bash_profile`
and `.bash_login` if those files already exist. It does not create the latter two,
which would shadow other Bash login files. Hooks go before noninteractive early
returns, preserving a leading shebang, existing content, and valid startup-file
symlinks. Repeated configuration repairs or moves the block without duplicating
it. Setup runs as `dev` and never sources the credential file as root.

Bash's login files cover interactive SSH logins; `.bashrc` also covers ordinary
Bash SSH remote commands. Zsh loads `.zshenv` for interactive and noninteractive
invocations. Once the entry shell exports credentials, its descendants—including
Git, `gh`, SDKs, scripts, and Codex—inherit them even if a child skips startup
files. See [Bash startup rules](https://www.gnu.org/software/bash/manual/html_node/Bash-Startup-Files)
and [Zsh startup rules](https://zsh.sourceforge.io/Doc/Release/Files.html).

Keep the credential file to quiet, shell-compatible export assignments: it is
sourced as shell code and may be read more than once per login. Single quotes
preserve dollar signs, spaces, and most punctuation literally; a literal single
quote needs shell escaping. It must not print output or require a terminal.

If custom dotfiles select another `ZDOTDIR` before the home `.zshenv` runs, add
the source block to that directory's `.zshenv` yourself. Rerun `configure` after
replacing startup files or adding a new Bash login file. Other login shells need
their own startup integration. Shells launched with
startup files disabled and no inherited credentials, `incus exec`, cron, and
independently started systemd services need their own environment setup. The
recipe does not modify `/etc/environment`, PAM, or systemd environment settings.

After changing values, open a fresh session and restart existing agents and
remote runtimes. Existing processes retain their previous environment. For
Codex desktop remote use, the runtime must start through `dev`'s configured SSH
shell; reconnect/restart that runtime rather than only creating another task.
The initial Codex config explicitly preserves the inherited environment:

```toml
[shell_environment_policy]
inherit = "all"
ignore_default_excludes = true
```

Existing and custom Codex configs are preserved, so check these settings and any
explicit environment filters when upgrading. See the
[Codex configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference#shell_environment_policyignore_default_excludes).
GitHub HTTPS uses the packaged `/usr/bin/gh auth git-credential`, which reads
`GH_TOKEN` from its environment. Scope the token to repositories this VM needs.

Inside `ssh lima-dev`:

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

For the desktop, add **lima-dev** as an SSH host in its remote connection
settings, then select a guest directory under `/home/dev/projects`. Use the
`dev` connection, not a connection with the username `root`. The app may
install a separate remote runtime; verify its version and effective managed
policy in a fresh task. Installation of the CLI does not authenticate the desktop.

### Change or update the setup

| File | Responsibility |
| --- | --- |
| `cli.go`, `vm.go`, `incus.go`, `process.go`, `ssh.go` | Host CLI, Lima/Incus orchestration, subprocesses, and SSH configuration |
| `assets.go` | Embeds the recipe at build time |
| `lima/dev-sandbox.json` | Lima template (JSON is valid YAML): image base, resources, primary account, plain mode |
| `lima/bootstrap.sh` | Creation-only setup of key-based root SSH |
| `incus/bootstrap.sh` | Creation-only SSH/account setup through Incus |
| `lima/provision.sh` | Shared repeatable OS, account, SSH, development credential hooks, Git authentication, and Codex installation for both backends |
| `lima/dotfiles.sh` | Optional per-account dotfiles installation for root and dev |
| `lima/credentials.sh` | Private dev credential file and Bash/Zsh startup hooks |
| `config/codex/requirements.toml` | Root-owned, VM-wide managed restrictions |
| `config/codex/config.toml` | Initial dev defaults, preserved after first installation |
| `internal/verification/`, `cmd/dev-sandbox-verify/` | Go Linux, Codex policy, and sandbox acceptance checks |
| `scripts/build-guest.sh`, `guestbin/` | Build and embed the Linux guest verifiers |

Install the updated executable, then apply its embedded recipe with `configure`.
Configuration also updates Codex to the latest stable release. When developing
the recipe, rebuild and reinstall after editing the shared source.
The version installed during provisioning is recorded
in `/usr/local/share/dev-sandbox/codex-version` for diagnostics; verification checks
actual policy behavior rather than requiring that exact version:

```sh
dev-sandbox configure dev  # applies the recipe and verifies it
```

During creation, Lima initially grants `dev` sudo. The creation-only bootstrap
installs Lima's public login keys for root and enables key-only root SSH. The
launcher confirms root access, then runs provisioning as root, which revokes
`dev`'s sudo access before installing development packages. Private keys remain
on the host. The template's `passwordlessSudo: true` is only for this bootstrap;
completed VMs deny sudo to `dev`.

`configure` sends the current Bash setup script and embedded policy files
directly over root SSH. Lima stores no setup script to replay on boot. Embedded recipe
edits take effect only after rebuilding the executable and explicitly running
`configure`; the Codex file overrides below require no rebuild. Updating the
executable alone does not modify existing VMs.

`configure` applies setup to a running VM without rebooting it; if stopped, it
starts the VM first. It installs the latest Codex, restores the selected managed policy and
system settings, and runs the guest acceptance checks. It preserves `dev`'s
personal Codex config unless explicitly replaced, plus credentials and projects. Use it while development tools
are idle because it updates installed software and reloads SSH configuration.

### Custom Codex policy and defaults

The executable includes default Codex files. Supply explicit host file paths to
replace either complete file; settings are not merged and project directories
are not searched automatically. Both backends support these options:

```sh
dev-sandbox create my-dev \
  --codex-requirements ./requirements.toml \
  --codex-config ./config.toml

# Select a different policy for an existing VM.
dev-sandbox configure my-dev --codex-requirements ./requirements.toml

# Explicitly replace an existing personal config.
dev-sandbox configure my-dev --codex-config ./config.toml --replace-codex-config

# Stop using a custom policy and restore the executable's embedded policy.
dev-sandbox configure my-dev --reset-codex-requirements
```

Files are read and checked for valid TOML before contacting the instance manager.
Codex validates its full configuration schema in the guest during verification;
a schema error can therefore fail setup after the files have been installed.
Correct the files and rerun `configure` with the override options.

`--codex-requirements` installs `/etc/codex/requirements.toml` and saves a root-owned
copy at `/usr/local/share/dev-sandbox/custom-requirements.toml`. Future `configure`
runs restore that saved selection, even if the original host file is gone.
Supplying a new file replaces the saved selection. `--reset-codex-requirements`
removes it and restores the embedded policy. VMs without a custom selection get
the current executable's embedded policy on every `configure`. Editing the host
file or upgrading the executable does not update a VM until `configure` runs.

`--codex-config` supplies `/home/dev/.codex/config.toml` only when absent.
An existing file remains untouched unless `--replace-codex-config` is supplied
with `--codex-config` on `configure`. This option overwrites the personal config;
back it up first if needed. It does not alter credentials. The initial config
is not saved as a persistent template. Keep the personal config compatible with
the selected requirements: the embedded user config selects `vm_dev`, so a
policy using another profile may also need a matching user config.

These are operator-controlled overrides. Managed requirements remain root-owned;
`dev` can change personal preferences but cannot use them to loosen managed
requirements in supported Codex clients. Root dotfiles installers also have
administrative access and should leave managed policy files to this mechanism.

Verification always checks Linux isolation, policy ownership and checksum, and
Codex's strict config loading. It compares the selected managed default, allowed
profiles, and feature requirements with the app-server response and checks
resolved feature enforcement. The embedded policy additionally gets the existing
workspace-write and secret-read-denial sandbox probes. Custom policies report
those behavior probes as **not tested**: arbitrary filesystem/network policies
need their own acceptance tests. Successful verification does not certify a
custom policy as equivalent to the embedded restrictions.

### Optional dotfiles

Pass your own Git repository when creating or configuring a VM:

```sh
dev-sandbox create my-dev --dotfiles-repo https://github.com/OWNER/dotfiles.git
dev-sandbox configure my-dev --dotfiles-repo https://github.com/OWNER/dotfiles.git --dotfiles-install setup.sh
```

No dotfiles are installed by default. The default installer is `install`; use
`--dotfiles-install` for another executable path relative to the repository.
The installer must have a shebang and run without interaction. It runs with the
repository as its working directory and the target account's HOME, USER, LOGNAME,
and SHELL. It is responsible for its dependencies, backups, startup files, and
any shell preferences; the recipe does not assume Mise or Zsh.

Both `root` and `dev` get independent checkouts at
`~/.local/share/dev-sandbox/dotfiles`. The installer runs as each account, so it must
support `dev` without sudo. Only supply repositories you trust to run as root.
The repository must be accessible from both guest accounts; host credentials
and SSH agents are not forwarded. Public HTTPS repositories work without setup.
GitHub HTTPS is configured to use the packaged `gh` credential helper after installation.
The installer intentionally starts with a minimal environment, so credentials in
`credentials.sh` are not loaded during dotfiles installation.
Keep `/usr/local/bin` ahead of alternative Codex installations in your
installer's PATH settings.

Repeat the options on `configure` to update and rerun the installer. Updates use
`git pull --ff-only`; local conflicts stop setup. A different repository URL is
rejected for an existing checkout; move that checkout aside in each account
before switching repositories. Omitting the options leaves installed dotfiles
alone and does not update or remove them.

### Configuration behavior

Explicit provisioning restores the selected policy, SSH settings, and `dev`'s empty
supplementary group list. Manual changes to those settings survive normal
restarts but are overwritten by `configure`. Root's authorized keys are
initialized once during creation and are not recopied from development files
during configuration.
If setup fails, fix the cause and rerun `configure`; restarting does not retry it.
Applicable acceptance checks run during `create`, `configure`, and `verify`. Lima
startup does not run our verification.

Resource/image changes in `dev-sandbox.json` apply to newly created VMs after rebuilding.
Use `--cpus`, `--memory`, and `--disk` for per-VM resource choices during creation.
Change existing VM resources with Lima's own stopped-instance editing workflow. Run Ubuntu
security upgrades administratively as needed; package installation is not a
substitute for a guest patching policy.

`install-ssh` adds an Include to `~/.ssh/config`, backs up that file before
changing it, and stores a small SSH entry under `~/.ssh/dev-sandbox/`. The entry includes
Lima's own SSH configuration and defaults to `dev`; `root@` overrides the user. It refuses
to overwrite an unrelated generated-file target or rewrite a symlinked SSH
config. `ssh-config` prints the entry instead if you manage SSH configuration
through your own dotfiles tooling.
Create fresh VMs for the Ubuntu 26.04 recipe with `dev` as the primary user.

### Isolation and validation limits

* Plain mode disables host filesystem mounts, SSH-agent forwarding, automatic
  port forwarding, and bundled containerd. Use explicit SSH tunnels for previews,
  for example `ssh -N -L 3000:127.0.0.1:3000 lima-dev`.
* Linux protects `/root` from `dev`; the embedded managed Codex policy additionally
  denies common sensitive paths, permits workspace writes and direct networking,
  and disables apps, plugins, browser/computer use and configured MCP servers.
* Ubuntu 26.04 supplies the bubblewrap AppArmor profile. No custom profile is
  installed; Ubuntu's global user-namespace restriction stays enabled, and Codex
  applies its own filesystem sandbox. Older Ubuntu releases are not supported.
* Managed policy and installed tools are root-owned; development credentials and
  startup hooks are dev-owned. root SSH permits public-key authentication only;
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

The host CLI uses `go-toml/v2` to parse custom configuration; the Linux verifier
uses `golang.org/x/sys` for filesystem access checks. Versions and checksums are
recorded in `go.mod` and `go.sum`. The build toolchain is pinned in `mise.toml`,
which local development and GitHub Actions both use; `go.mod` records the minimum
supported Go version. Builds use Bash and gzip; tests use Go, Git, Bash, and
OpenSSH with synthetic data and temporary directories.

`tests/environment-linux.sh` additionally checks credential loading on Ubuntu
26.04 with Bash/Zsh interactive and noninteractive SSH, inherited child
environments, the packaged `gh`, and isolation from root and another user. Run it as root in a test VM:
`bash tests/environment-linux.sh lima/provision.sh`. It uses private mount,
network, and PID namespaces, so real credentials, accounts, and SSH configuration
are not changed. It requires the recipe's packages, `ip`, `unshare`, and overlayfs.

```sh
mise run check                         # Go tests/vet, verifier tests, Bash syntax
mise run build                         # .build/dev-sandbox, recipe and guest verifiers embedded
mise exec -- go test -race ./...
mise run release v0.1.0                 # four OS/CPU archives and checksums
# Supply the actual hosting repository to also generate a Homebrew formula:
mise run release v0.1.0 OWNER/REPO
```

Run `mise trust` and `mise install` when setting up a checkout. If mise is
[activated in your shell](https://mise.jdx.dev/getting-started.html#activate-mise),
you can omit `mise exec --` for direct Go commands. Tasks use `mise run` either way.
After changing the Go pin in `mise.toml`, run
`mise install` again. Mise sets `GOTOOLCHAIN=local` so Go uses the pinned compiler
instead of automatically downloading a newer toolchain.

`mise run` defaults to `build`. To embed a custom host version, use
`VERSION=v0.1.0 mise run build`.

`mise run build`, `mise run test`, and `mise run check` first cross-compile and compress both
Linux guest verifiers. For direct `go build` or `go test` commands, run `mise run guest`
first and rerun it after changing verifier code. Generated files in `guestbin/`
are ignored by Git. Releases rebuild them before embedding them in each host binary.
Provisioning selects the guest architecture with `uname -m`; it installs no Ruby
or Go runtime. Existing environments need one `configure` run with the rebuilt
CLI before using its `verify` command, which now invokes the compiled verifier.

Release files are written to `.build/releases/VERSION/`. Archives contain the
executable, documentation, and dependency license notices. `CGO_ENABLED=0` keeps
builds free of C library dependencies on Linux. macOS binaries still use OS
libraries. Builds cover macOS/Linux on arm64/amd64; full VM acceptance has been
exercised on macOS arm64. Cross-compilation alone does not validate other hosts'
virtualization setup.

GitHub Actions runs checks on macOS and Linux. Pushing a `vX.Y.Z` tag runs checks,
builds the four archives and Homebrew formula using the hosting repository's name,
and creates a **draft** GitHub release. Review and publish the draft, then copy
its generated `dev-sandbox.rb` into `Formula/dev-sandbox.rb` in your Homebrew tap. Users
can then install with `brew install OWNER/TAP/dev-sandbox`; the formula depends on
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

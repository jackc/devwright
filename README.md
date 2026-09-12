# Devwright

Devwright is a Go binary that creates a project's development VM from **native
Lima configuration**, copies local project files or fetches a Git checkout, installs your personal dotfiles,
and prompts for the project's credentials. Runtime requirements are Lima 2.2+,
OpenSSH, and Bash. Git is needed only for repository sources or dotfiles repositories.
No Ruby, Python launcher, Ansible, Go compiler, or guest
verifier is needed to use a released binary.

The project owns its VM recipe. Significant setup changes mean creating a new VM.
Lima owns VM configuration, provisioning, readiness, and lifecycle.

## Start with a local project

```sh
cd my-project
devwright init
# Edit .devwright/lima.yaml and its scripts. Git is not required.
devwright create my-project --from .
ssh lima-my-project
cd ~/projects/my-project
```

`init` refuses to overwrite an existing `.devwright` directory. Its starter recipe
provides Ubuntu 26.04, current Codex and Claude Code, managed agent defaults, a
non-sudo `dev` account, and key-only root SSH. It enables automatic application
port forwarding to the host's localhost while disabling host mounts, SSH agent
forwarding, and bundled containerd. Project recipes can change these choices.
Packages are not version-pinned.

An app listening on port 3000 inside the VM is available at `http://localhost:3000`
on the host, provided that host port is free. Lima manages forwarding automatically;
no separate SSH tunnel is needed. Forwarding binds to host loopback by default,
so it does not publish the app to the LAN. Use native Lima `portForwards` rules to
remap or exclude ports.

This default applies to newly initialized recipes. For an existing VM, stop it,
run `limactl edit NAME`, set `plain: false`, and retain `mounts: []`,
`ssh.forwardAgent: false`, and both `containerd.system: false` and
`containerd.user: false`, then start it again. Update the project's
`.devwright/lima.yaml` too so future VMs use the same settings.

Choose the development account with `user.name`, `user.home`, and `user.uid` in
`lima.yaml`. The starter home defaults to `/home/{{.User}}`; its system script
uses Lima's `{{.User}}` and looks up that account's home and primary group.
Devwright uses the resolved Lima account for project files, personal dotfiles,
credentials, and the SSH alias.

The launcher does not enforce the starter template's agent policies on other
recipes. There is no `verify` or `configure` command, and no verifier executable is
installed. Native Lima readiness probes determine whether project setup succeeded;
the launcher also checks that Bash and a writable guest home are available.
Custom recipes need a Linux guest with those tools, cloud-init with JSON status
output, and a configured SSH user. On resume, `finish` checks cloud-init errors
and reruns the saved readiness probes without rerunning provisioning scripts.

Local `--from` directories are copied as they are, including uncommitted, untracked,
hidden, and ignored files. No Git commands run and `.gitignore` is not interpreted.
Git metadata (`.git` files/directories) is omitted. File permissions and symlinks
are preserved; symlinks are not followed. Special files such as sockets are rejected.
The guest receives an independent directory, not a host mount or ongoing sync.

The checkout directory is independent of the VM name. For a local `--from`,
Devwright uses the source directory's basename after resolving the path. For a
Git URL, it uses the final repository path component with `.git` removed.
Case, underscores, dots, and hyphens are preserved:

```sh
cd full_stack_payments
devwright create fsp-dev-vm --from .
ssh lima-fsp-dev-vm
cd ~/projects/full_stack_payments
```

Use `--project-name full_stack_payments` to choose a different directory name,
including when copying from a temporary staging directory. Project names must
be 1–255 ASCII letters, digits, dots, underscores, or hyphens, starting with a
letter or digit; other source names require an explicit valid override. The
chosen name is saved with the VM so `finish` can resume without the source.
Existing VMs created before this option retain `~/projects/VM-NAME`.

## Create from a repository

```sh
devwright create my-project \
  --from git@github.com:team/my-project.git \
  --ref main \
  --dotfiles git@github.com:me/dotfiles.git
```

The host's normal Git authentication fetches the project and dotfiles. The selected
project revision supplies `.devwright/lima.yaml`; use `--recipe path/to/lima.yaml`
for another location. `--ref` accepts a branch, tag, or available commit. Without
it, the remote repository's HEAD is selected. An explicit `--ref` on a local Git
checkout also selects the Git workflow, copying only that revision's committed
files and recipe. A local directory without `--ref` always uses direct file copying.

The binary transfers Git bundles over SSH, separately from Lima configuration.
This avoids Lima's template size limit for repository history. The guest gets a
normal working checkout in `~/projects/PROJECT-NAME`, with its original remote URL when
available. Host private keys, Git configuration, hooks, and agent sockets are not
copied. Submodule repositories and Git LFS objects are not bundled; fetch them
inside the guest with its own credentials. Subsequent private Git operations also
need guest credentials or sign-in. Only run recipes and dotfiles you trust.

## Project files

| File | Purpose |
| --- | --- |
| `.devwright/lima.yaml` | Actual Lima settings, non-secret `env`, provisioning, and readiness probes |
| `.devwright/setup-system.sh` | Guest root setup, referenced by Lima |
| `.devwright/setup-user.sh` | Guest user setup, referenced by Lima |
| `.devwright/config/` | Starter agent policies and settings, referenced by Lima data provisioners |
| `.devwright/credentials.yaml` | Optional credential names and prompt descriptions |
| `.devwright/setup-project.sh` | Optional Bash hook run by devwright after checkout, dotfiles, and credentials |

Scripts and configuration referenced by Lima are resolved and saved by Lima at
creation. The starter scripts have completion markers: successful restarts skip
setup; partial failures retry. Custom scripts must implement their own retry and
restart behavior. Provisioning has no project checkout or personal credentials;
use `setup-project.sh` for commands such as `bundle install` that need them.

The project hook runs as the development user in `~/projects/PROJECT-NAME`, with the
credential loader sourced. It runs once successfully; `finish` retries a failed
hook. It must tolerate partial completion. It is saved at creation, so edits to
its original file don't change an existing VM.

The recipe also works directly with Lima:

```sh
limactl start --name=my-manual-vm .devwright/lima.yaml
```

That performs native provisioning only. Devwright's checkout, dotfiles, credential
prompts, project hook, and SSH alias are additional onboarding conveniences.
Lima's external `file` provisioning references remain an experimental Lima feature.

## Personal dotfiles

Dotfiles are personal settings, independent of the project's recipe. Supply
`--dotfiles URL`, or set a default in `~/.config/devwright/config.yaml`
(`$XDG_CONFIG_HOME/devwright/config.yaml` when set):

```yaml
dotfiles_repo: git@github.com:me/dotfiles.git
dotfiles_install: install
dotfiles_root: false
```

CLI flags override defaults. `--no-dotfiles` skips them. The default installer is
an executable `install` in the repository root; select another with
`--dotfiles-install scripts/install`. By default, Devwright invokes the installer
once as the development user selected by Lima, from a private checkout at
`~/.local/share/devwright/dotfiles`.

`--dotfiles-root` (or `dotfiles_root: true`) instead invokes the installer **once
as root**. The installer owns the sequence of system and personal setup. It can
install packages and then switch to the development user with `runuser`; Devwright
does not invoke it a second time or grant the development user sudo privileges.
The recipe must enable root SSH. Root execution remains optional and defaults
to false; the installer need not change root's personal configuration.

Privileged installation uses one persistent checkout at
`/usr/local/share/devwright/dotfiles`, owned by root and readable by the development
user's primary group. The group cannot modify it, and other users have no access
unless they share that group. Keep generated files in the user's home, not in this
checkout. Both system setup and user setup can read its files, and shell startup
files may continue sourcing them after installation.

Either execution mode receives these variables from the resolved Lima config:

| Variable | Value |
| --- | --- |
| `DEVWRIGHT_USER` | Development username, even when the installer runs as root |
| `DEVWRIGHT_HOME` | Development user's home directory |
| `DEVWRIGHT_UID` | Development user's numeric UID |

`HOME` and `USER` belong to the account executing the installer. A single entry
point can support both modes:

```bash
#!/bin/bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
if [[ $(id -u) -eq 0 ]]; then
  bash scripts/install-system
  exec runuser -u "$DEVWRIGHT_USER" -- \
    env HOME="$DEVWRIGHT_HOME" /bin/bash scripts/install-user
fi
exec bash scripts/install-user
```

Onboarding SSH commands use fresh connections with multiplexing disabled, so
login-shell and group changes made by provisioning take effect on subsequent
connections. They do not leave a shared connection for interactive SSH logins.

The system script can select the user's shell using `$DEVWRIGHT_USER`. The user
script runs with the selected account's identity and home. The checkout stays
root-owned, and the VM's sudo policy is unchanged.

One completion marker is written only after the entire installer succeeds,
including any user setup it waits for. `finish` retries the entire installer after
failure and skips it after success, so it must tolerate partial completion.
Restarts and `finish` use the saved repository snapshot and execution mode;
changing host preferences does not change an existing VM's setup.

## Credentials and recovery

Declare required variables beside the Lima recipe:

```yaml
- name: GH_TOKEN
  description: GitHub token scoped to this project's repositories
```

Values are prompted without echo, sent through SSH stdin, and saved as private
mode-0600 files inside the guest. Values never enter the recipe or saved host
onboarding state. Shell startup hooks load them; separately started services need
their own environment configuration. The development user and root can read them.

```sh
devwright finish my-project                 # Resume onboarding after interruption
devwright credentials my-project           # Supply missing declared variables
devwright credentials my-project --replace # Rotate declared variables
```

Missing credentials in an unattended session leave the VM available and return an
error. `finish` uses the saved directory archive or Git bundles, hook, and declarations under the Lima
instance's `devwright/` directory; the original checkout is unnecessary. It skips
successful dotfiles and project setup. Reconnect shells after rotating credentials.
Agent account sign-ins, such as `codex login --device-auth` and Claude's `/login`,
remain inside the guest.

Lima provisioning failures stop creation. Fix a transient cause and restart the
VM to retry the saved recipe. If a script itself needs changing, create another
VM. Keep the old VM until important user data has been transferred.

```sh
limactl stop my-project
limactl start my-project
limactl delete my-project  # Deletes this VM and its saved onboarding data
```

Devwright installs aliases in `~/.ssh/devwright/` and preserves existing SSH
configuration, making a backup when adding its Include line. Remove a deleted
VM's alias file yourself. Root access, when enabled by the recipe, uses
`ssh root@lima-my-project`.

Creation accepts Lima resource overrides `--cpus`, `--memory`, `--disk` (sizes in
GiB), plus repeatable `--param` and `--set`. These are non-secret Lima options.
Lima's global configuration applies normally; review it alongside the project
recipe when changing isolation settings.

## Build and development

```sh
mise run build  # Builds .build/devwright; no guest artifacts required
mise run test
```

See [DEVELOPMENT.md](DEVELOPMENT.md) for tests and release details. The prior
multi-backend implementation remains in source for regression reference; it is
not imported by the new binary. [LEGACY.md](LEGACY.md) describes that historical
CLI, not the current commands. Checkpoints `c8cfe1b` and `1717467` preserve the
Ansible and Ruby experiments, respectively.

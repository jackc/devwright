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
non-sudo `dev` account, and key-only root SSH. It disables host mounts, SSH agent
forwarding, and automatic application port forwarding. Project recipes can change
these choices. Packages are not version-pinned.

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
normal working checkout in `~/projects/NAME`, with its original remote URL when
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

The project hook runs as the development user in `~/projects/NAME`, with the
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
`--dotfiles-install scripts/install`. Installation uses a private checkout at
`~/.local/share/devwright/dotfiles` and runs as the development user. Explicit
`--dotfiles-root` additionally installs a separate checkout as root; the recipe
must enable root SSH for that option. Successful installers are not rerun by
`finish` or restarts.

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

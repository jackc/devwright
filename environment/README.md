# Lima development recipes

Create a complete Ubuntu development VM from a checked-in recipe. **Significant
configuration changes mean creating a new VM.** There is no Ansible, Python
launcher, virtual environment, or configuration-in-place command.

Host tooling uses Ruby's standard library, Lima 2.2+, OpenSSH, and Git when
installing dotfiles. Guest provisioning uses Bash and ordinary Ubuntu utilities.
The macOS system Ruby 2.6 is supported; a separate Ruby installation is unnecessary.
Ubuntu's own cloud-init implementation may use Python internally, but our tooling
does not require a Python installation or any Python packages on the host.

## Getting started

Install Lima (`brew install lima` on macOS). Linux hosts also need Ruby. From
this checkout, build the shared acceptance-verifier artifacts once:

```sh
mise install
mise run guest
```

That build uses Go. Neither a Go compiler nor the devwright executable is needed
at runtime when the prebuilt `guestbin/verify-linux-{arm64,amd64}.gz` files are
already present. No Ruby gems are required.

Edit `environment/config.yaml`, or copy it to a project-specific recipe:

```sh
environment/dev create my-dev --config ./project-environment.yaml
ssh lima-my-dev
```

Defaults are 4 CPUs, 4 GiB RAM, and a 60 GiB disk. Override them at creation with
`--cpus 8 --memory 8 --disk 100`, or edit `environment/lima.yaml`.

Creation renders the recipe into a native Lima `mode: system` provisioner,
creates a fresh instance, checks for unexpected effective Lima overrides, starts
it, waits for provisioning, transfers the verifier over SSH, verifies through a
real dev SSH session, installs an SSH alias, and prompts for missing credentials.
Existing instance names are refused, never replaced or deleted.

The VM retains the current devwright setup: Ubuntu 26.04, non-sudo `dev`, separate
key-only root administration, current Codex and Claude Code, managed policies,
AppArmor compatibility, optional dotfiles, private credentials, and projects.
There are no host mounts, host SSH agent forwarding, or automatic application
port forwarding. Guest outbound networking remains available. Use SSH tunnels
for application ports.

## Creation recipe

`config.yaml` contains package names, non-secret environment variables, optional
policy/config paths, dotfiles settings, project scripts, and credential declarations:

```yaml
packages: [postgresql, libpq-dev]
environment:
  APP_ENV: development

dotfiles_repo: https://github.com/OWNER/dotfiles.git
dotfiles_install: install

system_script: setup/system.sh
user_script: setup/user.sh

credentials:
  - name: GH_TOKEN
    description: GitHub token scoped to this project's repositories
```

Paths are relative to the recipe file. Both scripts are optional Bash scripts:
`system_script` runs as root after the base setup; `user_script` runs as dev with
the declared non-secret environment. The user script starts from a minimal
environment; the system script inherits the root provisioning environment.
Both run inside the VM, not on the host. Extra packages install before dotfiles.
Scripts should tolerate a retry after partial failure. No credential values are
available to provisioning by default; interactive onboarding follows setup.

Optional `codex_requirements`, `codex_config`, `claude_managed_settings`, and
`claude_config` paths select initial policies and personal settings. Empty paths
use this repository's defaults. JSON files are checked for valid objects and Bash
scripts for syntax on the host; policy schemas and TOML are checked by the guest
verifier after installation. There are no reset/replace options for existing VMs.

For dotfiles, the host fetches the repository's default branch using normal Git
authentication and bundles committed history into the saved creation recipe.
Independent root/dev checkouts run their own installer. Host Git configuration,
credentials, and agent sockets are not transferred. Submodule and Git LFS contents
are not bundled. Only select dotfiles and scripts you trust to run as root.

## What happens on restart

Lima calls system provisioners on every boot. Our provisioner checks the root-owned
`/usr/local/share/devwright/lima-setup-complete` marker and immediately exits when
setup has already succeeded. Successful restarts do not reinstall packages,
update agents, overwrite configuration, or rerun project scripts.

The instance stores a snapshot of the recipe's scripts, configuration, and dotfiles.
Editing the original source files does not change it. Apt versions remain unpinned;
there is no claim of identical package versions across new VMs, and this recipe
does not add a new automatic-update policy.

```sh
limactl stop my-dev
limactl start my-dev
environment/dev verify my-dev
```

For a significant change, edit the recipe and create another VM with a new name:

```sh
environment/dev create my-dev-v2 --config ./project-environment.yaml
```

Bring over the projects or data you need explicitly. Creation does not migrate
uncommitted files, databases, credentials, or account sign-ins. Keep the old VM
until you have verified the replacement and transferred needed data.

## Credentials and sign-in

The saved recipe records credential **names and descriptions**, never values.
After setup, hidden prompts collect missing values and transfer them over SSH
stdin. Values are stored as dev-owned mode-0600 files under
`~/.config/devwright/credentials.d/`, in a mode-0700 directory. The existing
`credentials.sh` shell loader sources these files after manual assignments and
the declared non-secret environment. Each rotation replaces that variable's file.

```sh
environment/dev credentials my-dev
# Prompt again for every declared credential:
environment/dev credentials my-dev --replace
```

These commands use the instance's saved declarations; the original recipe file is
not needed. Existing nonempty variables are skipped unless `--replace` is passed.
An unattended creation with missing required credentials exits unsuccessfully
with the missing variable name, leaving the configured VM available to finish.
No values go into host files, process arguments, or the saved Lima configuration.
All processes running as dev can read dev's credentials, and root retains normal
administrative access. Reconnect shells and remote runtimes after rotation.

Complete agent sign-ins inside the guest as before:

```sh
ssh lima-my-dev
codex login --device-auth
claude  # complete /login
```

Bash/Zsh SSH shell hooks load the environment. Independently started services
still need their own environment configuration. Browser login and real service
credential authorization are not automated by the verifier.

## Recovery and inspection

If first-boot provisioning fails, no completion marker is written. Fix external
causes such as a temporary download outage and restart the VM to retry the same
saved recipe. If the recipe itself needs changing, create a fresh VM from the
corrected source. To inspect logs, use root SSH after bootstrap has established it:

```sh
ssh root@lima-my-dev 'tail -100 /var/log/cloud-init-output.log'
```

If provisioning succeeded but verifier transfer or onboarding was interrupted:

```sh
environment/dev finish my-dev
```

`finish` requires the completion marker, installs the verifier, verifies the VM,
installs the alias, and finishes credential prompts. It does not reconfigure the
VM or update development tools. `verify` only checks an already completed VM.

Preview the complete saved Lima recipe:

```sh
environment/dev render preview --config ./project-environment.yaml > /tmp/recipe.yaml
limactl validate /tmp/recipe.yaml
```

Lima enforces a 4 MiB template limit. Compiled verifiers are transferred separately
to stay within that limit. A large dotfiles history can still exceed it; keep the
recipe small and fetch large dependencies inside guest scripts. Rendered recipes
contain selected dotfiles source/history and configuration, so treat them as
private when those inputs are private.

The launcher checks the effective VM settings and saved provisioner checksum,
including after template composition. SHA-256 identifies the selected script; it
is not an authenticity guarantee against someone who can edit the host's Lima state.
Use the launcher for creation; invoking the base `lima.yaml` directly only creates
an unconfigured bootstrap VM. The base file deliberately has no setup payload.

## Files

| File | Purpose |
| --- | --- |
| `lima.yaml` | Base VM resources, OS, account, and isolation settings |
| `config.yaml` | Project creation recipe and credential declarations |
| `provision.sh` | First-boot completion/retry guard |
| `setup-user.sh` | Bash environment and credential-directory loader |
| `dev`, `launcher.rb` | Standard-library Ruby renderer, Lima/SSH orchestration, prompts |
| `../lima/*.sh`, `../config/` | Shared existing guest setup, dotfiles, credential hooks, policies |
| `../guestbin/verify-linux-*.gz` | Existing acceptance verifier, transferred after boot |

The shared Bash recipe remains the implementation of the guest setup. The old
Go CLI retains its original embedded-verifier behavior; this experiment disables
only that embedded-verifier step and transfers the same artifact afterward.
Ansible/Python implementation files were removed; checkpoint `c8cfe1b` retains
the previous experiment for comparison. Existing Ansible-created VMs are not
adopted by this launcher because they lack a saved native creation recipe.

## Tests

Validated September 8, 2026 on macOS arm64 with Lima 2.2.0, system Ruby 2.6,
Ubuntu 26.04 arm64, Codex 0.153.4, and Claude Code 2.1.263:

- Nine host tests passed with both system Ruby 2.6 and Ruby 4.0; the boot guard
  retries failed setup and skips completed setup.
- Default and custom fresh VMs passed the existing guest acceptance verifier.
- Custom apt packages, policies, root/dev dotfiles, system/user scripts, literal
  environment values, and synthetic credential rotation passed.
- Missing-credential onboarding resumed with `finish` after installing the value.
- Editing source recipes/scripts and restarting left the saved recipe unchanged;
  setup ran only once, user data survived, and the installed SSH alias worked.
- Existing Go regression tests passed after making embedded verifier installation
  optional in the shared Bash recipe.

Linux hosts and x86_64 guests have not received an end-to-end run of this version.

```sh
ruby environment/test_launcher.rb
bash -n environment/provision.sh environment/setup-user.sh lima/provision.sh
# Creates a disposable VM and tests custom setup, credentials, and restart:
ruby environment/acceptance.rb native-parity-test
```

The acceptance test leaves its VM and SSH alias for inspection. Stop the VM with
`limactl stop native-parity-test` when finished. It uses synthetic credentials,
not real account authentication. The existing verifier measures selected policy
behavior, including intentionally unrestricted behavior; it does not certify
network allowlists, desktop runtimes/connectors, or private repository scope.

# Lima + Ansible development environment

This experiment replaces the devwright provisioning executable with Lima,
Ansible, and a checked-in recipe. It creates the same Ubuntu 26.04 environment:
non-sudo `dev`, separate key-only root SSH, current Codex and Claude Code,
managed agent policies, optional dotfiles, private credentials, and the existing
acceptance verifier. It currently supports **Lima Linux VMs only**.

## Install host dependencies

Install Lima 2.2+ and Python 3.11+ (for example `brew install lima python`). From
this repository, create a local Ansible environment and build the verifiers:

```sh
python3 -m venv .build/ansible
.build/ansible/bin/pip install -r environment/requirements.txt
mise run guest
```

The last command uses the repository's Go toolchain. Go is needed to build the
shared acceptance verifier, not to run the launcher or provision the machine.
There is no dependency on the devwright CLI or any external Ansible collection.
Run `mise install` first if the repository's toolchain is not installed.

## Create and configure

Choose a fresh instance name; creation refuses existing names:

```sh
environment/dev create my-dev
ssh lima-my-dev
```

Creation validates the effective Lima configuration, boots the VM, establishes
root administration, runs Ansible, verifies through a real dev SSH session,
installs the host SSH alias, and prompts for missing declared credentials.

The recipe is `environment/config.yaml`. To use a project-specific recipe, copy
it to another file and edit it, then supply it on every configuration run:

```sh
environment/dev create my-dev --config ./project-environment.yaml --cpus 8 --memory 8 --disk 100
environment/dev configure my-dev --config ./project-environment.yaml
environment/dev verify my-dev
```

Memory and disk CLI values are in GiB. `configure` starts a stopped VM, reapplies
the recipe, updates the agents, and verifies it. Run configuration while development
tools are idle. `verify` checks a running VM without installing or updating tools.
A failed configuration can be retried with `configure`, including a run interrupted
after bootstrap access was revoked. Failed VMs are left intact for diagnosis.

The launcher uses plain Lima commands for lifecycle. No setup runs on reboot:

```sh
limactl stop my-dev
limactl start my-dev
ssh root@lima-my-dev
```

Ansible owns the guest account and service configuration. Edit the task files to
add services or other system setup; add ordinary apt packages and non-secret
variables in the recipe:

```yaml
packages: [postgresql, libpq-dev]
environment:
  APP_ENV: development
  PGHOST: localhost
```

Package versions are unpinned. Configuration installs current listed apt packages
and current agent releases; it does not promise identical versions or configure
a new system-wide automatic-update policy. Existing projects, credentials, and
personal agent settings are preserved unless replacement is explicitly requested.

## Credentials and sign-in

Declare required values in the recipe, never the actual secrets:

```yaml
credentials:
  - name: GH_TOKEN
    description: GitHub token scoped to this environment's repositories
```

After successful provisioning, the launcher checks only which declared variables
are nonempty in a fresh dev SSH environment and prompts for missing values with
hidden input. It sends values to the guest over SSH stdin, without putting them
in Lima configuration, host files, command arguments, or Ansible output.
Values go into dev's mode-0600 `~/.config/devwright/credentials.sh`. All processes
running as dev can read them; root retains normal administrative access.
Noninteractive runs with missing required credentials fail with the variable name;
the configured VM remains available to finish onboarding interactively.

```sh
environment/dev credentials my-dev --config ./project-environment.yaml
# Prompt again for all declared credentials:
environment/dev credentials my-dev --config ./project-environment.yaml --replace
```

Repeated rotation replaces the launcher's managed block for each variable.
Pre-existing manually written assignments are preserved; the managed assignment
at the end takes precedence. Existing processes keep old environment values, so
reconnect shells and remote runtimes after changes. Removing a declaration does
not revoke or delete a previously installed credential.

Account sign-ins remain interactive inside the guest:

```sh
ssh lima-my-dev
codex login --device-auth
claude  # complete /login
```

The launcher does not import personal host credentials or automate browser logins.
Non-secret variables are written separately to `environment.sh` and loaded through
the credential shell hooks. Those hooks cover Bash/Zsh SSH sessions; independently
started systemd services still need their own environment configuration.

## Dotfiles and agent overrides

`dotfiles_repo` is optional. The host clones its default branch using normal Git
authentication, transfers a temporary Git bundle, and runs `dotfiles_install`
(default `install`) independently as root and dev. Only supply a repository you
trust to run as root. Host Git configuration, credentials, and agent sockets are
not transferred. Submodule and Git LFS contents are not bundled. Subsequent runs
require the same repository and a fast-forward update, matching devwright.

Policy/config paths are relative to the selected recipe file:

```yaml
codex_requirements: policies/requirements.toml
claude_managed_settings: policies/managed-settings.json
codex_config: policies/config.toml
claude_config: policies/settings.json
```

A managed policy becomes the saved root-owned selection. Omitting its path on a
later run preserves that selection; set `reset_codex_requirements: true` or
`reset_claude_managed_settings: true` to restore the bundled defaults. Selecting
and resetting the same policy together is rejected.

Personal config files seed absent files only. Set `replace_codex_config: true`
or `replace_claude_config: true` together with the corresponding path to replace
an existing personal file. Replacement is explicit and overwrites the old file.
The launcher validates TOML/JSON and the bundled Claude user-settings schema
before VM operations; the guest verifier checks runtime policy behavior.

## Files and responsibility boundary

| File | Responsibility |
| --- | --- |
| `lima.yaml` | Image, CPU, RAM, disk, plain mode, host integration, initial account |
| `config.yaml` | Packages, environment, dotfiles, policy selections, credential declarations |
| `bootstrap.yaml` | Initial dev/sudo connection establishes root SSH and Python |
| `setup.yaml`, `tasks/` | Accounts, SSH policy, apt, agents, personal setup, verifier installation |
| `templates/policies.sh.j2` | Atomic policy selection, initial configs, legacy config migrations |
| `files/` | Focused AppArmor, Git, and environment helpers |
| `dev`, `launcher.py` | Effective-config checks, root handoff, Ansible invocation, SSH aliases, prompts |
| `../config`, `../lima/{bootstrap,dotfiles,credentials}.sh`, `../guestbin` | Shared existing policies, helpers, and verifier assets |

The experiment intentionally retains the existing `/usr/local/share/devwright`
and `~/.config/devwright` paths for verifier and configuration parity. It does
not call the old full `lima/provision.sh` or the Go CLI. The focused shell helpers
preserve tested migration and shell-startup behavior while Ansible modules own
ordinary account, package, directory, repository, and file setup.

Lima still generates its own instance configuration, SSH connection file,
identity, and disks outside this repository. No separate cloud-init or global
Lima configuration is needed. Ansible connects through Lima's generated SSH
configuration, with agent consultation/forwarding disabled and separate root/dev
multiplexing sockets. Installed aliases live in `~/.ssh/lima-environments/` and
the launcher backs up the main SSH configuration before adding its include.

The template starts dev with sudo. The launcher tries root first; if unavailable,
it bootstraps over dev/sudo, then confirms an independent root connection before
Ansible revokes dev's sudo. Bootstrap-only VMs are not yet restricted environments.

Plain mode preserves guest outbound networking and SSH but disables automatic
application forwarding. Use SSH tunnels for application ports. There is no network
allowlist. The launcher rejects unexpected effective host mounts, provisioning,
forwarding, proxy inheritance, or primary-account settings, including host-global
Lima overrides. Editing the source YAML does not change an existing instance.

## Validation

Validated on September 8, 2026 with Lima 2.2.0 on macOS arm64, Ubuntu 26.04
arm64 guests, Ansible Core 2.19.12, Codex 0.153.4, and Claude Code 2.1.263:

- Nine host tests and both playbook syntax checks passed.
- Fresh creation completed in one command and passed the existing guest verifier.
- An interrupted configuration resumed through the established root connection.
- Custom policy selection/preservation/reset and personal setting preservation/
  replacement passed, with projects and synthetic credentials retained.
- Host-bundled dotfiles ran independently as root and dev; hooks survived a
  dotfiles installer replacing the login profile with an early return.
- Extra packages, literal environment values, credential rotation, and restart
  passed. SSH through Lima's regenerated connection configuration survived its
  changed port after restart.

Linux hosts and x86_64 guests have not received this experiment's end-to-end run.

Run host tests and syntax checks:

```sh
.build/ansible/bin/python -m unittest discover -s environment -p 'test_*.py'
ANSIBLE_LOCAL_TEMP=/tmp/ansible-syntax .build/ansible/bin/ansible-playbook -i 'development,' environment/setup.yaml --syntax-check
ANSIBLE_LOCAL_TEMP=/tmp/ansible-syntax .build/ansible/bin/ansible-playbook -i 'development,' environment/bootstrap.yaml --syntax-check
limactl validate environment/lima.yaml
```

For destructive-to-test-state acceptance, create a **disposable** test VM:

```sh
environment/dev create ansible-parity-test
.build/ansible/bin/python environment/acceptance.py ansible-parity-test
```

Acceptance deliberately changes that VM's personal configuration and synthetic
credentials. It exercises custom policies, preservation, explicit replacement,
reset, dotfiles for both accounts, package installation, literal environment
values, credential rotation, and restart. It leaves the VM and its alias for
inspection. Stop it with `limactl stop ansible-parity-test` when finished.

The existing verifier measures account isolation and agent policy behavior,
including behavior that a selected policy intentionally leaves unrestricted.
Authenticated model calls, actual private-repository authorization, and
desktop-provided runtimes/connectors are outside these checks.

# Agent Sandbox Config

## Lima development VMs

This branch implements a reusable **Lima + Ubuntu 26.04 + Codex** environment.
`vmadmin` administers the VM with sudo. Humans and agents both develop as `dev`,
without sudo, in `/home/dev/projects`. Projects in one VM share that account's
files and credentials. Use another VM when they need separate access.

Requirements on the host: Lima **2.2+**, Ruby **3.1+**, and OpenSSH.
The Ruby orchestrator uses only standard libraries. Its tests use Minitest
(`gem install minitest` if it is not already installed). Provisioning installs Ruby
inside the guest for verification. All maintained scripts are Ruby or Bash.
Creation downloads an Ubuntu image and installs packages. Provisioning
installs the latest stable Codex release from npm. The OS image selection comes
from the installed Lima Ubuntu 26.04 image template. OS package versions are not pinned.

```sh
# Run from this repository on the host.
ruby scripts/vm.rb create agent-dev  # installs and verifies the setup
ruby scripts/vm.rb install-ssh agent-dev

ssh lima-agent-dev           # dev: development, Codex, repositories
ssh vmadmin@lima-agent-dev   # vmadmin: VM administration
```

Create another isolated environment with the same recipe:

```sh
ruby scripts/vm.rb create another-dev
ruby scripts/vm.rb install-ssh another-dev
```

`create` refuses an existing name. The launcher checks for the `vmadmin` account
and rejects host mounts, agent forwarding, non-plain mode, or boot provisioning.
It never falls back to running development commands on the host. Existing
`default-dev-vm` and `pgx-dev-vm` instances are not managed or modified.

### Everyday use

```sh
limactl start agent-dev
ssh lima-agent-dev          # development as dev
ssh vmadmin@lima-agent-dev  # administration
limactl stop agent-dev
```

Lima manages starting, stopping, and deleting VMs. SSH provides interactive access;
there are no corresponding Ruby wrapper commands. Our SSH entry includes Lima's
current connection file instead of copying its port, so a normal Lima restart
requires no SSH refresh. It disables agent forwarding, agent consultation, and
connection sharing, keeping the two users' sessions separate.

Restarting does not run our setup script or update Codex. Your VM's disk and
installed settings persist. Use `ruby scripts/vm.rb verify agent-dev` to recheck
the restrictions without updating tools.

### GitHub and Codex sign-in

Supply a fine-grained GitHub token for each VM interactively from the host:

```sh
ruby scripts/vm.rb set-token agent-dev
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
Set your Git author name/email as `dev` when needed; provisioning deliberately
does not copy your host Git configuration.

For the desktop, add **lima-agent-dev** as an SSH host in its remote connection
settings, then select a guest directory under `/home/dev/projects`. Use the
`dev` connection, not a connection with the username `vmadmin`. The app may
install a separate remote runtime; verify its version and effective managed
policy in a fresh task. Installation of the CLI does not authenticate the desktop.

### Change or update the setup

| File | Responsibility |
| --- | --- |
| `lima/agent.json` | Lima template (JSON is valid YAML): image base, resources, admin account, plain mode |
| `lima/provision.sh` | Repeatable OS, account, SSH, GitHub helper, and Codex installation |
| `config/codex/requirements.toml` | Root-owned, VM-wide managed restrictions |
| `config/codex/config.toml` | Initial dev defaults, preserved after first installation |
| `scripts/verify_guest.rb` | Credential-free Linux and sandbox acceptance checks |

Edit the shared source, then apply it to an existing VM. This also updates Codex
to the latest stable release. The version installed during provisioning is recorded
in `/usr/local/share/agent-vm/codex-version` for diagnostics; verification checks
actual policy behavior rather than requiring that exact version:

```sh
ruby scripts/vm.rb configure agent-dev  # applies the recipe and verifies it
```

`create` and `configure` send the current Bash setup script and embedded policy
files over SSH to `vmadmin`, which executes it with sudo. Lima stores no setup
script to replay on boot. Editing this repository takes effect on an existing VM
only when you explicitly run `configure`.

`configure` applies setup to a running VM without rebooting it; if stopped, it
starts the VM first. It installs the latest Codex, updates managed policy and
system settings, and runs the guest acceptance checks. It preserves `dev`'s
personal Codex config, credentials, and projects. Use it while development tools
are idle because it updates installed software and reloads SSH configuration.

Explicit provisioning restores policy, SSH settings, public login keys from
`vmadmin`, and `dev`'s empty supplementary group list. Manual changes to those
settings survive normal restarts but are overwritten by `configure`.
If setup fails, fix the cause and rerun `configure`; restarting does not retry it.
Full acceptance checks run during `create`, `configure`, and `verify`. Lima
startup does not run our verification.

Resource/image changes in `agent.json` apply to newly created VMs. Change existing
VM resources with Lima's own stopped-instance editing workflow. Run Ubuntu
security upgrades administratively as needed; package installation is not a
substitute for a guest patching policy.

`install-ssh` adds an Include to `~/.ssh/config`, backs up that file before
changing it, and stores a small SSH entry under `~/.ssh/agent-vms/`. The entry includes
Lima's own SSH configuration and defaults to `dev`; `vmadmin@` overrides the user. It refuses
to overwrite an unrelated generated-file target or rewrite a symlinked SSH
config. `ssh-config` prints the entry instead if you manage SSH configuration
through your own dotfiles tooling.
Create fresh VMs for the Ubuntu 26.04 and `vmadmin` recipe. Migrating VMs made
with earlier recipes, including boot provisioning, is not supported.

### Isolation and validation limits

* Plain mode disables host filesystem mounts, SSH-agent forwarding, automatic
  port forwarding, and bundled containerd. Use explicit SSH tunnels for previews,
  for example `ssh -N -L 3000:127.0.0.1:3000 lima-agent-dev`.
* Linux protects `vmadmin` and `/root` from `dev`; managed Codex policy additionally
  denies common sensitive paths, permits workspace writes and direct networking,
  and disables apps, plugins, browser/computer use and configured MCP servers.
* Ubuntu 26.04 supplies the bubblewrap AppArmor profile. No custom profile is
  installed; Ubuntu's global user-namespace restriction stays enabled, and Codex
  applies its own filesystem sandbox. Older Ubuntu releases are not supported.
* The helper and policy files are root-owned. `vmadmin` has passwordless guest sudo;
  do not expose an admin SSH connection or rootful Docker socket to agents.
* Anyone running as `dev` can modify that account's startup files, tools, and
  project code. Do not run such files as `vmadmin` with sudo. Guest policy controls
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

Local checks: `ruby tests/test_vm.rb`, `ruby tests/test_verification.rb`, and
`bash -n lima/provision.sh`. `create` also validates the generated Lima YAML.
See [VALIDATION.md](VALIDATION.md) for the actual VM test results and research provenance.

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

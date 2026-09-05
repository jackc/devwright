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
The first boot downloads an Ubuntu image and installs packages. Provisioning
installs the latest stable Codex release from npm. The OS image selection comes
from the installed Lima Ubuntu 26.04 image template. OS package versions are not pinned.

```sh
# Run from this repository on the host.
ruby scripts/vm.rb create agent-dev
ruby scripts/vm.rb verify agent-dev
ruby scripts/vm.rb install-ssh agent-dev

ssh agent-dev         # dev: development, Codex, repositories
ssh agent-dev-admin   # vmadmin: VM administration
```

Create another isolated environment with the same recipe:

```sh
ruby scripts/vm.rb create another-dev
ruby scripts/vm.rb verify another-dev
ruby scripts/vm.rb install-ssh another-dev
```

`create` refuses an existing name. The launcher refuses instances without this
recipe's marker and rejects host mounts, agent forwarding, or non-plain mode.
It never falls back to running development commands on the host. Existing
`default-dev-vm` and `pgx-dev-vm` instances are not managed or modified.

### Everyday use

```sh
ruby scripts/vm.rb start agent-dev
ruby scripts/vm.rb shell agent-dev   # same account as ssh agent-dev
ruby scripts/vm.rb admin agent-dev  # administrative session
```

Use this launcher's `start` command: it removes the host `SSH_AUTH_SOCK` from
Lima's environment and refreshes installed SSH aliases if Lima changes its port.
An ordinary `limactl start` does not refresh those aliases; run `install-ssh`
afterward if needed. The generated aliases disable SSH-agent consultation,
forwarding, and connection sharing so a `dev` login cannot reuse a `vmadmin` connection.
The host's other SSH connections keep their existing configuration.

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

Inside `ssh agent-dev`:

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

For the desktop, add **agent-dev** as an SSH host in its remote connection
settings, then select a guest directory under `/home/dev/projects`. Use the
`dev` alias, not `agent-dev-admin` or Lima's generated admin alias. The app may
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
ruby scripts/vm.rb configure agent-dev
ruby scripts/vm.rb verify agent-dev
```

**`configure` stops and restarts the VM, disconnecting active sessions.** It
updates the stored provisioning script before reboot, so subsequent boots use
the new setup. It updates managed policy and installed tools but preserves
`dev`'s Codex config and credentials. Resource/image changes in `agent.json`
apply to newly created VMs; `configure` updates provisioning only. Change existing
VM resources with Lima's own stopped-instance editing workflow.

Provisioning runs on each boot. It restores policy, SSH settings, the approved
public login keys from `vmadmin`, and `dev`'s empty supplementary group list. Add
system setup to the recipe rather than making changes you expect these steps
to preserve. Run Ubuntu security upgrades administratively as needed; package
installation is not a substitute for a guest patching policy.

`install-ssh` adds an Include to `~/.ssh/config`, backs up that file before
changing it, and stores generated aliases under `~/.ssh/agent-vms/`. It refuses
to overwrite an unrelated generated-file target or rewrite a symlinked SSH
config. `ssh-config` prints the aliases instead if you manage SSH configuration
through your own dotfiles tooling.
Create fresh VMs for the Ubuntu 26.04 and `vmadmin` recipe. Migrating VMs made
with earlier recipes is not supported.

### Isolation and validation limits

* Plain mode disables host filesystem mounts, SSH-agent forwarding, automatic
  port forwarding, and bundled containerd. Use explicit SSH tunnels for previews,
  for example `ssh -N -L 3000:127.0.0.1:3000 agent-dev`.
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

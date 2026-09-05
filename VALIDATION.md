# Lima/Codex validation

## Ruby orchestration port

Retested September 5, 2026 using Ruby 4.0.2 and Lima 2.2.0:

- Replaced `scripts/vm.py` with `scripts/vm.rb` and the host unit tests with
  Minitest. Bash provisioning and Python guest probes are unchanged except for
  the provisioning script's renderer-name comment.
- Parsed Ruby and Python rendered templates compared equal before that comment
  update. The generated template passed `limactl validate`.
- 11 host tests / 42 assertions passed, including actual subprocess argument
  preservation, child environment filtering, token transport using synthetic
  input, SSH-file migration and preservation, and configure command ordering.
- Ran Ruby `configure agent-dev`: stopped the VM, updated its stored recipe,
  booted it, checked provisioning completion, and refreshed its SSH aliases.
- Ran Ruby `verify agent-dev`: all Linux, app-server, filesystem sandbox, and
  conflicting-profile checks passed after reboot.
- Ran Ruby `start` on the running VM and `install-ssh` again; both succeeded.
  The resulting SSH aliases logged in as `dev` and `jack`; jack's sudo worked.
- Ruby syntax, Bash syntax, and diff whitespace checks passed.

The existing VM was reused; this port did not create another VM. Token prompting
was covered with synthetic unit-test input, without changing stored credentials.
The authentication, GitHub scope, and desktop-inventory checks listed below
remain user-dependent and were not performed by this port.

## Environment

Implemented and exercised on September 5, 2026:

- Host: macOS arm64, Lima 2.2.0, VZ.
- New instance: `agent-dev`; Ubuntu 24.04 arm64, 4 CPUs, 4 GiB RAM,
  60 GiB sparse disk, plain mode.
- Accounts: `jack` (administrator), `dev` (shared unprivileged development).
- Codex CLI: 0.149.0 installed from the official npm package, root-owned.
- Existing `default-dev-vm` and `pgx-dev-vm` were not changed.

## Checks and findings

The maintained acceptance command is `ruby scripts/vm.rb verify agent-dev`.
It uses SSH as `dev`, no model invocation, no real credentials, and a synthetic
`.pgpass` that is removed afterward. It refuses to overwrite a preexisting file.

| Check | Result |
| --- | --- |
| Lima template validation | Passed with installed Lima 2.2.0 |
| Host unit tests | Passed: foreign/unsafe VM refusal, host credential environment removal, rendering, SSH identity separation |
| Python compilation, Bash syntax, diff whitespace | Passed |
| Login as dev; no sudo or extra groups | Passed |
| `/home/jack` and `/root` inaccessible to dev | Passed |
| Root-owned policy and tool directories unwritable by dev | Passed |
| No host filesystem sharing or forwarded host agent socket | Passed |
| Codex installation and app-server managed profile discovery | Passed |
| Apps/plugins remain disabled despite CLI enable overrides | Passed via resolved `codex features list` |
| Workspace writes permitted by Codex sandbox | Passed |
| Writes outside the workspace denied | Passed |
| Synthetic protected-file read denied | Passed |
| Conflicting local definition of managed profile | Rejected before command execution |
| Repeated provisioning and reboot persistence | Exercised; stored provisioning updated before reboot |
| Generated dev/admin SSH aliases after port change | Passed: dev and jack respectively; jack sudo succeeds |
| Global AppArmor user-namespace restriction | Still enabled (`kernel.apparmor_restrict_unprivileged_userns = 1`) |

`config/read` exposes raw requested feature settings, including CLI overrides.
It is **not** a substitute for checking resolved feature flags. The app-server's
`configRequirements/read` returned the managed feature requirements, and
`codex features list` showed the requested apps/plugins overrides resolved to false.

The first creation exposed a root-owned `.config` parent directory; provisioning
now explicitly creates it with dev ownership. Lima's READY message was not proof
of successful provisioning, so the launcher also checks a root-owned completion
marker that is removed before provisioning and recreated only on success.

Ubuntu initially denied user-namespace creation, preventing Codex's bubblewrap
sandbox from starting. Installing the system bubblewrap alone was insufficient.
After explicit user approval, the VM-only AppArmor profile in
`config/apparmor/agent-vm-bwrap` was installed and the sandbox checks succeeded.
Ubuntu's global unprivileged-user-namespace restriction was not disabled.

## Remaining user-dependent checks

- Codex login and an authenticated CLI task.
- GitHub HTTPS operations with the user's scoped token: an allowed private
  repository succeeds and a disallowed private repository fails.
- Desktop SSH connection and its independently installed runtime version.
- A fresh desktop task's complete tool inventory, including host-provided
  integrations and cross-task tools. VM filesystem tests cannot establish this.

No GitHub token, personal SSH key, personal Codex login state, or host dotfiles
were copied. No Claude installation or configuration was performed.

## Research incorporated

Read the other branches without importing their experimental settings wholesale:

- `consolidated-findings-a`, commit `11493ff`: `FINDINGS.md`, `OPTIONS.md`,
  `LIMA-STARTUP.md`. Incorporated the domain boundary, credential-free recipe,
  separate client-tool acceptance gate, and host SSH-agent startup workaround.
- `consolidated-findings-b`: Codex candidate templates. Replaced their outdated
  requirements shape with a managed profile table and centrally defined profile.

The user chose direct networking, shared human/agent development as `dev`, and
administration as `jack`. Earlier recommendations for per-project settings,
domain proxies, or Claude configuration are not this implementation's scope.

## Reference documentation

- [Lima 2.2.0 configuration schema](https://github.com/lima-vm/lima/blob/v2.2.0/templates/default.yaml)
- [Lima plain mode](https://lima-vm.io/docs/config/plain/)
- [Lima SSH access](https://lima-vm.io/docs/usage/ssh/)
- [Codex managed configuration](https://learn.chatgpt.com/docs/enterprise/managed-configuration)
- [Codex remote connections](https://learn.chatgpt.com/docs/remote-connections)
- [Ubuntu 24.04 release notes: user-namespace restrictions](https://documentation.ubuntu.com/release-notes/24.04/)

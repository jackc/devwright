# Lima, Incus, and Codex validation

## Incus backend: Linux host and unprivileged containers

Tested September 5, 2026 on an Apple M3 Max running macOS, with Lima 2.2.0/VZ.
A disposable `agent-incus-test` Lima VM ran Ubuntu 26.04 arm64, kernel
7.0.0-28-generic, 6 vCPUs, and 8 GiB memory, with nested virtualization enabled
and no host filesystem mounts. Ubuntu's Incus 6.0.5 and QEMU 10.2.1 packages
were installed there. Tests ran the Linux executable as the host's ordinary
`jack` account with membership in `incus-admin`, without Lima installed inside
the Linux host.

Incus used a 70 GiB loop-backed Btrfs pool and a private NAT bridge. Automatic
bridge subnet discovery failed inside Lima, so test setup explicitly selected
10.203.79.1/24 with IPv6 disabled. This was test-host initialization, not a
change to `agent-vm`'s recipe or to existing development VMs.

Created `nested-container` with 2 CPUs, 2 GiB memory, and a 12 GiB disk limit;
then created `final-container` using the default 4 CPUs, 4 GiB memory, and
60 GiB disk limit. Both used `images:ubuntu/26.04`, no profiles, unprivileged
isolated UID/GID mappings, and nested namespaces. Both completed creation,
SSH bootstrap, installation of Codex 0.153.4, and the full guest acceptance
suite. No AppArmor restrictions were disabled or custom profiles installed.

The tested commands include:

```sh
agent-vm create nested-container --backend incus --container \
  --cpus 2 --memory 2GiB --disk 12GiB
agent-vm create final-container --backend incus --container
agent-vm install-ssh nested-container --backend incus
agent-vm verify nested-container --backend incus
agent-vm set-token nested-container --backend incus
incus --force-local --project default stop nested-container
agent-vm configure nested-container --backend incus
```

Acceptance confirmed:

- Real SSH sessions select `dev` and `root` correctly. `dev` has no sudo,
  supplementary groups, access to root's home, or forwarded host SSH agent.
- The actual Codex sandbox permits workspace writes and denies writes outside
  it and reads of the synthetic protected file. Conflicting permission-profile
  overrides are rejected, and managed apps/plugins settings hold.
- Repeated `install-ssh` preserves the host SSH config. The generated entry
  still works after container stop/start without refreshing addresses or ports.
- The real terminal prompt accepts a synthetic token without echoing it; the
  token arrives through SSH stdin and is stored with mode 0600. The synthetic
  token was removed afterward; no actual GitHub credentials were used.
- Stop/start preserves dev project files, personal Codex configuration, and a
  deliberate policy-drift canary. `verify` detects that policy drift.
- `configure` starts the stopped container, restores the managed policy, and
  passes all acceptance checks while preserving projects, personal config,
  token contents, the host SSH identity/pinned host key, and root's authorized
  keys.

Live testing exposed a boot race: Incus reports running processes before
systemd's control socket and SSH are ready. The launcher now waits for guest
process reporting, systemd's socket, and completion of the boot transaction
before using SSH. The wait is bounded and cancelable. The stopped-container
configuration and fresh default-resource creation passed after this fix.

`make check` and `go test -race ./...` passed natively on macOS arm64 (Go 1.26.5)
and Linux arm64 (Go 1.26.0). The five Ruby verifier tests and three real PTY
tests passed on both. Incus regressions cover offline rendering, dependency
selection, API project scoping, VM/container creation, rejection of inherited
profiles and unsafe resolved settings/devices, bootstrap and failure ordering,
startup readiness/cancellation, SSH identity separation, and token transport.
Four OS/architecture release archives cross-built; the generated Homebrew
formula parses and requires Lima only on macOS. The final Linux ARM archive
also ran outside the checkout and passed `verify final-container --backend incus`.
These are local test artifacts, not published releases.

### Nested hardware VM attempt

The outer Lima VM exposed `/dev/kvm`. Incus created `nested-vm` as an arm64
hardware VM from the Ubuntu 26.04 default image, with KVM acceleration and
`-cpu host`. Its console remained at the UEFI/bootloader handoff, with no Linux
boot output, DHCP lease, or working Incus agent. Creation reached its guest
readiness timeout. Retrying with one vCPU, and a separate temporary diagnostic
boot with Secure Boot disabled, produced the same result. The Secure Boot
override was removed afterward; the product recipe retains the default.

This environment therefore did **not** complete nested Incus VM acceptance.
The VM code path has host regression coverage, but end-to-end guest validation
here uses the explicitly supported container mode. KVM device availability
alone is not sufficient evidence of a working nested guest stack. No host
AppArmor or firmware workaround was added to make that claim.

Both containers, the incomplete nested VM, and their disposable Linux host
were removed after testing.
Existing `agent-dev`, `default-dev-vm`, and `pgx-dev-vm` instances and the macOS
SSH configuration were not modified. Authenticated model tasks, private GitHub
repository scope, and desktop-provided tool inventory remain untested.

References used for the backend:
[Incus creation](https://linuxcontainers.org/incus/docs/main/howto/instances_create/),
[Incus guest execution](https://linuxcontainers.org/incus/docs/main/instance-exec/),
[instance options](https://linuxcontainers.org/incus/docs/main/reference/instance_options/),
and [Lima nested virtualization configuration](https://github.com/lima-vm/lima/blob/v2.2.0/templates/default.yaml).

## Go host CLI and distribution

Tested September 5, 2026 with Go 1.26.5 and Lima 2.2.0 on macOS arm64.
The host entry point is now `agent-vm`; guest provisioning remains Bash and
guest acceptance probes remain Ruby. Go embeds the template, policies, and
scripts in the executable. Parsed default Go and Ruby Lima recipes compared
equal before removing the old host orchestrator.

Created a fresh Ubuntu 26.04 VM, `agent-go-port-test`, by running the compiled
executable from `/tmp` with `--cpus 2 --memory 2GiB --disk 20GiB`. Creation,
root bootstrap, provisioning, and all credential-free guest checks passed with
Codex 0.153.4. Explicit configuration passed without changing the boot ID.
A Lima stop/start changed the boot ID and SSH port, preserved the provisioning
marker's timestamp, and passed all guest checks again. The same generated SSH
entry worked for dev and root across that restart. Creation with the existing
name was refused. The disposable VM was removed afterward; existing VMs and
the user's SSH configuration were not modified.

Token transport was exercised with synthetic input only. The actual terminal
hid the input, and the guest stored it with mode 0600. Real pseudo-terminal
regressions check successful entry, Ctrl-C, and SIGTERM: cancellation returns
promptly, restores terminal settings, and never sends a token. These regressions
passed on both macOS arm64 and Ubuntu 26.04 arm64. They caught and fixed a macOS
hang caused by closing a terminal with an outstanding blocking read; input now
uses nonblocking reads with bounded polling. Darwin's transient PENDIN kernel
flag is excluded from the terminal-setting comparison.

`make check` and `go test -race ./...` passed. Go tests cover argument and
environment handling, embedded payloads, resource options, prerequisite errors,
unsafe/malformed VM refusal, bootstrap/configuration ordering and failure stops,
token stdin transport, repeatable dotfiles installation, and SSH file migration,
preservation, symlink refusal, and user/port connection separation. The existing
five Ruby guest-verifier tests and three terminal regression tests passed.

Release archives cross-built for macOS/Linux on arm64/amd64 with checksums and
dependency license notices. The macOS ARM archive ran outside the checkout;
the Linux ARM archive ran inside the disposable guest. Both rendered their
embedded recipe and reported the build version. Archive contents, checksums,
binary architectures, generated Homebrew formula hashes and Ruby syntax, workflow
YAML, and diff whitespace were checked. Test archives use `v0.1.0-test`; this is
not a published release. GitHub Actions and release/tap publication are prepared
but have not run remotely because this checkout has no Git remote. Full Lima
host acceptance on Linux and Intel macOS remains untested. Historical validation
sections below retain the commands and account names used at the time.

## Optional dotfiles provisioning

Tested September 5, 2026 on fresh Ubuntu 26.04 VM `agent-dotfiles-test`,
with Codex 0.153.4. A credential-free fixture Git repository was transferred
into the guest during test bootstrap, then selected with `--dotfiles-repo`
using a guest-local `file://` URL and `--dotfiles-install 'setup fixture.sh'`.
No personal dotfiles repository or credentials were used.

Creation installed the fixture independently for root and dev. Checks confirmed
correct HOME, USER, LOGNAME, working directory, checkout ownership, and GitHub
HTTPS helper configuration. Both accounts resolved the VM-managed Codex and gh;
dev still lacked sudo and could not read root's installed files.

Configuration without dotfiles options left the fixture run counts unchanged.
After committing a fixture update, configuration through the CLI fetched it
with a fast-forward and reran both installers, producing the new version and
exactly two runs per account. Full guest account and Codex sandbox acceptance
checks passed on creation and both configuration runs. A test-harness ownership
issue on the transferred source repository was corrected before committing the
fixture update; no product changes were needed. The temporary VM was removed.
Existing VMs were not modified.

Local validation: 23 tests and 115 assertions passed, along with Bash syntax,
Lima template validation, and `git diff --check`. Arbitrary third-party installers
and private repository authentication remain dependent on their own setup.

## Primary dev account and root administration

Tested September 5, 2026 on fresh Ubuntu 26.04 VM `agent-root-test` with Codex
0.153.4. Lima's primary account is dev; vmadmin is no longer created. Creation
uses dev's initial sudo solely to install root's public SSH keys and enable
key-only root access. Root SSH is checked before provisioning denies dev sudo.
Further configuration runs directly as root without boot provisioning.

Creation and all guest acceptance checks passed. Native Lima SSH selected dev
without a user override. Root SSH worked; sshd's effective settings disabled
password and keyboard-interactive authentication and limited root login to
non-password authentication. The vmadmin account was absent. Explicit configure
passed without rebooting. A real Lima stop/start preserved the installation
marker; dev still had no sudo, root SSH still worked, and all guest checks passed.
The temporary VM was removed; existing VMs were not migrated.

All 21 host tests (97 assertions), Bash syntax, Lima validation, and whitespace
checks passed. Historical sections below retain the account names used then.

## SSH sharing separated by user


Tested September 5, 2026. Generated SSH entries and orchestration connections
now use `ControlMaster auto`, `ControlPath ~/.ssh/control-%C`, and
`ControlPersist 60`. OpenSSH's hash separates remote users, hosts, and ports.
Existing installed entries require an explicit `install-ssh` refresh.

All 20 host tests (92 assertions) passed, including distinct expanded socket
paths for two users across two ports. A read-only live test against the older
`agent-dev` VM used its existing `dev` and `jack` accounts and temporary sockets.
Each user reused its own master PID on a second connection; the two users had
different master PIDs and `id -un` always returned the requested user. Test
connections were closed afterward. No VM or host SSH configuration was changed.

## Lima lifecycle and SSH simplification


Tested September 5, 2026 using fresh VM `agent-ssh-test`. The Ruby `start`,
`shell`, and `admin` commands are removed. Creation and explicit configuration
still verify the guest. SSH defaults to dev at `lima-NAME`; specifying
`vmadmin@lima-NAME` selects administration without another alias.

The SSH entry now includes Lima's current SSH file rather than parsing and
copying connection settings. A direct Lima stop/start changed the SSH port;
the unchanged entry successfully logged in as both users afterward, admin sudo
worked, and all guest acceptance checks passed. The temporary VM was deleted.
No host SSH configuration was changed during this test.

All 20 host tests (91 assertions) passed. The SSH test uses OpenSSH's actual
configuration parser with changing ports, both usernames, a path containing
spaces, and checks that agent forwarding and connection sharing remain disabled.
The removed commands are rejected before launching subprocesses. Historical
sections below retain the old lifecycle and alias names used in those tests.

## Explicit provisioning lifecycle


Tested September 5, 2026 with fresh Ubuntu 26.04 VM `agent-explicit-test`.
Lima's stored configuration had no provisioning scripts. `create` applied setup
via vmadmin SSH and ran all guest acceptance checks successfully with Codex
0.153.4. The launcher no longer uses a Lima provisioning parameter as a marker;
it validates the account, plain mode, sharing settings, and absence of boot hooks.

A real Lima stop/start changed the kernel boot ID while preserving a synthetic
comment added to the managed policy. File sizes and modification timestamps for
the policy, completion marker, recorded Codex version, GitHub wrapper, and apt
history stayed unchanged. Explicit `configure` then restored the policy and
passed all guest checks without changing the boot ID. Both temporary VMs used
for these two changes were removed after testing.

All 19 host tests (68 assertions), Bash syntax, Lima template validation, and
whitespace checks passed. Tests cover configuration over admin SSH stdin,
automatic verification, starting a stopped VM for configuration, starting without
provisioning, and failure propagation. Earlier sections describe historical
recipes; current creation/configuration always runs verification, and ordinary
startup only checks the installation completion marker.

## Latest Codex installation


Tested September 5, 2026 on fresh Ubuntu 26.04 VM `agent-latest-test`.
The npm `latest` tag installed Codex 0.153.4 and all guest acceptance checks
passed. All 16 host tests (58 assertions), Bash syntax, Lima template validation,
and whitespace checks passed. The recipe no longer pins a Codex version; the
installed version is recorded for diagnostics and policy behavior is tested.

## Portable administrator account

Tested September 5, 2026 with a fresh `agent-vmadmin-test` VM using Ubuntu
26.04 and the final recipe. Provisioning and all guest acceptance checks passed.
The generated admin SSH alias logged in as `vmadmin`, passwordless sudo worked,
and the account had home `/home/vmadmin` and comment `VM administrator`.
No `/home/jack` directory or custom AppArmor profile was present. Ubuntu's global
user-namespace restriction remained enabled (`1`). The temporary VM was removed
after testing. Historical results below retain the account names used at the time.

All 16 host tests (58 assertions), Bash syntax, Lima template validation, and
whitespace checks passed. The current recipe requires fresh VMs; no administrator
account migration is provided.

## Ubuntu 26.04 default

The template now uses `template:_images/ubuntu-26.04`. Tested September 5, 2026
with a fresh `agent-2604-test` VM: Ubuntu 26.04 LTS arm64, Ruby 3.3.8, Codex
0.149.0, Lima 2.2.0/VZ. Provisioning and all guest acceptance checks passed.
The temporary VM was removed after testing. Existing VMs, including the Ubuntu
24.04 `agent-dev`, were not upgraded or recreated.

Ubuntu 26.04 supplies `/etc/apparmor.d/bwrap-userns-restrict`. The previous
custom profile caused conflicting attachments for `/usr/bin/bwrap`, preventing
sandbox startup. The custom profile and all provisioning logic for it have now
been removed. New VMs rely solely on Ubuntu 26.04's packaged profile; there is no
older-image fallback or migration logic. The 26.04 test confirmed the custom profile
was absent and `kernel.apparmor_restrict_unprivileged_userns` remained `1`.

Host tests (16 tests / 58 assertions), Bash syntax, Lima template validation,
and diff whitespace checks passed. Authentication and desktop integration checks
remain outside these credential-free tests.

## Ruby guest verification port

The remaining guest probes are now `scripts/verify_guest.rb` and
`scripts/check_codex.rb`. The sandbox canary itself also runs Ruby. Provisioning
installs Ruby and removes the two obsolete deployed Python probes. It no longer
explicitly installs Python development packages; existing system Python packages
are left alone because Ubuntu and other development tools may depend on them.

The verifier checks the installed requirements file against its provisioned
SHA-256 digest, then checks managed profile and feature behavior through Codex.
This preserves the policy checks without adding a Ruby TOML-parser dependency.
Subprocess execution has bounded waits and process-group cleanup; the protocol
reader handles buffered responses, notifications, EOF, and timeouts.

Host tests: 16 tests / 58 assertions passed, covering orchestration plus protocol
handling, command output/exit status/timeouts, and preservation of an existing
canary file. Ruby syntax, Bash syntax, and generated Lima validation passed.

Retested in `agent-dev` using guest Ruby 3.2.3 after reconfiguration and reboot:
all account, filesystem, app-server, managed-feature, and conflicting-override
checks passed. Both obsolete deployed `.py` files were confirmed absent. The
user-dependent authenticated checks remain outstanding as documented below.

## Ruby orchestration port

Retested September 5, 2026 using Ruby 4.0.2 and Lima 2.2.0:

- Replaced `scripts/vm.py` with `scripts/vm.rb` and the host unit tests with
  Minitest. At that stage, Bash provisioning and Python guest probes were unchanged except for
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

The maintained acceptance command is `agent-vm verify agent-dev`.
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
After explicit user approval, a VM-only custom AppArmor profile was installed
and the sandbox checks succeeded. That historical 24.04 workaround has since
been removed from this project; it is preserved in earlier commits only.
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
administration as `vmadmin`. Earlier recommendations for per-project settings,
domain proxies, or Claude configuration are not this implementation's scope.

## Reference documentation

- [Lima 2.2.0 configuration schema](https://github.com/lima-vm/lima/blob/v2.2.0/templates/default.yaml)
- [Lima plain mode](https://lima-vm.io/docs/config/plain/)
- [Lima SSH access](https://lima-vm.io/docs/usage/ssh/)
- [Codex managed configuration](https://learn.chatgpt.com/docs/enterprise/managed-configuration)
- [Codex remote connections](https://learn.chatgpt.com/docs/remote-connections)
- [Ubuntu 24.04 release notes: user-namespace restrictions](https://documentation.ubuntu.com/release-notes/24.04/)

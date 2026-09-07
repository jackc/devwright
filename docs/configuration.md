# Configuration and operation

[Back to the README](../README.md) · [Backend setup](backends.md)

- [Everyday use](#everyday-use)
- [Credentials and sign-in](#credentials-and-sign-in)
- [Updates](#change-or-update-the-setup)
- [Codex policy and defaults](#custom-codex-policy-and-defaults)
- [Claude Code policy and defaults](#custom-claude-code-policy-and-defaults)
- [Dotfiles](#optional-dotfiles)
- [Configuration behavior](#configuration-behavior)
- [Isolation and validation limits](#isolation-and-validation-limits)

Examples use the default Lima backend. Repeat `--backend incus` or `--backend user`
on every command for another backend; see [backend setup](backends.md) for its
prerequisites and supported actions. VM/container development runs as `dev` in
`/home/dev/projects`. Native development uses the managed account's own home;
its agent settings are editable defaults, and host managed policy stays untouched.

## Everyday use

```sh
limactl start dev
ssh lima-dev          # development as dev
ssh root@lima-dev     # administration
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
installed settings persist. Use `devwright verify dev` to recheck
the restrictions without updating tools.

## Credentials and sign-in

Development credentials live in `/home/dev/.config/devwright/credentials.sh`,
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
vi ~/.config/devwright/credentials.sh
```

Root can populate the same file administratively, for example by running
`sudo -u dev -H vi /home/dev/.config/devwright/credentials.sh`. Keep it owned by
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

Sign in to Claude Code the same way: run `claude` inside the guest and finish
`/login` by opening the printed URL on the host and pasting the code, or create
a long-lived token with `claude setup-token` on a trusted machine and add
`export CLAUDE_CODE_OAUTH_TOKEN='...'` to `credentials.sh`. An interactive login
is stored in `~/.claude/.credentials.json` with mode `0600`; the managed policy
denies that file, and Codex's sign-in file, to sandboxed commands and to Claude
Code's file tools.

For the desktop, add **lima-dev** as an SSH host in its remote connection
settings, then select a guest directory under `/home/dev/projects`. Use the
`dev` connection, not a connection with the username `root`. The app may
install a separate remote runtime; verify its version and effective managed
policy in a fresh task. Installation of the CLI does not authenticate the desktop.

## Change or update the setup

Install the updated executable, then apply its embedded recipe with `configure`.
Configuration also updates Codex to the latest stable release. When developing
the recipe, rebuild and reinstall after editing the shared source.
The versions installed during provisioning are recorded
in `/usr/local/share/devwright/codex-version` and `claude-version` for diagnostics;
verification checks actual policy behavior rather than requiring those exact versions:

```sh
devwright configure dev  # applies the recipe and verifies it
```

`configure` sends the current Bash setup script and embedded policy files
directly over root SSH. Lima stores no setup script to replay on boot. Embedded recipe
edits take effect only after rebuilding the executable and explicitly running
`configure`; the agent file overrides below require no rebuild. Updating the
executable alone does not modify existing VMs.

`configure` applies setup to a running VM without rebooting it; if stopped, it
starts the VM first. It installs the latest Codex and Claude Code packages, restores the selected
managed policies and system settings, and runs the guest acceptance checks. It
preserves `dev`'s personal Codex and Claude Code configs unless explicitly
replaced, plus credentials and projects. Use it while development tools
are idle because it updates installed software and reloads SSH configuration.

## Custom Codex policy and defaults

The embedded `vm_dev` profile permits writes throughout the development user's
home and temporary directories, with read access elsewhere. Keep repositories
and worktrees under that home; external workspace paths require a custom policy.
It intentionally does not inherit Codex's `:workspace` profile, whose read-only
Git metadata can block commits and linked-worktree cleanup. Repositories and
worktrees under the home directory can share Git metadata and merge changes
without widening permissions for each task. This also permits changes to shell
startup files and agent configuration in that home. The VM and Unix account
permissions are the primary boundary; managed secret-path denials remain an
additional safeguard. This profile does not grant sudo or override OS permissions.
It grants access through the enclosing home rather than `:workspace_roots`:
Codex 0.153.4 otherwise adds a read-only mount for a linked worktree's resolved
Git directory even with a writable `.git` rule. An explicit `~/.codex` grant
allows worktrees stored by the app there; the sign-in file remains denied.

The executable includes default Codex files. Supply explicit host file paths to
replace either complete file; settings are not merged and project directories
are not searched automatically. The Lima and Incus backends support these options:

```sh
devwright create my-dev \
  --codex-requirements ./requirements.toml \
  --codex-config ./config.toml

# Select a different policy for an existing VM.
devwright configure my-dev --codex-requirements ./requirements.toml

# Explicitly replace an existing personal config.
devwright configure my-dev --codex-config ./config.toml --replace-codex-config

# Stop using a custom policy and restore the executable's embedded policy.
devwright configure my-dev --reset-codex-requirements
```

Files are read and checked for valid TOML before contacting the instance manager.
Codex validates its full configuration schema in the guest during verification;
a schema error can therefore fail setup after the files have been installed.
Correct the files and rerun `configure` with the override options.

`--codex-requirements` installs `/etc/codex/requirements.toml` and saves a root-owned
copy at `/usr/local/share/devwright/custom-requirements.toml`. Future `configure`
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
resolved feature enforcement. Embedded and custom policies both get behavioral
probes using the selected default profile: workspace writes, outside-workspace
writes, synthetic `~/.pgpass` reads, and a lower-scope read override. Reports
separate observed access (**allowed** or **denied**) from assessment: **matches
policy**, **contradicts policy**, or **expectation unknown**. Formatting changes
do not disable tests. Complex filesystem rules whose expectations cannot be
inferred still get measured; these observations are not a certification of the
policy. These probes do not measure network access.

Independent behavioral checks continue after a mismatch or execution error,
including the other agent's checks. Verification returns a nonzero exit status
for known policy mismatches and probes that fail to execute. Intentional access,
unknown expectations, and safe skips do not themselves fail verification. An
existing `~/.pgpass` is never read or overwritten: only its read and override
probes are skipped, while workspace probes still run. Prerequisite failures
such as unsafe ownership or a policy checksum mismatch can stop dependent tests.

## Custom Claude Code policy and defaults

Claude Code gets the same treatment with its own files. The managed policy is
installed as `/etc/claude-code/managed-settings.json`, which Claude Code applies
above every user, project, local, and `--settings` value; the initial personal
settings go to `/home/dev/.claude/settings.json`. Both are strict JSON objects
without comments.

User settings supplied with `--claude-config` are validated against a bundled
copy of the published Claude Code settings schema before provisioning. Guest
and native-user verification also validate the installed user file, including
preserved settings. This catches malformed network, model, environment, and
hook settings that would make Claude silently discard the entire file.
Validation works offline; settings added by newer Claude releases may require
updating the [schema snapshot](../internal/claudepolicy/schema/README.md).

```sh
devwright create my-dev \
  --claude-managed-settings ./managed-settings.json \
  --claude-config ./settings.json

devwright configure my-dev --claude-managed-settings ./managed-settings.json
devwright configure my-dev --claude-config ./settings.json --replace-claude-config
devwright configure my-dev --reset-claude-managed-settings
```

The selection, restore, reset, and replace rules match the Codex options above,
with the custom copy saved as `/usr/local/share/devwright/custom-managed-settings.json`.
The embedded policy turns the Bash sandbox on and refuses to start without it,
forbids unsandboxed retries and bypass mode, denies the same secret paths as the
Codex policy plus both agents' sign-in files to sandboxed commands and to the
Read/Grep/Glob tools, locks read paths so no lower scope can re-open them, allows
every domain because the VM is the network boundary, and turns off claude.ai
connectors, configured MCP servers, the built-in browser and computer-use
servers, plugin marketplaces, sideloaded plugins, and channels. Self-updates are
disabled so `configure` is the only update path. The initial personal file only
lets sandboxed commands run without prompts. Claude Code drops individual
invalid managed entries and keeps the rest, so verification checks the posture
Claude Code reports rather than trusting the file.

Verification checks that `claude` resolves to the root-owned package, the
policy checksum, the absence of `managed-settings.d` drop-ins and
`managed-mcp.json`, the bubblewrap AppArmor state described under isolation limits, and that
`claude sandbox status` reports the sandbox on, strict, and set by policy, when
the installed release prints that report. For embedded and custom policies it
then drives non-interactive sessions through a loopback stub of the Messages API:
the stub asks Claude Code to run the verifier's probe through the Bash tool, so
the real sandbox applies without a model or sign-in. Each session gets a minimal
environment, so proxy settings, provider selectors, and credentials exported in
`credentials.sh` or dotfiles cannot redirect it away from the stub. The probe
reports workspace write, outside-workspace write, and synthetic `~/.pgpass`
read access, then repeats with a lower-scope `allowRead` override. When the
managed policy requires the read denial to hold against overrides, a readable
canary is a failure. Otherwise the override's observed behavior is reported
without assuming it must be denied. A session that never ran the probe is an
execution error. The tests exercise the selected managed policy without adding
sandbox restrictions to make the probe pass.

## Optional dotfiles

Pass your own Git repository when creating or configuring a VM:

```sh
devwright create my-dev --dotfiles-repo https://github.com/OWNER/dotfiles.git
devwright configure my-dev --dotfiles-repo https://github.com/OWNER/dotfiles.git --dotfiles-install setup.sh
```

No dotfiles are installed by default. The default installer is `install`; use
`--dotfiles-install` for another executable path relative to the repository.
The installer must have a shebang and run without interaction. It runs with the
repository as its working directory and the target account's HOME, USER, LOGNAME,
and SHELL. It is responsible for its dependencies, backups, startup files, and
any shell preferences; the recipe does not assume Mise or Zsh.

Both `root` and `dev` get independent checkouts at
`~/.local/share/devwright/dotfiles`. The installer runs as each account, so it must
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

## Configuration behavior

Explicit provisioning restores the selected policies, SSH settings, and `dev`'s empty
supplementary group list. Manual changes to those settings survive normal
restarts but are overwritten by `configure`. Root's authorized keys are
initialized once during creation and are not recopied from development files
during configuration.
If setup fails, fix the cause and rerun `configure`; restarting does not retry it.
Applicable acceptance checks run during `create`, `configure`, and `verify`. Lima
startup does not run our verification.

Resource/image changes in `devwright.json` apply to newly created VMs after rebuilding.
Use `--cpus`, `--memory`, and `--disk` for per-VM resource choices during creation.
Change existing VM resources with Lima's own stopped-instance editing workflow. Run Ubuntu
security upgrades administratively as needed; package installation is not a
substitute for a guest patching policy.

`install-ssh` adds an Include to `~/.ssh/config`, backs up that file before
changing it, and stores a small SSH entry under `~/.ssh/devwright/`. The entry includes
Lima's own SSH configuration and defaults to `dev`; `root@` overrides the user. It refuses
to overwrite an unrelated generated-file target or rewrite a symlinked SSH
config. `ssh-config` prints the entry instead if you manage SSH configuration
through your own dotfiles tooling.
Create fresh VMs for the Ubuntu 26.04 recipe with `dev` as the primary user.

## Isolation and validation limits

* Plain mode disables host filesystem mounts, SSH-agent forwarding, automatic
  port forwarding, and bundled containerd. Use explicit SSH tunnels for previews,
  for example `ssh -N -L 3000:127.0.0.1:3000 lima-dev`.
* Linux protects `/root` from `dev`; the embedded managed Codex policy additionally
  denies common sensitive paths, permits home writes and direct networking,
  and disables apps, plugins, browser/computer use and configured MCP servers.
  The embedded managed Claude Code policy does the same for Claude Code's Bash
  sandbox and file tools. Only Bash is sandboxed there: WebFetch and WebSearch
  run in Claude Code's own process. Claude Code's bundled seccomp filter blocks
  Unix sockets inside the sandbox once the required AppArmor profile is in place;
  the guest exposes no agent, Docker, or Incus socket to `dev` either way, and
  the verifier keeps checking that.
* The Claude desktop app's SSH sessions install their own Claude Code runtime
  and deliver claude.ai connectors in-process, where `disableClaudeAiConnectors`
  and the MCP allowlist do not reach them. The managed file still binds that
  runtime's sandbox and permission controls. Inspect a fresh session's tool
  inventory before relying on that separation.
* Ubuntu's global unprivileged user-namespace restriction stays enabled. The
  recipe disables Ubuntu's stock `bwrap-userns-restrict` profile through
  `/etc/apparmor.d/disable/` and installs the profile Anthropic documents for
  Claude Code. That profile is unconfined and inherited on exec, so `bwrap` and
  every command it runs, for Codex as well as Claude Code, may create user
  namespaces and hold capabilities inside them. Ubuntu's stock profile confined
  those commands to a child profile that denies capabilities, which blocked the
  nested user namespace Claude Code's seccomp filter creates; commands now rely
  on bubblewrap's namespaces and each agent's own sandbox instead. Verification
  checks the installed profile's checksum, that the stock profile is disabled,
  and that the global restriction is still on. Without AppArmor the recipe
  installs no profile and verification says so. Older Ubuntu releases are not
  supported.
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
  a valid negative test. No network allowlist is included.

`create` also validates the generated Lima YAML.
See [VALIDATION.md](../VALIDATION.md) for the actual VM test results and research provenance.

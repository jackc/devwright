# Claude Code controls: design

Design date: September 6, 2026. Status: **implemented in the CLI, recipe, and
verifier on the same day; host tests pass, guest acceptance in a VM is still
pending** (see [VALIDATION.md](VALIDATION.md)). It gives Claude Code the same
treatment the recipe gives Codex: a root-owned, VM-wide managed policy that the
`dev` account cannot loosen, an editable personal config that provisioning
preserves, host-side override flags, and credential-free acceptance checks.

Inputs: the Codex rules in [config/codex/requirements.toml](config/codex/requirements.toml)
and [config/codex/config.toml](config/codex/config.toml), the project goals in
[README.md](README.md#original-project-goals), the architecture assessment and
consolidated findings from the research branches (`OPTIONS.md`, `FINDINGS.md`),
and the [Claude Code documentation](https://code.claude.com/docs/en/managed-settings)
as of Claude Code 2.1.261. Everything marked **verified** below was checked on
this Mac today with the local `claude` binary; nothing has run in a guest yet.

## Summary of the mapping

| Codex control today | Claude Code equivalent | Where |
| --- | --- | --- |
| Root-owned `/etc/codex/requirements.toml` | Root-owned `/etc/claude-code/managed-settings.json` | [config/claude/managed-settings.json](config/claude/managed-settings.json) |
| `default_permissions = "vm_dev"` (workspace writes, direct network) | `sandbox.enabled`, `failIfUnavailable`, `allowedDomains: ["*"]`; writes are limited to the working directory and session temp by default | managed |
| `allowed_sandbox_modes` excludes `danger-full-access` | `sandbox.allowUnsandboxedCommands: false` (strict mode; the `dangerouslyDisableSandbox` escape hatch is ignored) | managed |
| `allowed_approval_policies` excludes `never` | `permissions.disableBypassPermissionsMode: "disable"` | managed |
| `[permissions.filesystem] deny_read` | `sandbox.filesystem.denyRead` for Bash plus `permissions.deny` `Read(...)` rules for the file tools; `allowManagedReadPathsOnly` stops lower scopes from re-opening a path | managed |
| A conflicting profile override is rejected | `allowManagedReadPathsOnly: true`; managed `permissions.deny` entries union and cannot be removed by user or project files | managed |
| `features.apps = false` | `disableClaudeAiConnectors: true` | managed |
| `[mcp_servers]` empty (locally configured servers disabled) | `allowedMcpServers: []` (an empty allowlist admits no configured server), `allowManagedMcpServersOnly: true`, `enableAllProjectMcpServers: false` | managed |
| `features.plugins = false` | `strictKnownMarketplaces: []`, `disableSideloadFlags: true`, `channelsEnabled: false` | managed |
| `features.browser_use`, `in_app_browser`, `computer_use = false` | `deniedMcpServers` naming the built-in `claude-in-chrome` and `computer-use` servers, which are exempt from the allowlist but not from the denylist. The desktop app's in-process tools are outside both lists (see limits) | managed |
| Codex updated by `configure` only | apt package on the `latest` channel; `env.DISABLE_UPDATES = "1"` so the binary never self-updates | provisioning, managed |
| `~/.codex/config.toml` initial dev defaults, preserved afterwards | `~/.claude/settings.json` initial dev defaults, preserved afterwards | [config/claude/settings.json](config/claude/settings.json) |
| `--codex-requirements`, `--reset-codex-requirements`, `--codex-config`, `--replace-codex-config` | `--claude-managed-settings`, `--reset-claude-managed-settings`, `--claude-config`, `--replace-claude-config` | host CLI |
| `codex sandbox -P vm_dev` probe in the verifier | A loopback Messages API stub drives `claude -p --bare` through one Bash command; the real sandbox applies | guest verifier |
| `configRequirements/read` through the app-server | `claude sandbox status` JSON (`enabledSource: "policy"`, `strictModeSource: "policy"`) plus the managed file checksum | guest verifier |

## Goals this design meets, and how

* **Secrets stay unreadable.** The managed policy denies the Codex list (`/root`,
  `~/.ssh`, `~/.pgpass`, `~/.aws`, `~/.gnupg`, `~/.kube`) plus both agents'
  credential stores, in the Bash sandbox and in the Read/Grep/Glob tools. The
  two layers are separate in Claude Code; the research verified that each misses
  what the other covers, so both are set.
* **No account-level integrations for coding.** Connectors fetched by the CLI,
  configured MCP servers from any scope, the built-in browser and computer-use
  servers, plugin marketplaces, sideloaded plugins and channels are off. This
  is the parity set with the Codex `features` and `mcp_servers` rules.
* **Safe by default, project cannot loosen.** Managed settings sit above user,
  project, local and `--settings` values. Boolean and lock keys take the managed
  value; deny lists union; the MCP allowlist and marketplace list are taken
  whole from the managed source. Only root can change the file, and root is
  only reachable over key-only SSH.
* **Direct network, like `vm_dev`.** `allowedDomains: ["*"]` gives sandboxed
  commands the VM's network without prompts. The VM or host network policy
  remains the boundary, exactly as for Codex; a project that wants an allowlist
  can add `deniedDomains` (union) but cannot remove the wildcard.
* **Credential-free verification.** The verifier can exercise the real sandbox
  without a sign-in (section "Verification"), so `create`, `configure` and
  `verify` keep working on a VM with no Anthropic credentials, as they do for Codex.

## Guest layout (Lima and Incus)

### Installation

Anthropic publishes signed apt repositories with `stable` and `latest`
channels. The `claude-code` package installs one root-owned file,
`/usr/bin/claude`, plus a copyright notice, has no maintainer scripts and
depends only on `libc6` (**verified** by downloading and listing
`claude-code_2.1.236-1_arm64.deb` from the `stable` channel). This is the
root-owned, no-Node install the recipe wants: the native installer is
home-directory only, and the binary must not be dev-writable.

Provisioning adds, as root:

```sh
install -d -m 755 /etc/apt/keyrings
curl -fsSL https://downloads.claude.ai/keys/claude-code.asc -o /etc/apt/keyrings/claude-code.asc
# Refuse to continue unless the key fingerprint is 31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE (verified today).
echo 'deb [signed-by=/etc/apt/keyrings/claude-code.asc] https://downloads.claude.ai/claude-code/apt/latest latest main' \
  > /etc/apt/sources.list.d/claude-code.list
apt-get update -qq
apt-get install -y --no-install-recommends claude-code socat
```

`bubblewrap` is already installed for Codex; Claude Code's Linux sandbox needs
`bubblewrap` and `socat`. The recipe follows the `latest` channel, as it does
for Codex. The `stable` channel was first proposed, but on the day of the guest
run it served 2.1.236, a month behind `latest` at 2.1.263: that release prints
no sandbox posture on Linux and rejects the newer session flags, so the
verifier could neither read the posture nor rely on the flags. The policy keys
used here need 2.1.219 or later, and the verifier checks that floor; it also
tolerates a release without the posture report and says so. `configure`
upgrades the package on each run, matching "configuration also updates Codex".
The installed version is recorded in `/usr/local/share/dev-sandbox/claude-version`.
The apt key is pinned by fingerprint in the script rather than trusted blindly.

Alternative considered: the official `install.sh` run as root with a private
`HOME` under `/usr/local/share/claude`. It works but leaves a fake home, writes
shell startup files for that home, and keeps the auto-updater layout. The apt
package is simpler and is the documented system-wide method.

### Managed policy

`/etc/claude-code/managed-settings.json`, root-owned, mode `0644`, directory
`0755`. The file lifecycle copies the Codex one exactly: the embedded default
is stored at `/usr/local/share/dev-sandbox/default-managed-settings.json`, a
custom selection at `custom-managed-settings.json`, the installed policy's
checksum at `managed-settings.sha256`, and `configure` restores the selection
in `custom`/`default`/`preserve` modes. The `managed-settings.d/` drop-in
directory and `managed-mcp.json` are never created; the verifier treats their
presence as policy drift, because a drop-in can replace single values.

The embedded policy is [config/claude/managed-settings.json](config/claude/managed-settings.json).
Rule by rule:

| Key | Value | Reason |
| --- | --- | --- |
| `sandbox.enabled`, `failIfUnavailable` | `true` | Sandboxing is the gate; a missing dependency fails the session instead of running unsandboxed. |
| `sandbox.allowUnsandboxedCommands` | `false` | Same intent as excluding `danger-full-access`. The verifier expects `strictMode: true`. |
| `sandbox.filesystem.denyRead` | Codex list plus `~/.codex/auth.json` and `~/.claude/.credentials.json` | Both agents share the `dev` account; neither should read the other's sign-in token through a shell. Linux stores Claude Code's OAuth token in `~/.claude/.credentials.json` with mode 0600. |
| `sandbox.filesystem.allowManagedReadPathsOnly` | `true` | Only managed `allowRead` entries count, so a project cannot re-open a denied path. This is the Claude analogue of Codex rejecting a conflicting profile override. |
| `sandbox.network.allowedDomains` | `["*"]` | Direct network like `vm_dev`. A bare `*` is documented (2.1.186+) and loads (**verified**). |
| `permissions.deny` | `Read(...)` rules for the same paths | The file tools are not sandboxed; deny rules union across scopes and cannot be removed below managed. `//root/**` is the absolute-path form. |
| `permissions.disableBypassPermissionsMode` | `"disable"` | No `--dangerously-skip-permissions`, mirroring the exclusion of `approval_policy = "never"`. |
| `disableClaudeAiConnectors` | `true` | Stops the CLI fetching claude.ai connectors. Any lower scope's `true` also sticks. |
| `allowedMcpServers`, `allowManagedMcpServersOnly` | `[]`, `true` | An empty allowlist admits no configured server from any source, including `.mcp.json`, `--mcp-config`, plugins and `managed-mcp.json`; the lock ignores user and project allowlists. Built-in servers are exempt from the allowlist. |
| `deniedMcpServers` | `claude-in-chrome`, `computer-use` by name | The denylist is the only list that reaches built-in servers, so this is the guest-side counterpart of Codex turning off browser and computer use. It merges with any entries a user adds. |
| `enableAllProjectMcpServers` | `false` | Belt and braces for project `.mcp.json`. |
| `strictKnownMarketplaces`, `disableSideloadFlags`, `channelsEnabled` | `[]`, `true`, `false` | No plugin marketplaces, no `--plugin-dir`/`--plugin-url`/`--mcp-config`/`--agents` sideloading, no messaging channels. `disableSideloadFlags` also rejects `--agents`; drop it in a custom policy if inline agent definitions are wanted. |
| `env.DISABLE_UPDATES` | `"1"` | The package is root-owned; `claude update` and background updates are blocked, so `configure` is the only update path. |

Not included on purpose, to stay at parity with the Codex policy:
`autoAllowBashIfSandboxed` (a user preference, see below), `excludedCommands`
(there is no managed lock for it; keep the list empty and let the user add
entries knowingly), hooks, skills and subagents from projects (they run as `dev`
like any build script), `WebFetch`/`WebSearch` (Codex does not disable web
search in the VM), `enableArtifact`, `disableRemoteControl` and
`disableAgentView`. Those last three are reasonable additions for a stricter
custom policy; they are bridges to claude.ai rather than to host files.

### Initial personal config

`/home/dev/.claude/settings.json`, dev-owned, mode `0600`, installed only when
absent or with `--replace-claude-config`. The embedded file is
[config/claude/settings.json](config/claude/settings.json) and sets only
`sandbox.autoAllowBashIfSandboxed: true`, the counterpart of Codex's
`approval_policy = "on-request"`: sandboxed commands run without prompts, and
anything the sandbox blocks still needs a person. Claude Code's Bash tool
inherits the shell environment by default, so no equivalent of
`shell_environment_policy` is needed for `~/.config/dev-sandbox/credentials.sh`
to reach agents. `~/.claude` is created `0700` next to `~/.codex`; the config
file is staged outside dev's directories and renamed, as for Codex.

Settings files are strict JSON with no comments, and Claude Code rejects a
user file whose shape fails validation as a whole. Documentation for the file
belongs in the README, not in the file.

### Sign-in

Two supported paths, neither of which copies host credentials:

* Interactive: `ssh lima-dev`, run `claude`, complete `/login` by opening the
  printed URL on the host and pasting the code.
* Token: run `claude setup-token` on a trusted machine and add
  `export CLAUDE_CODE_OAUTH_TOKEN='...'` to `~/.config/dev-sandbox/credentials.sh`
  in the guest. This uses the existing credential convention and its startup
  hooks; every process running as `dev` can read it, by design.

`~/.claude/.credentials.json` holds the interactive login on Linux with mode
0600. The managed policy denies it to sandboxed commands and the file tools.

## Host CLI surface

Mirroring the Codex options in [cli.go](cli.go):

```text
  --claude-managed-settings FILE  Use and remember a custom managed policy (create/configure)
  --reset-claude-managed-settings Restore the embedded managed policy (configure)
  --claude-config FILE            Initial dev settings; existing settings are preserved (create/configure)
  --replace-claude-config         Replace existing dev settings with --claude-config (configure)
```

Validation before contacting the instance manager: the file must be JSON with
an object at the top level; the managed file must additionally parse into the
`internal/claudepolicy` struct (sandbox enabled and fail-if-unavailable flags,
`denyRead`, `permissions.deny`, `allowedMcpServers`, `disableClaudeAiConnectors`)
that the verifier compares against the guest. Claude Code drops individual
invalid managed entries and enforces the rest, so a schema slip does not fail
closed the way an invalid Codex requirements file does; the guest verifier's
posture checks are therefore the real gate. The user backend rejects
`--claude-managed-settings` and `--reset-claude-managed-settings` for the same
reason it rejects `--codex-requirements`: the managed path is host-wide.

Same-action rules as Codex: reset/replace only on `configure`, reset conflicts
with a custom file, replace requires a file, and empty paths are refused.

## Provisioning changes

`lima/provision.sh` defines two shell functions, `install_managed_policy` and
`install_user_config`, that both agents' file blocks call, driven by
`claude_policy_mode` and `replace_claude_config` variables and
`__DEFAULT_MANAGED_SETTINGS_B64__`, `__MANAGED_SETTINGS_B64__` and
`__CLAUDE_CONFIG_B64__` placeholders substituted by `vm.provision()`. The
policy files are installed before the package; after the apt install
`claude --version` records the version and `sudo -u dev -H claude --version`
proves dev can run it. Before applying ownership to dev's dot-directories the
script refuses any that is a symlink, because `install -d` follows one.

Ubuntu 26.04's packaged `bwrap-userns-restrict` profile lets Codex's
bubblewrap sandbox run, but it confines every command bubblewrap launches to a
child profile that denies capabilities. The first guest run showed Claude Code
2.1.263 failing every sandboxed command with `apply-seccomp: write
/proc/self/setgroups (nested userns is capability-restricted)`: its bundled
seccomp filter creates a nested user namespace, which that child profile
forbids. The recipe therefore disables the stock profile through
`/etc/apparmor.d/disable/` and installs Anthropic's documented
`/etc/apparmor.d/bwrap` profile (`flags=(unconfined)` with `userns`, ABI 5.0 on
26.04). The global `kernel.apparmor_restrict_unprivileged_userns` restriction
stays on for everything else. The profile is unconfined and inherited on
exec, so `bwrap` and every command it runs, under Codex as well as Claude
Code, may create user namespaces and hold in-namespace capabilities; those
commands are confined by bubblewrap's namespaces and each agent's own sandbox
instead of the capability-denying child profile. The verifier checks the
profile's checksum, that the stock profile is disabled, and that the sysctl
is still `1`; without AppArmor the recipe installs nothing and the verifier
says NOT TESTED. With that profile in place the lab passed inside the guest. Incus containers may additionally need
`sandbox.enableWeakerNestedSandbox: true` because bubblewrap cannot mount a
fresh `/proc` in an unprivileged container; Codex's probe passed in such
containers, so try without it first and add it per instance only if the probe
fails. Claude Code 2.1.263 bundles its seccomp filter, so Unix sockets are
blocked inside the sandbox on Linux.

## Verification in the guest

`internal/verification` gains a Claude section run after the Codex checks.
Everything below is credential-free and uses synthetic files only.

1. `command -v claude` resolves to `/usr/bin/claude`, root-owned and not
   writable by dev (added to the existing writable-path check), and
   `claude --version` reports at least 2.1.219.
2. `/etc/claude-code` and `managed-settings.json` are root-owned and not
   writable by dev; the checksum matches
   `/usr/local/share/dev-sandbox/managed-settings.sha256`; no
   `managed-settings.d` entries and no `managed-mcp.json` exist.
3. `claude sandbox status` run as dev in `~/projects` prints one JSON line with
   `enabled: true`, `enabledSource: "policy"`, `strictMode: true`,
   `strictModeSource: "policy"` and `filesystemPolicy: "strict"`. This is the
   posture the local binary reports for the embedded file supplied through
   `--settings` and, since the guest runs, what Claude Code 2.1.263 reports for
   the managed file on Linux (**verified**). Releases that print only the
   legacy status object skip this comparison with a NOT TESTED line.
4. `claude doctor` output is captured for diagnostics only; it is prose.
5. **Sandbox behaviour probe, embedded policy only.** The verifier starts a
   loopback HTTP stub of the Messages API on `127.0.0.1:0`, then runs, as dev,
   in a fixture under `~/projects/verify-XXXX/workspace`:

   ```sh
   ANTHROPIC_API_KEY=synthetic ANTHROPIC_BASE_URL=http://127.0.0.1:PORT \
   CLAUDE_CONFIG_DIR=<fixture>/config DISABLE_TELEMETRY=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
   claude -p --bare --model stub-model --allowedTools Bash \
     --max-turns 3 --output-format json 'Run the acceptance probe.'
   ```

   The session's environment is an allow-list (`HOME`, `PATH`, `USER`,
   `LOGNAME`, `SHELL`, locale, `TERM`, `TMPDIR`, `TZ`) plus the variables
   above, so proxy settings, provider selectors and credentials from dev's
   `credentials.sh` cannot redirect the request away from the stub.

   The stub answers the first request with a streamed `tool_use` block for
   `Bash` whose command is `/usr/local/share/dev-sandbox/verify claude-probe
   CANARY SIBLING`, and the second request, which carries the `tool_result`,
   with `end_turn`. The probe is the existing `SandboxProbe`: workspace write
   succeeds, outside-workspace write is denied, the canary read (a synthetic
   `~/.pgpass`, refused if one exists) is denied. The stub relays the tool
   result to the verifier, which also checks the sibling file is unchanged and
   the JSON result has `subtype: "success"`. A second run passes
   `--settings '{"sandbox":{"filesystem":{"allowRead":["~/.pgpass"]}}}'` and
   must still report the canary denied, proving `allowManagedReadPathsOnly`
   holds; this is the counterpart of Codex rejecting a conflicting override.
   `--bare` reads no OAuth credentials or keychain and skips hooks, plugins and
   memory, so the run touches nothing personal; `CLAUDE_CONFIG_DIR` keeps
   session state inside the fixture. The guest runs confirmed that managed
   settings apply under `--bare`: the canary denial and the lock test depend
   on it. Only the probe's own `did not hold` report counts as an enforcement
   failure; a session that never ran the probe is reported as not run, with
   hints about bubblewrap, socat, the AppArmor profile and containers.
6. A custom policy reports the behaviour probe as **NOT TESTED**, exactly as
   Codex does, and the run ends with the existing "authenticated model run,
   private repository scope, desktop-provided tool inventory" not-tested line.

The stub approach was prototyped on this Mac today (**verified**, see
"Validation performed"): the nested session completed in two turns, the
workspace write succeeded, the sibling write and canary read were denied, and
the outbound request was blocked with a `sandbox_violations` report. The Go
implementation needs only `net/http` and server-sent events, no third-party
dependency. [tests/claude-sandbox-lab.py](tests/claude-sandbox-lab.py) is the
host-side reproduction; run it from a terminal.

## Restricted native users (`--backend user`)

The enforced boundary stays the OS account, as for Codex. Claude Code installs
as the account with the official installer, `curl -fsSL https://claude.ai/install.sh | bash -s latest`,
which places the launcher at `~/.local/bin/claude` and versions under
`~/.local/share/claude/`. The installer refuses only root-with-`SUDO_USER`
invocations; the helper runs it as the target UID, so it proceeds. Auto-update
stays on for this backend because the account owns the install, mirroring the
Codex user install.

Editable defaults, installed like `nativeCodexConfig` (absent-only, replaced
with `--replace-claude-config`), as `~/.claude/settings.json`:

```json
{
  "sandbox": {
    "enabled": true,
    "failIfUnavailable": true,
    "allowUnsandboxedCommands": false,
    "autoAllowBashIfSandboxed": true,
    "filesystem": {
      "denyRead": ["~/.ssh", "~/.pgpass", "~/.aws", "~/.gnupg", "~/.kube", "~/.codex/auth.json", "~/.claude/.credentials.json"]
    },
    "network": {"allowedDomains": ["*"]}
  },
  "permissions": {
    "deny": ["Read(~/.ssh/**)", "Read(~/.pgpass)", "Read(~/.aws/**)", "Read(~/.gnupg/**)", "Read(~/.kube/**)", "Read(~/.codex/auth.json)", "Read(~/.claude/.credentials.json)"]
  },
  "disableClaudeAiConnectors": true,
  "allowedMcpServers": [],
  "deniedMcpServers": [{"serverName": "claude-in-chrome"}, {"serverName": "computer-use"}],
  "enableAllProjectMcpServers": false
}
```

Managed-only keys are omitted because a user file cannot set them. The
account's own programs can edit this file; that is documented as "user
defaults, not enforced policy", matching the Codex wording. On macOS the
sandbox is Seatbelt and the login token lives in that account's keychain. On
Linux the host must already have `bubblewrap` and `socat`; the helper's
prerequisite check adds `/usr/bin/bwrap` and `/usr/bin/socat` and does not
install packages, per the backend's rules. Because the backend changes no host
security policy, preflight also refuses to create accounts on a Linux host
whose kernel restricts unprivileged user namespaces while `/usr/bin/bwrap` has
no unconfined AppArmor profile loaded, and points at the README's profile
instructions. Managed settings on the host are never touched; `render` reports
"editable user defaults; host managed settings untouched". Native verification
runs `claude --version` and the stub-driven probe with explicit sandbox
settings passed through `--settings`, so it tests the mechanism rather than
the editable defaults; it does not read `claude sandbox status`.

## Limits, in the README's terms

* **Only Bash is sandboxed.** Read, Edit, Glob and Grep follow permission
  rules, which the managed policy sets. WebFetch, WebSearch and MCP run in
  Claude Code's own process; MCP is disabled, the web tools are not.
* **Unix sockets are blocked only by the seccomp filter.** Claude Code 2.1.263
  bundles that filter, and it applies once the bwrap AppArmor profile is in
  place; the verifier does not probe sockets. The guest has no agent socket,
  Docker socket or Incus socket reachable by `dev`; the existing verifier
  checks keep proving that. Codex's `vm_dev` profile allows Unix sockets.
* **`excludedCommands` has no managed lock.** A user or project file can add
  commands that run outside the sandbox. The OS account, not the sandbox, is
  the boundary the README already claims; treat additions like startup-file
  edits.
* **Desktop SSH sessions bring their own runtime and connectors.** The Claude
  desktop app installs Claude Code on the remote host itself and delivers
  claude.ai connectors in-process; `disableClaudeAiConnectors` and the MCP
  allowlist do not reach those connectors. The managed file still binds that
  runtime's sandbox, permission and plugin controls. As with Codex, inspect a
  fresh session's tool inventory before treating the separation as verified,
  and prefer the CLI over SSH for coding sessions that must not see personal
  connectors. `sshHostAllowlist` and `sshConfigs` are host-side managed keys
  that can pin desktop SSH sessions to the `lima-*`, `incus-*` and `user-*`
  aliases; they are outside this recipe.
* **Temp is writable inside the sandbox.** The session temp directory is
  writable by design, and on macOS `/tmp` too. Do not keep credentials there.
  Codex has the same behaviour under `/private/tmp`.
* **Replacement binaries.** Policy controls the supported client. Anyone running
  as `dev` can install another copy of Claude Code in the home directory; the
  managed file still applies to it, but a modified client would not honour it.
  Same caveat as for Codex.

## Validation performed for this design (macOS arm64, Claude Code 2.1.261)

* `claude --settings FILE sandbox status` accepts both embedded files and
  reports `enabled: true, enabledSource: policy, strictMode: true,
  strictModeSource: policy, filesystemPolicy: strict` for the managed file.
  Every key used in the managed file, plus `credentials.*`,
  `blockReadsOutsideWorkingDirectories`, `allowManagedHooksOnly`,
  `deniedMcpServers`, `allowedChannelPlugins`, `forceLoginMethod`,
  `availableModels` and `excludedCommands`, loads without dropping the file.
* A file with a wrong-typed value (`allowedDomains` as a string,
  `enabled` as a string) or the unknown `network.allowMachLookup` key drops the
  whole `--settings` file and reports the sandbox off, whereas an unknown key
  elsewhere is tolerated. Managed files are documented to degrade per entry
  instead; the guest verifier must therefore check posture, not file presence.
* Anthropic's apt `stable` repository: `Release` lists `amd64` and `arm64`;
  the newest arm64 package was 2.1.236-1 (100 MB) and contains only
  `/usr/bin/claude` and a copyright file, no maintainer scripts. The release
  key fingerprint matched the documented `31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE`.
* Stub-driven `claude -p --bare` with a synthetic API key completed against the
  loopback stub with no model traffic (`subtype: success`, two turns). With the
  lab's closed policy the probe observed: workspace write ok, outside-workspace
  write denied, canary read denied, `/etc/hosts` readable, `example.com`
  blocked with a `sandbox_violations` report. With the embedded managed policy
  (`allowedDomains: ["*"]`) the same probe reached `example.com` (HTTP 200)
  while the write and read denials held, so the wildcard gives the direct
  network the design intends. A first attempt with the fixture under `/tmp`
  showed the sibling write allowed, confirming that temp stays writable and
  that fixtures must live under the home directory.
  `python3 tests/claude-sandbox-lab.py` and
  `python3 tests/claude-sandbox-lab.py --settings config/claude/managed-settings.json`
  both pass from a terminal.
* The `claude doctor` command prints prose and reports managed settings by
  source; it is a diagnostic, not a machine-checkable interface.

Not validated: anything inside a guest (apt install on Ubuntu 26.04, the
bubblewrap AppArmor interaction with Claude Code, Incus containers, the exact
`claude sandbox status` output for a managed file, the `policyLocked` field),
the native-user installer flow, and every authenticated behaviour.

## Implementation plan

1. `assets.go`: embed `config/claude/*.json`.
2. `internal/claudepolicy`: JSON struct and `Parse` used by host validation and
   the verifier, like `codexpolicy`.
3. `cli.go`, `codex.go`: add the four flags with the same action and conflict
   rules; generalise `loadCodexFiles` into a per-agent file loader.
4. `vm.go`: substitute the three new placeholders and prepend
   `claude_policy_mode` and `replace_claude_config`.
5. `lima/provision.sh`: apt key with fingerprint check, repository, package
   install, the CLAUDE FILES block, version record, dev smoke test.
6. `internal/verification`: `claude.go` with the posture checks, stub server,
   probe orchestration and lock test; `cmd/dev-sandbox-verify` gains
   `claude-probe CANARY SIBLING`.
7. `user_setup.go`, `user_backend.go`, `internal/verification/native.go`:
   installer step, editable defaults, prerequisites, native verification.
8. Tests: flag validation, file lifecycle against temporary paths (as
   `TestCodexFileLifecycle`), stub SSE framing and tool-result relay, probe
   preservation of the sibling, drift detection for drop-ins.
9. README and VALIDATION: a "Custom Claude Code policy and defaults" section
   beside the Codex one, the sign-in paths, and guest results once a VM run
   passes. Add `~/.claude/.credentials.json` to the Codex `deny_read` list at
   the same time so each agent's token is hidden from the other.

## Open questions

* Resolved: with the managed file, the guest's `claude sandbox status`
  (2.1.263) reports `enabledSource: "policy"` and `strictModeSource: "policy"`
  as `--settings` does on the host; `policyLocked` stays `false` because it
  describes the Windows sandbox install, so the verifier ignores it.
* Resolved for VMs: Ubuntu 26.04's packaged profile blocks Claude Code's
  seccomp step, so the recipe installs the documented `bwrap` profile (see
  Provisioning). Incus containers remain untested.
* Where does the desktop app install its remote runtime, and does that copy
  honour `DISABLE_UPDATES`?
* `stable` versus `latest` apt channel: resolved in favour of `latest` after
  the first guest run (see Installation).

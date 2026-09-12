# Development and releases

The current executable imports `internal/launch`, which embeds the editable
starter recipe and credential shell hook. It does not import the historical root
package or embed guest verifier binaries. Runtime dependencies are Lima,
OpenSSH, and Bash; Git is required only for repository sources. Go dependencies handle command flags, credential declaration
YAML, terminal prompts, and onboarding locks; users install no language runtime.

```sh
mise run build
mise run test
mise run check
# Full historical Go regression suite, including old verifier builds:
mise run test-legacy
```

`internal/launch/template/` is the source copied by `devwright init`. Its YAML and
scripts are the actual recipe, with no custom VM schema or generated shell
payload. Project owners can edit all of it. The launcher delegates template
resolution, schema validation, provisioning, and readiness to Lima.

An opt-in acceptance test creates a real disposable VM, tests repository checkout,
dev-only dotfiles, credential recovery and rotation, the project hook, and restart
persistence. It leaves the VM stopped for inspection:

```sh
DEVWRIGHT_ACCEPTANCE=my-fresh-test mise exec -- \
  go test ./internal/launch -run '^TestAcceptance$' -v -count=1
DEVWRIGHT_CUSTOM_ACCEPTANCE=my-custom-test mise exec -- \
  go test ./internal/launch -run '^TestCustomAcceptance$' -v -count=1
DEVWRIGHT_DIRECTORY_ACCEPTANCE=my-directory-test mise exec -- \
  go test ./internal/launch -run '^TestDirectoryAcceptance$' -v -count=1
```

Run unattended (without a controlling terminal) to exercise missing-credential
recovery. It uses synthetic credentials only. It does not test authenticated agent
sessions, private Git access, submodules/LFS, or every host architecture.

The legacy `internal/verification` package remains available for template regression
testing. It is no longer a creation requirement or a public launcher command.
Historical backend tests and their additional tooling are documented in
[LEGACY-DEVELOPMENT.md](LEGACY-DEVELOPMENT.md).

Release builds use `cmd/devwright`, GoReleaser, and `scripts/release-prepare.sh`.
The preparation step collects Go dependency licenses and does not build verifiers.
`mise run release` builds a local snapshot; publishing remains an explicit action.

Validated September 8, 2026 on macOS arm64 with Lima 2.2.0 and Ubuntu 26.04:
the seven launcher unit tests, race checks, Go vet, Bash syntax, release preparation,
and historical Go regression suite passed. Fresh starter and custom VM acceptance
passed, including credential recovery/rotation, once-only setup, restart persistence,
custom users and environment, explicit root dotfiles, SSH alias use, and readiness
failure handling. The Linux amd64 binary cross-build passed; Linux hosts and x86_64
guests have not received an end-to-end run of the new workflow.

The local-directory workflow also passed a fresh VM acceptance run with host Git
deliberately disabled: files, executable modes, hidden files, and symlinks arrived,
and resumed onboarding preserved guest edits after the source directory was removed.

Validated September 9, 2026: source-based project names and `--project-name`
passed the launcher suite, build, Go vet, release configuration checks, and Bash
syntax checks. Fresh local-directory, Git-repository, and custom-recipe VM
acceptance runs passed, including distinct VM/project names, an explicit project
name, retry/restart recovery, and preservation of guest edits. Unit tests also
cover saved-name validation and the VM-name fallback for older onboarding state.

## SSH client choice

Devwright uses the system OpenSSH client for internal operations, passing Lima's
generated instance `ssh.config` with `ssh -F` (see
[`app.ssh`](internal/launch/onboard.go)). This keeps Lima responsible for connection
settings, including ports that can change after a VM restart, and lets OpenSSH
interpret its own configuration format.

`golang.org/x/crypto/ssh` is a viable alternative. It would provide structured
errors and direct connection control, but it does not automatically consume
OpenSSH configuration. Devwright would need to obtain Lima's connection details
and manage authentication, host-key handling, cancellation, and cleanup. Avoiding
subprocesses is unlikely to materially improve provisioning performance; remote
commands would still require shell quoting.

Keep OpenSSH for this workload. Reconsider an internal client if targeted retries,
finer connection control, or substantial remote execution volume creates a
concrete benefit. Setup deliberately authenticates each call afresh because
provisioning can change login shells and groups; preserve that behavior with
either client. Interactive SSH aliases can remain independent of this choice.

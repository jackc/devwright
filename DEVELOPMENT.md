# Development and releases

[Back to the README](README.md)

Run the commands below from a source checkout. For environment setup and
operation, see [backend setup](docs/backends.md) and
[configuration](docs/configuration.md).

## Build, check, and release

The host CLI uses `go-toml/v2` to parse custom configuration; the Linux verifier
uses `golang.org/x/sys` for filesystem access checks. Versions and checksums are
recorded in `go.mod` and `go.sum`. The build toolchain is pinned in `mise.toml`,
which local development and GitHub Actions both use; `go.mod` records the minimum
supported Go version. Builds use Bash and gzip; tests use Go, Git, Bash, Python 3, and
OpenSSH with synthetic data and temporary directories.

`python3 tests/claude-sandbox-lab.py` exercises Claude Code's real Bash sandbox
on the host with a loopback stub and synthetic files, using no model or sign-in.
Run it from a terminal, not inside an agent session; pass
`--settings config/claude/managed-settings.json` to test the embedded policy.

Run `bash tests/codex-worktree.sh` as the development user inside a provisioned
Linux VM to verify that the default managed sandbox blocks protected Git writes.
Run `python3 tests/codex-worktree-approval.py` there to exercise an actual
`on-request` approval through an ephemeral Codex app-server session. This second
check requires Codex sign-in and consumes model usage. It approves only a fixed
script in a disposable repository, then verifies branch creation, commit, merge,
and worktree cleanup. Both checks preserve real repositories.

`tests/environment-linux.sh` additionally checks credential loading on Ubuntu
26.04 with Bash/Zsh interactive and noninteractive SSH, inherited child
environments, the packaged `gh`, and isolation from root and another user. Run it as root in a test VM:
`bash tests/environment-linux.sh lima/provision.sh`. It uses private mount,
network, and PID namespaces, so real credentials, accounts, and SSH configuration
are not changed. It requires the recipe's packages, `ip`, `unshare`, and overlayfs.

```sh
mise run check                         # Go tests/vet, verifier tests, Bash syntax
mise run build                         # .build/devwright, recipe and guest verifiers embedded
mise exec -- go test -race ./...
mise run release                       # local snapshot; never publishes
mise exec -- goreleaser check           # validate release configuration
# At a clean, tagged commit (requires gh and GITHUB_REPOSITORY or GitHub access):
mise run release build                 # tagged archives, checksums, and formula; no upload
```

Run `mise trust` and `mise install` when setting up a checkout. If mise is
[activated in your shell](https://mise.jdx.dev/getting-started.html#activate-mise),
you can omit `mise exec --` for direct Go commands. Tasks use `mise run` either way.
After changing the Go pin in `mise.toml`, run
`mise install` again. Mise sets `GOTOOLCHAIN=local` so Go uses the pinned compiler
instead of automatically downloading a newer toolchain.

`mise run` defaults to `build`. To embed a custom host version, use
`VERSION=v0.1.0 mise run build`.

`mise run build`, `mise run test`, and `mise run check` first cross-compile and
compress the Linux and macOS verifiers for arm64 and amd64. For direct
`go build` or `go test` commands, run `mise run guest`
first and rerun it after changing verifier code. Generated files in `guestbin/`
are ignored by Git. Releases rebuild them before embedding them in each host binary.
Provisioning selects the guest architecture with `uname -m`; it installs no Ruby
or Go runtime. Existing environments need one `configure` run with the rebuilt
CLI before using its `verify` command, which now invokes the compiled verifier.

Release files are written to `.build/releases/`. Archives contain the
executable, documentation, and dependency license notices. `CGO_ENABLED=0` keeps
builds free of C library dependencies on Linux. macOS binaries still use OS
libraries. Builds cover macOS/Linux on arm64/amd64; full VM acceptance has been
exercised on macOS arm64. Cross-compilation alone does not validate other hosts'
virtualization setup.

GitHub Actions runs checks on macOS and Linux. Pushing a `vX.Y.Z` tag runs checks
and uses the pinned GoReleaser version to build and upload the four archives and
checksums. The release script adds the Homebrew formula and publishes the release
only after all assets have uploaded. GoReleaser is configured in `.goreleaser.yaml`;
its preparation hook rebuilds the embedded verifiers and collects license notices.

To release, commit the changes, tag the commit, and push the branch and tag:

```sh
git tag -a v0.1.0 -m "Release v0.1.0"
git push origin HEAD v0.1.0
```

For a manual release from a clean tagged checkout, set `GITHUB_TOKEN` and run
`mise run release publish`. Do not run it while the tag workflow is publishing.
Local `snapshot` and `build` modes do not publish. All modes replace the contents
of `.build/releases/`.

Copy the release's generated `devwright.rb` into `Formula/devwright.rb` in your
Homebrew tap. Users can then install with `brew install OWNER/TAP/devwright`;
the formula depends on Lima on macOS. Install your chosen backend separately on
Linux. Replace OWNER/TAP with the actual tap name.

## Source map

| File | Responsibility |
| --- | --- |
| `cli.go`, `vm.go`, `incus.go`, `process.go`, `ssh.go` | Host CLI, Lima/Incus orchestration, subprocesses, and SSH configuration |
| `assets.go` | Embeds the recipe at build time |
| `lima/devwright.json` | Lima template (JSON is valid YAML): image base, resources, primary account, plain mode |
| `lima/bootstrap.sh` | Creation-only setup of key-based root SSH |
| `incus/bootstrap.sh` | Creation-only SSH/account setup through Incus |
| `lima/provision.sh` | Shared repeatable OS, account, SSH, development credential hooks, Git authentication, and Codex/Claude Code installation for Lima and Incus |
| `dotfiles.go` | Host-authenticated Git fetch and temporary bundle transport for VM dotfiles |
| `lima/dotfiles.sh` | Optional per-account dotfiles installation for root and dev |
| `lima/credentials.sh` | Private dev credential file and Bash/Zsh startup hooks |
| `config/codex/requirements.toml` | Root-owned, VM-wide managed restrictions |
| `config/codex/config.toml` | Initial dev defaults, preserved after first installation |
| `config/claude/managed-settings.json` | Root-owned, VM-wide managed Claude Code settings |
| `config/claude/settings.json` | Initial dev Claude Code settings, preserved after first installation |
| `internal/verification/`, `cmd/devwright-verify/` | Guest/native isolation, agent policy, and sandbox acceptance checks |
| `internal/codexpolicy/`, `internal/claudepolicy/` | The managed policy keys the host validates and the verifier compares |
| `tests/claude-sandbox-lab.py` | Terminal-run lab exercising Claude Code's sandbox without a model or sign-in |
| `scripts/build-guest.sh`, `guestbin/` | Build and embed the Linux/macOS verifiers |
| `user_*.go`, `internal/userpolicy/` | Native account provisioning, SSH policy, verification, and deletion |

After editing embedded recipes or policies, rebuild and reinstall the executable,
then run `devwright configure NAME` to apply them to an existing environment.
Updating the executable alone does not modify environments. See
[configuration behavior](docs/configuration.md#configuration-behavior).

## Native acceptance testing

The macOS test is a standalone script requiring a built executable and enabled
Remote Login. Run it as your administrator login, **not as root**:

```sh
mise run build
bash tests/users-macos.sh "$PWD/.build/devwright"
# Keep fixtures for inspection, then use the printed cleanup command:
bash tests/users-macos.sh "$PWD/.build/devwright" --keep
bash tests/users-macos.sh --cleanup /path/printed/by/the/test
```

The test creates two disposable accounts with synthetic credentials and a private
client HOME. It checks SSH/shell access, cross-account isolation, credential and
configuration preservation, wrong-key rejection, active-process deletion refusal,
private home archiving, and preservation of unrelated host configuration. It
never uses real GitHub or Codex credentials. Logs and identity tombstones remain
for inspection; fixture accounts and homes are removed by default. On macOS the
test verifies each fixture against its root-owned registry and unloads only that
fixture's user domain when its sole remaining processes are Apple's `distnoted`
and `cfprefsd`, parented by launchd. Other workloads prevent cleanup and are
listed with a retry command. The host's system SSH domain is untouched.

For Linux, run `bash tests/users-linux.sh /absolute/path/devwright` on a prepared
host, or `mise run test-users-linux-vm` from this checkout. The VM driver creates a
stock Ubuntu 26.04 Lima VM with separate Lima state and no host mounts, installs
test prerequisites and an explicit persistent SSH host key inside that VM, runs
acceptance plus a stop/start persistence check, and deletes only that VM. The
explicit test key avoids the Lima boot setup replacing the image's default keys;
normal native account management never changes server host keys.
Use `bash tests/users-linux-vm.sh --keep` to retain it for debugging. Existing
Lima VMs are not used. Full macOS account/Remote Login acceptance remains manual;
see [VALIDATION.md](VALIDATION.md) for actual results.

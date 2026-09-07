# Devwright

Devwright creates isolated development environments for humans and coding agents.
It is a Go CLI that sets up Codex and Claude Code in Lima VMs, Incus VMs or
containers, or restricted native accounts on Linux and macOS.

It exists to keep coding work away
from your personal SSH agent, sensitive dotfiles, and unrelated credentials, and
to make restrictive defaults part of setup instead of something you must remember
for every project. Development credentials belong to each environment; optional
dotfiles let you bring a familiar shell setup.

VMs and containers run Ubuntu 26.04 with a non-sudo `dev` account, key-only root
SSH for administration, and managed Codex and Claude Code policies. Those policies
restrict filesystem access and disable supported CLI integrations such as apps,
plugins, and configured MCP servers. Native accounts provide a lighter option
with editable agent defaults and OS account isolation.

## Choose a backend

| Backend | Host | Environment |
| --- | --- | --- |
| [Lima](docs/backends.md#lima), the default | macOS or Linux | Hardware VM |
| [Incus](docs/backends.md#incus-on-linux) | Linux | Hardware VM or unprivileged system container |
| [Native user](docs/backends.md#restricted-native-users-on-linux-and-macos) | macOS or Linux | Separate restricted OS account using the host's SSH service |

Projects in one environment share its account and credentials. Create separate
environments when projects need separate access. Containers and native accounts
share the host kernel; native accounts also share host resources and public files.
There is no network allowlist. Desktop apps may supply their own runtimes and
connectors, so CLI verification does not establish desktop isolation. See
[isolation and validation limits](docs/configuration.md#isolation-and-validation-limits)
and [tested configurations](VALIDATION.md).

## Install

From a source checkout, install [mise](https://mise.jdx.dev/getting-started.html)
(`brew install mise` on macOS), then build with the pinned Go toolchain:

```sh
mise trust
mise install
mise run build
mkdir -p ~/.local/bin
install -m 755 .build/devwright ~/.local/bin/devwright
export PATH="$HOME/.local/bin:$PATH"  # also add to your shell startup file
```

For the default backend, install Lima 2.2+ (`brew install lima` on macOS) and
OpenSSH; see [Lima installation](https://lima-vm.io/docs/installation/) for other
hosts. Incus and native users have their own [host prerequisites](docs/backends.md).

If installing a prebuilt release archive, choose your OS and CPU, verify its
SHA-256 against `checksums.txt`, and put its `devwright` executable on PATH.
Prebuilt executables include the recipe and verifiers and need no Go compiler.
See [Development](DEVELOPMENT.md) for builds, releases, and Homebrew packaging.

## Quick start

Create a Lima VM and install its SSH alias:

```sh
devwright create dev  # downloads Ubuntu, installs tools, and verifies the setup
devwright install-ssh dev
ssh lima-dev
```

Inside the environment:

```sh
cd ~/projects
codex login --device-auth
claude  # complete /login when prompted
```

Add repository tokens and other development credentials to
`~/.config/devwright/credentials.sh` inside the environment, using shell-quoted
`export` assignments. Host credentials are not copied. Scope tokens to the
repositories the environment needs, and open a fresh session after changing them.
See [credentials and sign-in](docs/configuration.md#credentials-and-sign-in) for
GitHub, alternative sign-in flows, and desktop SSH connections.

New VMs default to 4 CPUs, 4 GiB memory, and a 60 GiB sparse disk. Override these
with `--cpus`, `--memory`, and `--disk` on `create`. For another backend, follow the
[Incus](docs/backends.md#incus-on-linux) or
[native-user setup](docs/backends.md#restricted-native-users-on-linux-and-macos)
and repeat `--backend incus` or `--backend user` on every command.

## Everyday commands

Run these on the host:

```sh
devwright verify dev     # check the existing setup without updating tools
devwright configure dev  # apply the current recipe, update tools, and verify
ssh root@lima-dev        # administer the guest
limactl stop dev
limactl start dev
```

`configure` preserves projects, credentials, and personal agent settings unless
explicitly replaced; run it while development tools are idle. Restarting an
environment does not update or reconfigure it. Use Lima or Incus for VM/container
start, stop, and delete operations; native accounts use `devwright delete`.
Run `devwright --help` to list commands.

Command-specific flags follow the subcommand (for example,
`devwright create dev --cpus 8`). Use `devwright COMMAND --help` to see its options.
The global `--backend` flag can appear before or after the subcommand.

## More information

- [Backend setup](docs/backends.md): host prerequisites, resources, SSH, recovery,
  native account deletion, and environments created under the old `dev-sandbox` name.
- [Configuration and operation](docs/configuration.md): credentials, custom agent
  policies, dotfiles, updates, and isolation limits.
- [Development and releases](DEVELOPMENT.md): source map, builds, tests, and packaging.
- [Validation results](VALIDATION.md) and [Claude Code design](CLAUDE-CODE-DESIGN.md):
  recorded checks, policy rationale, and remaining limits.

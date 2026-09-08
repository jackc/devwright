# Lima + Ansible environment

This directory starts the replacement for the devwright executable. At this
stage it contains only the Lima VM definition; Ansible and the launcher have
not been implemented. Creating this VM does **not** yet produce the completed
restricted development environment.

## Files needed

One checked-in Lima file, `lima.yaml`, is sufficient. It references the Ubuntu
26.04 image template shipped with Lima 2.2+, just as the existing executable
does. No separate cloud-init file, global Lima configuration, network definition,
or image-building recipe is needed for the current behavior.

Lima generates the instance configuration, SSH connection file, login identity,
and disks outside this repository. Those are instance state, not additional
files to check in. The existing `lima/devwright.json` remains the old executable's
input during this transition; the new YAML is independently usable by Lima.

## Responsibility boundary

| Existing behavior | Owner in the replacement |
| --- | --- |
| Ubuntu 26.04 image and architecture selection | Lima |
| 4 CPUs, 4 GiB RAM, 60 GiB sparse disk; instance-specific overrides | Lima |
| Native/default hypervisor selection | Lima |
| No host filesystem mounts or SSH agent forwarding | Lima |
| No importing personal host SSH public keys or proxy environment | Lima |
| Plain mode; no bundled containerd or automatic application port forwarding | Lima |
| Initial `dev` account, UID 1000, `/home/dev`, Bash, public-key login | Lima |
| Initial passwordless sudo needed for provisioning | Lima bootstrap state |
| Root public-key SSH setup and guest SSH server policy | Ansible bootstrap |
| Revoke `dev` sudo, supplementary groups, password login; set home permissions | Ansible |
| Apt packages, repositories, updates, tool installation, AppArmor configuration | Ansible |
| Agent managed policies, user defaults, dotfiles, Git configuration | Ansible |
| Projects directory, environment files, credential permissions and shell hooks | Ansible |
| Credential collection and interactive account sign-in coordination | Launcher / Ansible |
| Start/stop/delete operations | Lima commands; launcher may invoke them |
| Friendly host SSH alias and Ansible SSH connection options | Launcher / host SSH config |
| Check effective Lima configuration for unexpected global overrides | Launcher preflight |
| Development readiness and guest isolation checks | Ansible / verification commands |

Lima can execute system provisioning scripts, but none are necessary here:
Ansible can reach the initial account over SSH and use sudo. Keeping guest setup
in Ansible gives initial setup and later configuration the same implementation.

## Bootstrap handoff

To preserve today's non-sudo development account and separate root administration,
the future playbook needs two connection phases:

1. Connect as `dev`, use sudo, and install Lima's guest public login keys for
   root with key-only SSH server settings. This replaces `lima/bootstrap.sh`.
2. Establish a separate root SSH connection and verify it works **before**
   revoking `dev`'s sudo access. Continue system configuration as root, and run
   user setup as `dev` where appropriate.

Subsequent configuration connects as root directly. An interrupted run must be
resumable through whichever administration path has already been established.
Do not interpret `passwordlessSudo: false` as removing sudo permission: it changes
Lima's password behavior rather than expressing the final restricted account.
The final sudo policy belongs in Ansible, as it does in the old provisioning script.

## Network and SSH behavior

Plain mode preserves the existing behavior: normal guest outbound networking
and Lima's SSH access remain available, but automatic application port forwarding
is disabled. This is not a network allowlist. Use explicit SSH tunnels for web
services initially; enabling Lima application forwarding would be a separate
configuration decision.

Lima's generated per-instance `ssh.config` provides the connection details.
The future launcher should use it rather than save a fixed SSH port. Preserve
the old host SSH options: `IdentityAgent none`, `ForwardAgent no`, and a
`ControlPath` containing `%C` so root and dev never share a multiplexed connection.
These are OpenSSH client options, not extra Lima YAML fields. Put overrides before
the generated configuration is included, matching the existing `ssh.go` behavior.

Lima supports host-global defaults and overrides. A checked-in template alone
does not replace devwright's check of the effective instance configuration.
The launcher must retain that check before provisioning or injecting credentials.

## Validate and create

From the repository root, validate without creating a VM:

```sh
limactl validate environment/lima.yaml
```

For a fresh, uniquely named bootstrap VM:

```sh
limactl create --tty=false --name=recipe-dev environment/lima.yaml
limactl start --tty=false recipe-dev
```

To choose different resources, pass `--cpus=8 --memory=8 --disk=100` to
`limactl create` (memory and disk CLI values are in GiB), or edit the YAML before
creation. Editing the source YAML does not update an existing instance; use
Lima's instance editing commands for later VM resource changes.

The image template comes from the installed Lima release and can change when
Lima is upgraded. Apt package versions will remain unpinned. Package upgrades
and automatic-update policy belong in the future Ansible configuration; this
Lima template does not install or upgrade development packages on restart.

## Validation status

The first draft is intended for configuration validation and review. Full parity
requires the Ansible bootstrap, guest setup, host SSH integration, and verification
described above, followed by a fresh VM creation and restart test.

References: [Lima templates](https://lima-vm.io/docs/templates/),
[plain mode](https://lima-vm.io/docs/config/plain/),
[template field reference](https://github.com/lima-vm/lima/blob/v2.2.0/templates/default.yaml).

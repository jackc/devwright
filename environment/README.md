# Superseded experiment

The Ruby/Ansible experiment has been replaced by the Go `devwright` project
launcher. See the [current README](../README.md). Run `devwright init` in a project
to write native Lima YAML, provisioning scripts, and credential declarations.

The old experiment is preserved at commit `1717467` (Ruby/native provisioning)
and `c8cfe1b` (Ansible). Its existing VMs remain usable with Lima, but are not
adopted by the new launcher. Create a fresh VM for the new workflow.

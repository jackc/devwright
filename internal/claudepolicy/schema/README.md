# Claude Code settings schema

`settings.json` is an unmodified copy of SchemaStore's published Claude Code
settings schema at commit `d2cbdcde9855c1bf9ea99c336163cc6c93753e39`:

[Upstream schema](https://github.com/SchemaStore/schemastore/blob/d2cbdcde9855c1bf9ea99c336163cc6c93753e39/src/schemas/json/claude-code-settings.json)

Claude Code's [settings documentation](https://code.claude.com/docs/en/settings)
points to this schema for validation. The upstream Apache 2.0 license is in
`LICENSE`; both the schema license and the Go validator's license are included
in release archives.

The host and guest embed this snapshot and validate without network access or a
host Claude installation. It checks published fields, including nested network,
environment, hook, and model settings that the managed posture struct does not
read. Unknown fields are handled exactly as the schema specifies; this is not
a claim to validate settings introduced after the snapshot.

To update, copy the complete upstream schema at a chosen commit, update this
reference and the license if needed, then run the Go tests and rebuild the guest
verifiers. Keep the snapshot aligned with the supported Claude settings surface.

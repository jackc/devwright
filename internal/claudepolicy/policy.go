// Package claudepolicy describes the managed Claude Code settings the recipe
// installs. Claude Code validates the full schema itself; this package reads
// only the keys that host validation and guest verification compare.
package claudepolicy

import (
	"encoding/json"
	"errors"
)

type Settings struct {
	Sandbox struct {
		Enabled                  *bool `json:"enabled"`
		FailIfUnavailable        *bool `json:"failIfUnavailable"`
		AllowUnsandboxedCommands *bool `json:"allowUnsandboxedCommands"`
		Filesystem               struct {
			Disabled                  *bool    `json:"disabled"`
			DenyRead                  []string `json:"denyRead"`
			AllowManagedReadPathsOnly *bool    `json:"allowManagedReadPathsOnly"`
		} `json:"filesystem"`
	} `json:"sandbox"`
	Permissions struct {
		Deny                         []string `json:"deny"`
		DisableBypassPermissionsMode string   `json:"disableBypassPermissionsMode"`
	} `json:"permissions"`
	DisableClaudeAiConnectors *bool              `json:"disableClaudeAiConnectors"`
	AllowedMcpServers         *[]json.RawMessage `json:"allowedMcpServers"`
}

// Parse requires a JSON object; Claude Code drops a managed file that is not
// one. Wrong-typed values for the compared keys are rejected here too.
func Parse(data []byte) (Settings, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return Settings{}, err
	}
	if object == nil {
		return Settings{}, errors.New("managed settings must be a JSON object")
	}
	var s Settings
	err := json.Unmarshal(data, &s)
	return s, err
}

// SandboxEnabled reports whether the policy turns the Bash sandbox on.
func (s Settings) SandboxEnabled() bool {
	return s.Sandbox.Enabled != nil && *s.Sandbox.Enabled
}

// StrictSandbox reports whether the policy forbids unsandboxed retries.
func (s Settings) StrictSandbox() bool {
	return s.Sandbox.AllowUnsandboxedCommands != nil && !*s.Sandbox.AllowUnsandboxedCommands
}

// FilesystemIsolated reports whether the filesystem layer stays on.
func (s Settings) FilesystemIsolated() bool {
	return s.Sandbox.Filesystem.Disabled == nil || !*s.Sandbox.Filesystem.Disabled
}

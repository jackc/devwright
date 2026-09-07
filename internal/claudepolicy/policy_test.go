package claudepolicy

import (
	"os"
	"testing"
)

func TestEmbeddedPolicyParses(t *testing.T) {
	data, err := os.ReadFile("../../config/claude/managed-settings.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if s.Sandbox.Enabled != nil || s.Sandbox.AllowUnsandboxedCommands != nil || s.Sandbox.FailIfUnavailable != nil {
		t.Fatalf("embedded policy posture: %+v", s.Sandbox)
	}
	if s.AllowedMcpServers == nil || len(*s.AllowedMcpServers) != 0 || s.DisableClaudeAiConnectors == nil || !*s.DisableClaudeAiConnectors {
		t.Fatal("embedded policy must disable connectors and configured MCP servers")
	}
	if s.Permissions.DisableBypassPermissionsMode != "" || len(s.Permissions.Deny) != 0 || len(s.Sandbox.Filesystem.DenyRead) != 0 {
		t.Fatalf("embedded permissions: %+v", s.Permissions)
	}
}

func TestInvalidPolicies(t *testing.T) {
	for _, data := range []string{"", "[]", "null", "{broken", `{"sandbox": {"enabled": "yes"}}`, `{"allowedMcpServers": "all"}`, `{"permissions": {"deny": "Read(x)"}}`} {
		if _, err := Parse([]byte(data)); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
	s, err := Parse([]byte(`{"sandbox": {"enabled": false}, "unrelated": 1}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.SandboxEnabled() || s.StrictSandbox() || !s.FilesystemIsolated() {
		t.Fatalf("relaxed policy: %+v", s.Sandbox)
	}
}

func TestValidateUserSettings(t *testing.T) {
	for _, path := range []string{"../../config/claude/settings.json", "../../config/claude/managed-settings.json"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate(data); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	for _, data := range []string{
		`{}`, `{"sandbox":{"enabled":false}}`,
		`{"model":"personal","env":{"EXAMPLE":"value"},"sandbox":{"network":{"allowedDomains":["*"]}}}`,
		`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo check"}]}]}}`,
		// The published schema permits extension keys at the top level. An
		// instance's $schema is data, never a URL to fetch during validation.
		`{"$schema":"https://invalid.example/settings.json","futureSetting":true}`,
	} {
		if err := Validate([]byte(data)); err != nil {
			t.Errorf("rejected %s: %v", data, err)
		}
	}
	for _, data := range []string{
		``, `null`, `[]`, `{broken`,
		`{"sandbox":{"enabled":null}}`,
		`{"sandbox":{"network":{"allowedDomains":"*"}}}`,
		`{"sandbox":{"network":{"allowedDomains":[null]}}}`,
		`{"sandbox":{"network":{"httpProxyPort":"8080"}}}`,
		`{"sandbox":{"autoAllowBashIfSandboxed":"yes"}}`,
		`{"sandbox":{"filesystem":{"allowWrite":"/tmp"}}}`,
		`{"model":42}`, `{"env":{"EXAMPLE":false}}`,
		`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":42}]}]}}`,
	} {
		if err := Validate([]byte(data)); err == nil {
			t.Errorf("accepted %s", data)
		}
	}
}

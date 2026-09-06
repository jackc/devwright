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
	if !s.SandboxEnabled() || !s.StrictSandbox() || !s.FilesystemIsolated() {
		t.Fatalf("embedded policy posture: %+v", s.Sandbox)
	}
	if s.AllowedMcpServers == nil || len(*s.AllowedMcpServers) != 0 || s.DisableClaudeAiConnectors == nil || !*s.DisableClaudeAiConnectors {
		t.Fatal("embedded policy must disable connectors and configured MCP servers")
	}
	if s.Permissions.DisableBypassPermissionsMode != "disable" || len(s.Permissions.Deny) == 0 || len(s.Sandbox.Filesystem.DenyRead) == 0 {
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

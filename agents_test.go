package devsandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCodexOptions(t *testing.T) {
	for _, args := range [][]string{
		{"verify", "--codex-requirements", "p"},
		{"render", "--codex-config", "p"},
		{"create", "--reset-codex-requirements"},
		{"configure", "--reset-codex-requirements", "--codex-requirements", "p"},
		{"configure", "--replace-codex-config"},
		{"create", "--codex-config="},
	} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if _, err := parseOptions([]string{"--codex-requirements", "policy", "configure", "--codex-config", "config", "--replace-codex-config"}); err != nil {
		t.Fatal(err)
	}
}

func TestCodexInputValidation(t *testing.T) {
	for _, data := range []string{"broken = [", "[features]\napps = 'wrong type'"} {
		path := filepath.Join(t.TempDir(), "requirements.toml")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		o := options{codexRequirements: path}
		if err := o.loadAgentFiles(); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
	o := options{codexConfig: filepath.Join(t.TempDir(), "missing")}
	if err := o.loadAgentFiles(); err == nil {
		t.Fatal("accepted missing file")
	}
}

func TestCodexFileLifecycle(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"/etc/codex", "/usr/local/share/dev-sandbox", "/home/dev/.codex"} {
		if err := os.MkdirAll(root+dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	policy := filepath.Join(root, "host-policy.toml")
	config := filepath.Join(root, "host-config.toml")
	custom := "default_permissions = 'custom'\n# literal $(false) `false`\n"
	personal := "model = 'personal'\n"
	for path, content := range map[string]string{policy: custom, config: personal} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	apply := func(o options) {
		t.Helper()
		if err := o.loadAgentFiles(); err != nil {
			t.Fatal(err)
		}
		v := vm{options: o}
		script, err := v.provision()
		if err != nil {
			t.Fatal(err)
		}
		// Exercise the actual file-installation section against temporary paths,
		// without invoking package installation or needing root privileges.
		header := strings.Join(strings.Split(script, "\n")[:4], "\n")
		section := strings.Split(strings.Split(script, "# BEGIN CODEX FILES\n")[1], "# END CODEX FILES")[0]
		section = strings.ReplaceAll(section, "chown dev:dev", "true")
		if runtime.GOOS == "darwin" {
			section = strings.ReplaceAll(section, "mv -fT", "mv -f")
		}
		for _, path := range []string{"/usr/local/share/dev-sandbox", "/etc/codex", "/home/dev/.codex"} {
			section = strings.ReplaceAll(section, path, root+path)
		}
		if out, err := exec.Command("bash", "-c", "set -euo pipefail\n"+header+"\n"+section).CombinedOutput(); err != nil {
			t.Fatalf("install: %v: %s", err, out)
		}
	}
	want := func(path, value string) {
		t.Helper()
		data, err := os.ReadFile(root + path)
		if err != nil || string(data) != value {
			t.Fatalf("%s: %q %v; want %q", path, data, err, value)
		}
	}
	apply(options{codexRequirements: policy, codexConfig: config})
	want("/etc/codex/requirements.toml", custom)
	want("/home/dev/.codex/config.toml", personal)
	// Original host files need not exist on a subsequent configure.
	os.Remove(policy)
	os.WriteFile(root+"/etc/codex/requirements.toml", []byte("manual edit"), 0600)
	apply(options{})
	want("/etc/codex/requirements.toml", custom)
	want("/home/dev/.codex/config.toml", personal)
	os.WriteFile(config, []byte("model = 'replacement'\n"), 0600)
	apply(options{codexConfig: config})
	want("/home/dev/.codex/config.toml", personal)
	apply(options{codexConfig: config, replaceCodexConfig: true, resetCodexRequirements: true})
	want("/home/dev/.codex/config.toml", "model = 'replacement'\n")
	bundled, _ := recipe.ReadFile("config/codex/requirements.toml")
	want("/etc/codex/requirements.toml", string(bundled))
	apply(options{})
	want("/etc/codex/requirements.toml", string(bundled))
}

func TestClaudeOptions(t *testing.T) {
	for _, args := range [][]string{
		{"verify", "--claude-managed-settings", "p"},
		{"render", "--claude-config", "p"},
		{"create", "--reset-claude-managed-settings"},
		{"configure", "--reset-claude-managed-settings", "--claude-managed-settings", "p"},
		{"configure", "--replace-claude-config"},
		{"create", "--claude-config="},
	} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if _, err := parseOptions([]string{"--claude-managed-settings", "policy", "configure", "--claude-config", "config", "--replace-claude-config"}); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeInputValidation(t *testing.T) {
	for _, data := range []string{"{broken", "[]", `{"sandbox": {"enabled": "yes"}}`} {
		path := filepath.Join(t.TempDir(), "managed-settings.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		o := options{claudeManagedSettings: path}
		if err := o.loadAgentFiles(); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte("[]"), 0600)
	o := options{claudeConfig: path}
	if err := o.loadAgentFiles(); err == nil {
		t.Fatal("accepted non-object settings")
	}
}

func TestClaudeFileLifecycle(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"/etc/claude-code/managed-settings.d", "/usr/local/share/dev-sandbox", "/home/dev/.claude"} {
		if err := os.MkdirAll(root+dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Leftover drop-ins and a managed MCP file must not survive configure.
	os.WriteFile(root+"/etc/claude-code/managed-settings.d/10-loosen.json", []byte("{}"), 0600)
	os.WriteFile(root+"/etc/claude-code/managed-mcp.json", []byte("{}"), 0600)
	policy := filepath.Join(root, "host-policy.json")
	config := filepath.Join(root, "host-config.json")
	custom := "{\"sandbox\": {\"enabled\": true}, \"note\": \"literal $(false) `false`\"}\n"
	personal := "{\"model\": \"personal\"}\n"
	for path, content := range map[string]string{policy: custom, config: personal} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	apply := func(o options) {
		t.Helper()
		if err := o.loadAgentFiles(); err != nil {
			t.Fatal(err)
		}
		v := vm{options: o}
		script, err := v.provision()
		if err != nil {
			t.Fatal(err)
		}
		header := strings.Join(strings.Split(script, "\n")[:6], "\n")
		section := strings.Split(strings.Split(script, "# BEGIN CLAUDE FILES\n")[1], "# END CLAUDE FILES")[0]
		section = strings.ReplaceAll(section, "chown dev:dev", "true")
		if runtime.GOOS == "darwin" {
			section = strings.ReplaceAll(section, "mv -fT", "mv -f")
		}
		for _, path := range []string{"/usr/local/share/dev-sandbox", "/etc/claude-code", "/home/dev/.claude"} {
			section = strings.ReplaceAll(section, path, root+path)
		}
		if out, err := exec.Command("bash", "-c", "set -euo pipefail\n"+header+"\npolicy_dir="+root+"/usr/local/share/dev-sandbox\n"+section).CombinedOutput(); err != nil {
			t.Fatalf("install: %v: %s", err, out)
		}
	}
	want := func(path, value string) {
		t.Helper()
		data, err := os.ReadFile(root + path)
		if err != nil || string(data) != value {
			t.Fatalf("%s: %q %v; want %q", path, data, err, value)
		}
	}
	apply(options{claudeManagedSettings: policy, claudeConfig: config})
	want("/etc/claude-code/managed-settings.json", custom)
	want("/home/dev/.claude/settings.json", personal)
	for _, path := range []string{"/etc/claude-code/managed-settings.d", "/etc/claude-code/managed-mcp.json"} {
		if _, err := os.Lstat(root + path); !os.IsNotExist(err) {
			t.Fatalf("policy drift left behind: %s", path)
		}
	}
	if _, err := os.Stat(root + "/usr/local/share/dev-sandbox/managed-settings.sha256"); err != nil {
		t.Fatal(err)
	}
	os.Remove(policy)
	os.WriteFile(root+"/etc/claude-code/managed-settings.json", []byte("manual edit"), 0600)
	apply(options{})
	want("/etc/claude-code/managed-settings.json", custom)
	want("/home/dev/.claude/settings.json", personal)
	os.WriteFile(config, []byte("{\"model\": \"replacement\"}\n"), 0600)
	apply(options{claudeConfig: config})
	want("/home/dev/.claude/settings.json", personal)
	apply(options{claudeConfig: config, replaceClaudeConfig: true, resetClaudeManagedSettings: true})
	want("/home/dev/.claude/settings.json", "{\"model\": \"replacement\"}\n")
	bundled, _ := recipe.ReadFile("config/claude/managed-settings.json")
	want("/etc/claude-code/managed-settings.json", string(bundled))
	apply(options{})
	want("/etc/claude-code/managed-settings.json", string(bundled))
}

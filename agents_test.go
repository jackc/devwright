package devsandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
		for _, kind := range []string{"managed", "config"} {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			o := options{claudeManagedSettings: path}
			if kind == "config" {
				// Claude Code drops a user file with a wrong-typed value as a whole.
				o = options{claudeConfig: path}
			}
			if err := o.loadAgentFiles(); err == nil {
				t.Fatalf("accepted %s %q", kind, data)
			}
		}
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"sandbox": {"autoAllowBashIfSandboxed": true}, "model": "personal"}`), 0600)
	o := options{claudeConfig: path}
	if err := o.loadAgentFiles(); err != nil {
		t.Fatal(err)
	}
}

// provisionSection runs one agent's file-installation section of the rendered
// provisioning script against temporary paths, without package installation
// or root privileges. The shared shell functions and the prepended assignment
// lines come from the same rendered script, so a missing definition fails
// here exactly as it would in a guest.
func provisionSection(t *testing.T, root string, o options, marker string, paths ...string) {
	t.Helper()
	if err := o.loadAgentFiles(); err != nil {
		t.Fatal(err)
	}
	v := vm{options: o}
	script, err := v.provision()
	if err != nil {
		t.Fatal(err)
	}
	assignment := regexp.MustCompile(`^[a-z_]+=`)
	var header []string
	for _, line := range strings.Split(script, "\n") {
		if !assignment.MatchString(line) {
			break
		}
		header = append(header, line)
	}
	between := func(begin, end string) string {
		t.Helper()
		_, rest, ok := strings.Cut(script, begin)
		if !ok {
			t.Fatalf("missing %q", begin)
		}
		body, _, ok := strings.Cut(rest, end)
		if !ok {
			t.Fatalf("missing %q", end)
		}
		return body
	}
	body := "policy_dir=/usr/local/share/dev-sandbox\n" + between("# BEGIN AGENT FUNCTIONS\n", "# END AGENT FUNCTIONS") +
		between("# BEGIN "+marker+" FILES\n", "# END "+marker+" FILES")
	body = strings.ReplaceAll(body, "chown dev:dev", "true")
	if runtime.GOOS == "darwin" {
		body = strings.ReplaceAll(body, "mv -fT", "mv -f")
	}
	for _, path := range append([]string{"/usr/local/share/dev-sandbox"}, paths...) {
		body = strings.ReplaceAll(body, path, root+path)
	}
	if out, err := exec.Command("bash", "-c", "set -euo pipefail\n"+strings.Join(header, "\n")+"\n"+body).CombinedOutput(); err != nil {
		t.Fatalf("install: %v: %s", err, out)
	}
}

func wantFile(t *testing.T, path, value string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != value {
		t.Fatalf("%s: %q %v; want %q", path, data, err, value)
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
		provisionSection(t, root, o, "CODEX", "/etc/codex", "/home/dev/.codex")
	}
	apply(options{codexRequirements: policy, codexConfig: config})
	wantFile(t, root+"/etc/codex/requirements.toml", custom)
	wantFile(t, root+"/home/dev/.codex/config.toml", personal)
	// Original host files need not exist on a subsequent configure.
	os.Remove(policy)
	os.WriteFile(root+"/etc/codex/requirements.toml", []byte("manual edit"), 0600)
	apply(options{})
	wantFile(t, root+"/etc/codex/requirements.toml", custom)
	wantFile(t, root+"/home/dev/.codex/config.toml", personal)
	os.WriteFile(config, []byte("model = 'replacement'\n"), 0600)
	apply(options{codexConfig: config})
	wantFile(t, root+"/home/dev/.codex/config.toml", personal)
	apply(options{codexConfig: config, replaceCodexConfig: true, resetCodexRequirements: true})
	wantFile(t, root+"/home/dev/.codex/config.toml", "model = 'replacement'\n")
	bundled, _ := recipe.ReadFile("config/codex/requirements.toml")
	wantFile(t, root+"/etc/codex/requirements.toml", string(bundled))
	apply(options{})
	wantFile(t, root+"/etc/codex/requirements.toml", string(bundled))
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
		provisionSection(t, root, o, "CLAUDE", "/etc/claude-code", "/home/dev/.claude")
	}
	apply(options{claudeManagedSettings: policy, claudeConfig: config})
	wantFile(t, root+"/etc/claude-code/managed-settings.json", custom)
	wantFile(t, root+"/home/dev/.claude/settings.json", personal)
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
	wantFile(t, root+"/etc/claude-code/managed-settings.json", custom)
	wantFile(t, root+"/home/dev/.claude/settings.json", personal)
	os.WriteFile(config, []byte("{\"model\": \"replacement\"}\n"), 0600)
	apply(options{claudeConfig: config})
	wantFile(t, root+"/home/dev/.claude/settings.json", personal)
	apply(options{claudeConfig: config, replaceClaudeConfig: true, resetClaudeManagedSettings: true})
	wantFile(t, root+"/home/dev/.claude/settings.json", "{\"model\": \"replacement\"}\n")
	bundled, _ := recipe.ReadFile("config/claude/managed-settings.json")
	wantFile(t, root+"/etc/claude-code/managed-settings.json", string(bundled))
	apply(options{})
	wantFile(t, root+"/etc/claude-code/managed-settings.json", string(bundled))
}

func TestPolicyMode(t *testing.T) {
	for _, tc := range []struct {
		custom string
		reset  bool
		want   string
	}{{"", false, "preserve"}, {"file", false, "custom"}, {"", true, "default"}, {"file", true, "default"}} {
		if got := policyMode(tc.custom, tc.reset); got != tc.want {
			t.Fatalf("policyMode(%q, %v) = %q", tc.custom, tc.reset, got)
		}
	}
}

// The account block refuses to apply root ownership through a dev-planted
// symlink; run that loop from the rendered script against a fixture home.
func TestDevDirectorySymlinkRefused(t *testing.T) {
	v := vm{options: options{}}
	script, err := v.provision()
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(script, "for dev_dir in ")
	if !ok {
		t.Fatal("missing directory guard")
	}
	loop, _, ok := strings.Cut(rest, "\ndone\n")
	if !ok {
		t.Fatal("unterminated directory guard")
	}
	root := t.TempDir()
	os.MkdirAll(root+"/home/dev/.ssh", 0700)
	os.MkdirAll(root+"/etc/claude-code", 0755)
	os.Symlink(root+"/etc/claude-code", root+"/home/dev/.claude")
	body := "set -euo pipefail\nfor dev_dir in " + strings.ReplaceAll(loop, "/home/dev", root+"/home/dev") + "\ndone\n"
	if out, err := exec.Command("bash", "-c", body).CombinedOutput(); err == nil || !strings.Contains(string(out), "Refusing to manage") {
		t.Fatalf("symlink accepted: %v: %s", err, out)
	}
	os.Remove(root + "/home/dev/.claude")
	if out, err := exec.Command("bash", "-c", body).CombinedOutput(); err != nil {
		t.Fatalf("absent directories rejected: %v: %s", err, out)
	}
}

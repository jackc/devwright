package verification

import (
	"bufio"
	"bytes"
	"context"
	"dev-sandbox/internal/claudepolicy"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeVersionParsing(t *testing.T) {
	version, err := parseClaudeVersion("2.1.236 (Claude Code)\n")
	if err != nil || version != [3]int{2, 1, 236} {
		t.Fatalf("%v %v", version, err)
	}
	if !versionAtLeast(version, minimumClaudeVersion) || versionAtLeast([3]int{2, 1, 218}, minimumClaudeVersion) || !versionAtLeast([3]int{3, 0, 0}, minimumClaudeVersion) {
		t.Fatal("version comparison")
	}
	for _, text := range []string{"", "garbage", "2.1 (Claude Code)", "x.y.z (Claude Code)", "2.1.236"} {
		if _, err := parseClaudeVersion(text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
}

func TestClaudePosture(t *testing.T) {
	data, err := os.ReadFile("../../config/claude/managed-settings.json")
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := claudepolicy.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	relaxed, _ := claudepolicy.Parse([]byte(`{"sandbox":{"enabled":false}}`))
	good := `{"statusVersion":2,"enabled":true,"enabledSource":"policy","strictMode":true,"strictModeSource":"policy","filesystemPolicy":"strict"}`
	for _, tc := range []struct {
		name, output string
		policy       claudepolicy.Settings
		ok           bool
	}{
		{"embedded", "warning line\n" + good + "\n", embedded, true},
		{"from settings", strings.Replace(good, `"enabledSource":"policy"`, `"enabledSource":"settings"`, 1), embedded, false},
		{"sandbox off", strings.Replace(good, `"enabled":true`, `"enabled":false`, 1), embedded, false},
		{"escape hatch", strings.Replace(good, `"strictMode":true`, `"strictMode":false`, 1), embedded, false},
		{"relaxed filesystem", strings.Replace(good, `"filesystemPolicy":"strict"`, `"filesystemPolicy":"relaxed"`, 1), embedded, false},
		{"custom relaxed policy", strings.Replace(good, `"enabled":true`, `"enabled":false`, 1), relaxed, true},
		{"not json", "sandbox: on", embedded, false},
		{"legacy status", `{"available":false,"installed":false,"policyLocked":false,"reasons":["x"]}`, embedded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, err := parseSandboxStatus(tc.output)
			if err == nil && tc.name == "legacy status" {
				if status.StatusVersion >= 2 {
					t.Fatal("legacy status treated as a posture report")
				}
				return
			}
			if err == nil {
				err = checkClaudePosture(tc.policy, status)
			}
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestManagedDrift(t *testing.T) {
	dir := t.TempDir()
	if err := checkManagedDrift(dir); err != nil {
		t.Fatal(err)
	}
	os.Mkdir(filepath.Join(dir, "managed-settings.d"), 0755)
	if err := checkManagedDrift(dir); err != nil {
		t.Fatalf("empty drop-in directory: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "managed-settings.d", "10-loosen.json"), []byte("{}"), 0644)
	if err := checkManagedDrift(dir); err == nil {
		t.Fatal("accepted drop-in")
	}
	os.RemoveAll(filepath.Join(dir, "managed-settings.d"))
	os.WriteFile(filepath.Join(dir, "managed-mcp.json"), []byte("{}"), 0644)
	if err := checkManagedDrift(dir); err == nil {
		t.Fatal("accepted managed MCP file")
	}
}

func post(t *testing.T, url, body string) string {
	t.Helper()
	response, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMessageStubProtocol(t *testing.T) {
	stub, err := startStub("sh -c 'echo probe'")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	defer stub.close()
	first := post(t, stub.url()+"/v1/messages?beta=true", `{"model":"stub-model","stream":true,"messages":[{"role":"user","content":"Run the acceptance probe."}]}`)
	if !strings.Contains(first, `"tool_use"`) || !strings.Contains(first, `\"sh -c 'echo probe'\"`) || !strings.Contains(first, `"stop_reason":"tool_use"`) || !strings.HasPrefix(first, "event: message_start\ndata: ") {
		t.Fatalf("first turn: %s", first)
	}
	second := post(t, stub.url()+"/v1/messages", `{"model":"stub-model","stream":true,"messages":[{"role":"user","content":"x"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_stub","name":"Bash","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_stub","content":"PASS text"},{"type":"tool_result","tool_use_id":"toolu_stub","content":[{"type":"text","text":"part one"},{"type":"text","text":" part two"}]}]}]}`)
	if !strings.Contains(second, `"end_turn"`) || !strings.Contains(second, "STUB-DONE") {
		t.Fatalf("second turn: %s", second)
	}
	results := stub.toolResults()
	if len(results) != 2 || results[0] != "PASS text" || results[1] != "part one part two" {
		t.Fatalf("tool results: %q", results)
	}
	if response, err := http.Get(stub.url() + "/v1/messages"); err != nil || response.StatusCode != http.StatusNotFound {
		t.Fatalf("GET: %v %v", response, err)
	}
	if body := post(t, stub.url()+"/v1/messages/count_tokens", `{}`); body != `{"input_tokens":1}` {
		t.Fatalf("count_tokens: %s", body)
	}
}

// Launch this test binary as a fake claude that speaks to the real stub,
// then reports a canned sandbox outcome without touching the host sandbox.
func TestClaudeHelper(t *testing.T) {
	if os.Getenv("DEV_SANDBOX_CLAUDE_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	mode := os.Getenv("TEST_CLAUDE_MODE")
	if len(args) > 0 && args[0] == "--version" {
		os.Stdout.WriteString("2.1.236 (Claude Code)\n")
		os.Exit(0)
	}
	if len(args) > 1 && args[0] == "sandbox" && args[1] == "status" {
		os.Stdout.WriteString(`{"statusVersion":2,"enabled":true,"enabledSource":"policy","strictMode":true,"strictModeSource":"policy","filesystemPolicy":"strict"}` + "\n")
		os.Exit(0)
	}
	override := false
	for _, arg := range args {
		if arg == "--settings" {
			override = true
		}
	}
	base := os.Getenv("ANTHROPIC_BASE_URL")
	if base == "" || os.Getenv("ANTHROPIC_API_KEY") != "synthetic-key" || os.Getenv("CLAUDE_CONFIG_DIR") == "" || os.Getenv("SSH_AUTH_SOCK") != "" {
		os.Stderr.WriteString("unexpected environment\n")
		os.Exit(2)
	}
	response, err := http.Post(base+"/v1/messages?beta=true", "application/json", strings.NewReader(`{"model":"stub-model","stream":true,"messages":[{"role":"user","content":"probe"}]}`))
	if err != nil {
		os.Stderr.WriteString(err.Error())
		os.Exit(2)
	}
	command := ""
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			Delta struct {
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if json.Unmarshal([]byte(line[6:]), &event) == nil && event.Delta.PartialJSON != "" {
			var input struct {
				Command string `json:"command"`
			}
			json.Unmarshal([]byte(event.Delta.PartialJSON), &input)
			command = input.Command
		}
	}
	response.Body.Close()
	if command == "" || !strings.Contains(command, " claude-probe ") {
		os.Stderr.WriteString("no probe command in first turn\n")
		os.Exit(2)
	}
	text := "PASS Claude Code workspace write, outside-workspace write denial, and managed secret read denial\n"
	switch {
	case mode == "probe-failure", mode == "override-accepted" && override:
		text = "managed deny-read did not hold\nExit code 1"
	case mode == "sibling-changed":
		parts := strings.Split(strings.TrimSuffix(command, "'"), "' '")
		os.WriteFile(parts[len(parts)-1], []byte("changed"), 0600)
	}
	body, _ := json.Marshal(map[string]any{"model": "stub-model", "stream": true, "messages": []any{
		map[string]any{"role": "user", "content": "probe"},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "toolu_stub", "name": "Bash", "input": map[string]string{"command": command}}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_stub", "content": text}}},
	}})
	response, err = http.Post(base+"/v1/messages?beta=true", "application/json", bytes.NewReader(body))
	if err != nil {
		os.Stderr.WriteString(err.Error())
		os.Exit(2)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	os.Stdout.WriteString(`{"type":"result","subtype":"success","result":"STUB-DONE"}` + "\n")
	os.Exit(0)
}

func fakeClaude(t *testing.T, mode string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nexec " + quoteArg(executable) + " -test.run='^TestClaudeHelper$' -- \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEV_SANDBOX_CLAUDE_HELPER", "1")
	t.Setenv("TEST_CLAUDE_MODE", mode)
	return path
}

func TestClaudeSessionThroughStub(t *testing.T) {
	if _, err := startStub("x"); err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	claude := fakeClaude(t, "success")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	work := t.TempDir()
	run, err := runClaudeSession(ctx, claude, work, filepath.Join(work, "config"), "'/x/verify' claude-probe '/x/canary' '/x/sibling'")
	if err != nil {
		t.Fatal(err)
	}
	if run.subtype != "success" || len(run.results) != 1 || !strings.HasPrefix(run.results[0], "PASS Claude Code") {
		t.Fatalf("%+v", run)
	}
}

func TestClaudeSandboxFixturesCleanedOnSuccessAndFailure(t *testing.T) {
	if _, err := startStub("x"); err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	for _, mode := range []string{"success", "probe-failure", "override-accepted", "sibling-changed"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			if err := os.Mkdir(filepath.Join(home, "projects"), 0700); err != nil {
				t.Fatal(err)
			}
			claude := fakeClaude(t, mode)
			var out bytes.Buffer
			err := claudeSandboxCheck(claude, home, &out)
			if (err == nil) != (mode == "success") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if mode == "success" && !strings.Contains(out.String(), "PASS managed read denial holds") {
				t.Fatalf("output: %s", out.String())
			}
			if _, err := os.Lstat(filepath.Join(home, ".pgpass")); !os.IsNotExist(err) {
				t.Fatalf("canary left behind: %v", err)
			}
			entries, err := os.ReadDir(filepath.Join(home, "projects"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("fixture left behind: %v %v", entries, err)
			}
		})
	}
	home := t.TempDir()
	os.Mkdir(filepath.Join(home, "projects"), 0700)
	os.WriteFile(filepath.Join(home, ".pgpass"), []byte("existing synthetic content"), 0600)
	if err := claudeSandboxCheck(fakeClaude(t, "success"), home, io.Discard); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing canary: %v", err)
	}
}

func TestNativeClaudeCheckFixtures(t *testing.T) {
	if _, err := startStub("x"); err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	for _, mode := range []string{"success", "probe-failure"} {
		home := t.TempDir()
		os.Mkdir(filepath.Join(home, "projects"), 0700)
		var out bytes.Buffer
		err := nativeClaudeCheck(fakeClaude(t, mode), home, "/x/verify", &out)
		if (err == nil) != (mode == "success") {
			t.Fatalf("mode %s: %v", mode, err)
		}
		entries, _ := os.ReadDir(home)
		for _, entry := range entries {
			if entry.Name() != "projects" {
				t.Fatalf("fixture left in home: %s", entry.Name())
			}
		}
		projects, _ := os.ReadDir(filepath.Join(home, "projects"))
		if len(projects) != 0 {
			t.Fatalf("fixture left behind: %v", projects)
		}
	}
}

func TestClaudeProbeOutcomes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	os.Mkdir(locked, 0500)
	t.Cleanup(func() { os.Chmod(locked, 0700) })
	sibling := filepath.Join(locked, "outside")
	canary := filepath.Join(dir, "canary")
	os.WriteFile(canary, []byte(canaryText), 0600)
	t.Chdir(t.TempDir())
	if err := ClaudeProbe(canary, sibling, io.Discard); err == nil || !strings.Contains(err.Error(), "did not hold") {
		t.Fatalf("readable canary accepted: %v", err)
	}
	os.WriteFile(canary, []byte(""), 0600)
	var out bytes.Buffer
	if err := ClaudeProbe(canary, sibling, &out); err != nil || !strings.Contains(out.String(), "managed secret read denial") {
		t.Fatalf("masked canary: %v %s", err, out.String())
	}
	out.Reset()
	if err := ClaudeProbe("-", sibling, &out); err != nil || !strings.Contains(out.String(), "PASS Claude Code workspace write and outside-workspace write denial") {
		t.Fatalf("no canary: %v %s", err, out.String())
	}
	if err := ClaudeProbe("-", filepath.Join(dir, "writable-outside"), io.Discard); err == nil {
		t.Fatal("accepted a writable outside-workspace path")
	}
}

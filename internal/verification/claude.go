package verification

import (
	"bytes"
	"context"
	"crypto/sha256"
	"github.com/jackc/devwright/internal/claudepolicy"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The managed keys the embedded policy relies on exist from this release.
var minimumClaudeVersion = [3]int{2, 1, 219}

const claudeManagedDir = "/etc/claude-code"

// Releases before the second status format print only install fields on
// Linux; the posture comparison needs statusVersion 2.
type sandboxStatus struct {
	StatusVersion    int    `json:"statusVersion"`
	Enabled          bool   `json:"enabled"`
	EnabledSource    string `json:"enabledSource"`
	StrictMode       bool   `json:"strictMode"`
	StrictModeSource string `json:"strictModeSource"`
	FilesystemPolicy string `json:"filesystemPolicy"`
}

// parseClaudeVersion reads "2.1.236 (Claude Code)".
func parseClaudeVersion(text string) ([3]int, error) {
	var version [3]int
	fields := strings.Fields(text)
	if len(fields) < 2 || fields[1] != "(Claude" {
		return version, fmt.Errorf("unexpected Claude Code version output: %q", strings.TrimSpace(text))
	}
	parts := strings.Split(fields[0], ".")
	if len(parts) != 3 {
		return version, fmt.Errorf("unexpected Claude Code version: %q", fields[0])
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return version, fmt.Errorf("unexpected Claude Code version: %q", fields[0])
		}
		version[i] = n
	}
	return version, nil
}

func versionAtLeast(version, minimum [3]int) bool {
	for i := range version {
		if version[i] != minimum[i] {
			return version[i] > minimum[i]
		}
	}
	return true
}

// The status command prints one JSON line; earlier lines may carry warnings.
func parseSandboxStatus(output string) (sandboxStatus, error) {
	var status sandboxStatus
	lines := strings.Split(strings.TrimSpace(output), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	if err := json.Unmarshal([]byte(line), &status); err != nil {
		return status, fmt.Errorf("unexpected sandbox status output: %q", line)
	}
	return status, nil
}

func claudeSandboxStatus(claude, dir string) (sandboxStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	stdout, stderr, err := runWith(ctx, dir, scrubbedEnvironment(), claude, "sandbox", "status")
	if err != nil {
		return sandboxStatus{}, fmt.Errorf("sandbox status: %w: %s", err, stderr)
	}
	return parseSandboxStatus(stdout)
}

// checkClaudePosture compares the reported posture with the selected policy,
// the counterpart of reading Codex's managed requirements back.
func checkClaudePosture(policy claudepolicy.Settings, status sandboxStatus) error {
	if policy.SandboxEnabled() {
		if !status.Enabled || status.EnabledSource != "policy" {
			return errors.New("Managed sandbox policy is not in effect")
		}
		want := "strict"
		if !policy.FilesystemIsolated() {
			want = "relaxed"
		}
		if status.FilesystemPolicy != want {
			return errors.New("Unexpected filesystem isolation policy")
		}
	}
	if policy.StrictSandbox() && (!status.StrictMode || status.StrictModeSource != "policy") {
		return errors.New("Managed strict sandbox mode is not in effect")
	}
	return nil
}

// A drop-in can replace single values and a managed MCP file adds servers;
// the recipe installs neither, so their presence is policy drift.
func checkManagedDrift(dir string) error {
	entries, err := os.ReadDir(filepath.Join(dir, "managed-settings.d"))
	if err == nil && len(entries) > 0 {
		return errors.New("Managed settings drop-ins present; the recipe installs a single policy file")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Lstat(filepath.Join(dir, "managed-mcp.json")); err == nil {
		return errors.New("Managed MCP configuration present; the recipe installs none")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Preserved or subsequently edited user settings must still be valid. The
// separate sandbox probe uses explicit settings and cannot detect a user file
// Claude silently discarded.
func checkClaudeUserSettings(home string) error {
	path := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := claudepolicy.Validate(data); err != nil {
		return fmt.Errorf("invalid Claude Code user settings %s: %w", path, err)
	}
	return nil
}

func checkClaude(home string, out io.Writer) error {
	claude, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("Claude Code installation: %w", err)
	}
	if claude != "/usr/bin/claude" {
		return fmt.Errorf("Unexpected Claude Code executable: %s", claude)
	}
	stdout, stderr, err := probe(claude, "--version")
	if err != nil {
		return fmt.Errorf("Claude Code version check failed: %v: %s", err, stderr)
	}
	version, err := parseClaudeVersion(stdout)
	if err != nil {
		return err
	}
	if !versionAtLeast(version, minimumClaudeVersion) {
		return fmt.Errorf("Claude Code %s is older than the %d.%d.%d policy floor", strings.TrimSpace(stdout), minimumClaudeVersion[0], minimumClaudeVersion[1], minimumClaudeVersion[2])
	}
	fmt.Fprintf(out, "Claude Code installed: %s\n", strings.TrimSpace(stdout))
	if err := checkClaudeUserSettings(home); err != nil {
		return err
	}
	expected, err := os.ReadFile("/usr/local/share/devwright/managed-settings.sha256")
	if err != nil {
		return err
	}
	policy, err := os.ReadFile(filepath.Join(claudeManagedDir, "managed-settings.json"))
	if err != nil {
		return err
	}
	fields := strings.Fields(string(expected))
	if len(fields) == 0 || fmt.Sprintf("%x", sha256.Sum256(policy)) != fields[0] {
		return errors.New("Managed Claude policy differs from provisioned recipe")
	}
	if err := checkManagedDrift(claudeManagedDir); err != nil {
		return err
	}
	selected, err := claudepolicy.Parse(policy)
	if err != nil {
		return fmt.Errorf("managed settings: %w", err)
	}
	var failures []error
	status, err := claudeSandboxStatus(claude, filepath.Join(home, "projects"))
	if err != nil {
		fmt.Fprintf(out, "ERROR Claude Code sandbox posture: %v\n", err)
		failures = append(failures, err)
	} else if status.StatusVersion >= 2 {
		if err := checkClaudePosture(selected, status); err != nil {
			fmt.Fprintf(out, "FAIL Claude Code sandbox posture: %v\n", err)
			failures = append(failures, err)
		} else {
			fmt.Fprintln(out, "PASS Claude Code managed policy ownership, checksum, drop-in absence, and reported sandbox posture")
		}
	} else {
		fmt.Fprintln(out, "PASS Claude Code managed policy ownership, checksum, and drop-in absence")
		fmt.Fprintln(out, "NOT TESTED: reported sandbox posture; this Claude Code release prints no posture fields on Linux")
	}
	if err := checkSandboxProfile(out, "/etc/apparmor.d", "/usr/local/share/devwright/bwrap-profile.sha256", "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"); err != nil {
		fmt.Fprintf(out, "FAIL bwrap AppArmor profile: %v\n", err)
		failures = append(failures, err)
	}
	failures = append(failures, claudeBehaviorCheck(claude, home, policy, out))
	return errors.Join(failures...)
}

// checkSandboxProfile compares the AppArmor state with what provisioning
// recorded: the documented bwrap profile unchanged, the stock restrictive
// profile disabled, and the global user-namespace restriction still on. Both
// agents' bubblewrap sandboxes depend on that state.
func checkSandboxProfile(out io.Writer, apparmorDir, sumPath, sysctlPath string) error {
	expected, err := os.ReadFile(sumPath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(out, "NOT TESTED: bwrap AppArmor profile; provisioning found no AppArmor")
		return nil
	}
	if err != nil {
		return err
	}
	profile, err := os.ReadFile(filepath.Join(apparmorDir, "bwrap"))
	if err != nil {
		return err
	}
	fields := strings.Fields(string(expected))
	if len(fields) == 0 || fmt.Sprintf("%x", sha256.Sum256(profile)) != fields[0] {
		return errors.New("bwrap AppArmor profile differs from provisioned recipe")
	}
	restrict, err := os.ReadFile(sysctlPath)
	if err == nil && strings.TrimSpace(string(restrict)) != "1" {
		return errors.New("Unprivileged user namespace restriction is disabled")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Lstat(filepath.Join(apparmorDir, "bwrap-userns-restrict")); err == nil {
		if _, err := os.Lstat(filepath.Join(apparmorDir, "disable", "bwrap-userns-restrict")); err != nil {
			return errors.New("Stock bwrap-userns-restrict AppArmor profile is not disabled")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintln(out, "PASS bwrap AppArmor profile matches the recipe, stock profile disabled, user namespace restriction enabled")
	return nil
}

// messageStub answers the Messages API on loopback: the first turn asks for
// one Bash command, the turn carrying its result ends the session. No model
// or credential is involved; Claude Code applies its ordinary sandbox.
type messageStub struct {
	server   *http.Server
	listener net.Listener
	command  string
	mu       sync.Mutex
	results  []string
}

func startStub(command string) (*messageStub, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("loopback stub listener: %w", err)
	}
	s := &messageStub{listener: listener, command: command}
	s.server = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go s.server.Serve(listener)
	return s, nil
}

func (s *messageStub) url() string { return "http://" + s.listener.Addr().String() }
func (s *messageStub) close()      { s.server.Close() }
func (s *messageStub) toolResults() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.results...)
}

func (s *messageStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if !strings.Contains(r.URL.Path, "/messages") || strings.Contains(r.URL.Path, "count_tokens") {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"input_tokens":1}`)
		return
	}
	var request struct {
		Model    string `json:"model"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &request)
	results := toolResults(request.Messages)
	if len(results) > 0 {
		// Each request carries the whole conversation, so keep the latest
		// view rather than appending the same results across retries.
		s.mu.Lock()
		s.results = results
		s.mu.Unlock()
	}
	usage := map[string]int{"input_tokens": 1, "output_tokens": 1}
	start := map[string]any{"id": "msg_stub", "type": "message", "role": "assistant", "model": request.Model,
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": usage}
	var events []map[string]any
	if len(results) == 0 {
		input, _ := json.Marshal(map[string]string{"command": s.command, "description": "sandbox acceptance probe"})
		events = []map[string]any{
			{"type": "message_start", "message": start},
			{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_stub", "name": "Bash", "input": map[string]any{}}},
			{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(input)}},
			{"type": "content_block_stop", "index": 0},
			{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use", "stop_sequence": nil}, "usage": usage},
			{"type": "message_stop"},
		}
	} else {
		events = []map[string]any{
			{"type": "message_start", "message": start},
			{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}},
			{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "STUB-DONE"}},
			{"type": "content_block_stop", "index": 0},
			{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": usage},
			{"type": "message_stop"},
		}
	}
	var payload bytes.Buffer
	for _, event := range events {
		data, _ := json.Marshal(event)
		fmt.Fprintf(&payload, "event: %s\ndata: %s\n\n", event["type"], data)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Content-Length", strconv.Itoa(payload.Len()))
	w.Write(payload.Bytes())
}

// toolResults extracts the text of tool_result blocks; content may be a
// string or a list of text parts.
func toolResults(messages []struct {
	Content json.RawMessage `json:"content"`
}) []string {
	var results []string
	for _, message := range messages {
		var blocks []struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			if block.Type != "tool_result" {
				continue
			}
			var text string
			if json.Unmarshal(block.Content, &text) != nil {
				var parts []struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(block.Content, &parts)
				for _, part := range parts {
					text += part.Text
				}
			}
			results = append(results, text)
		}
	}
	return results
}

type claudeRun struct {
	results []string
	subtype string
}

// scrubbedEnvironment keeps only the variables a session needs from the
// account's login environment. Credentials, proxy settings, and provider
// selectors exported by dotfiles or credentials.sh stay out, so the loopback
// stub is the only endpoint a probe session can reach.
func scrubbedEnvironment() []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "HOME", "PATH", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "LC_CTYPE", "TERM", "TMPDIR", "TZ":
			env = append(env, entry)
		}
	}
	return env
}

// claudeEnvironment points a session at the stub with a synthetic key and
// keeps its state inside the fixture's configuration directory.
func claudeEnvironment(base, configDir string) []string {
	return append(scrubbedEnvironment(), "ANTHROPIC_API_KEY=synthetic-key", "ANTHROPIC_BASE_URL="+base, "CLAUDE_CONFIG_DIR="+configDir,
		"DISABLE_AUTOUPDATER=1", "DISABLE_UPDATES=1", "DISABLE_TELEMETRY=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
}

// checkProbeRun turns a session into a verdict. Only the probe's own report
// distinguishes a policy failure from a session that never ran the probe.
func checkProbeRun(run claudeRun, override bool) error {
	text := strings.Join(run.results, "\n")
	switch {
	case override && strings.Contains(text, "managed deny-read did not hold"):
		return fmt.Errorf("managed read denial did not hold against a lower-scope allowRead override: %s", text)
	case strings.Contains(text, "did not hold"):
		return fmt.Errorf("Claude sandbox enforcement failed: %s", text)
	case run.subtype != "success" || !strings.Contains(text, "PASS Claude Code"):
		return fmt.Errorf("Claude sandbox probe did not run to completion (%s); check bubblewrap and socat, the bwrap AppArmor profile, and for Incus containers sandbox.enableWeakerNestedSandbox: %s", run.subtype, text)
	}
	return nil
}

// runClaudeSession drives one non-interactive session through the stub.
// --bare reads no sign-in or keychain; managed settings still apply.
func runClaudeSession(ctx context.Context, claude, dir, configDir, command string, extra ...string) (claudeRun, error) {
	stub, err := startStub(command)
	if err != nil {
		return claudeRun{}, err
	}
	defer stub.close()
	// --allowedTools pre-approves Bash, so nothing prompts in print mode.
	args := append([]string{claude, "-p", "--bare", "--model", "stub-model", "--allowedTools", "Bash",
		"--max-turns", "3", "--output-format", "json"}, extra...)
	args = append(args, "Run the acceptance probe.")
	stdout, stderr, err := runWith(ctx, dir, claudeEnvironment(stub.url(), configDir), args...)
	if err != nil {
		return claudeRun{results: stub.toolResults()}, fmt.Errorf("claude session: %w: %s%s", err, stderr, stdout)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	var result struct {
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &result); err != nil {
		return claudeRun{}, fmt.Errorf("unexpected claude session output: %s", stdout)
	}
	return claudeRun{results: stub.toolResults(), subtype: result.Subtype}, nil
}

func quoteArg(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// nativeClaudeCheck exercises the account's Claude Code with explicit sandbox
// settings, so the probe tests the mechanism rather than the editable defaults.
func nativeClaudeCheck(claude, home, exe string, out io.Writer) error {
	fixture, err := os.MkdirTemp(filepath.Join(home, "projects"), "verify-native-claude-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(fixture)
	work, config := filepath.Join(fixture, "workspace"), filepath.Join(fixture, "config")
	for _, dir := range []string{work, config} {
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
	}
	sibling := filepath.Join(home, filepath.Base(fixture)+"-outside")
	file, err := os.OpenFile(sibling, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.WriteString("original")
	closeErr := file.Close()
	defer os.Remove(sibling)
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	command := quoteArg(exe) + " claude-probe - " + quoteArg(sibling)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	settings := `{"sandbox":{"enabled":true,"failIfUnavailable":true,"allowUnsandboxedCommands":false,"autoAllowBashIfSandboxed":true}}`
	run, err := runClaudeSession(ctx, claude, work, config, command, "--settings", settings)
	if err != nil {
		return fmt.Errorf("Claude Code workspace sandbox (check host bubblewrap/socat, the bwrap AppArmor profile on Linux, or Seatbelt support): %w", err)
	}
	if err := checkProbeRun(run, false); err != nil {
		return fmt.Errorf("Claude Code workspace sandbox: %w", err)
	}
	text := strings.Join(run.results, "\n")
	data, err := os.ReadFile(sibling)
	if err != nil {
		return err
	}
	if string(data) != "original" {
		return errors.New("outside-workspace file changed")
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "PASS ") {
			fmt.Fprintln(out, line)
		}
	}
	return nil
}

// ClaudeProbe runs inside Claude Code's Bash sandbox with synthetic paths only.
// A canary of "-" skips the read check (editable native defaults).
func ClaudeProbe(canary, sibling string, out io.Writer) error {
	if err := os.WriteFile("workspace-write-ok", []byte("ok"), 0600); err != nil {
		return err
	}
	if canary != "-" {
		// Linux masks denied files rather than failing the open; both count.
		data, err := os.ReadFile(canary)
		if err == nil && string(data) == canaryText {
			return errors.New("managed deny-read did not hold")
		}
		if err != nil && !errors.Is(err, os.ErrPermission) && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("unexpected canary read error: %v", err)
		}
	}
	if err := os.WriteFile(sibling, []byte("changed"), 0600); !errors.Is(err, os.ErrPermission) && !errors.Is(err, syscall.EROFS) {
		return fmt.Errorf("outside-workspace write denial did not hold: %v", err)
	}
	if canary == "-" {
		fmt.Fprintln(out, "PASS Claude Code workspace write and outside-workspace write denial")
		return nil
	}
	fmt.Fprintln(out, "PASS Claude Code workspace write, outside-workspace write denial, and managed secret read denial")
	return nil
}

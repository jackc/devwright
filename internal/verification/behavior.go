package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"devwright/internal/codexpolicy"
	"github.com/pelletier/go-toml/v2"
)

const observationPrefix = "DEVWRIGHT_OBSERVATION "

type observation struct {
	Workspace string `json:"workspace"`
	Outside   string `json:"outside"`
	Secret    string `json:"secret"`
}

// AccessProbe measures access without treating permissive behavior as an error.
// The caller supplies synthetic paths only; '-' never opens a real secret.
func AccessProbe(canary, sibling string, out io.Writer) error {
	classify := func(err error) string {
		if err == nil {
			return "allowed"
		}
		if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EROFS) {
			return "denied"
		}
		return "error: " + err.Error()
	}
	result := observation{Workspace: classify(os.WriteFile("workspace-write-ok", []byte("ok"), 0600)), Outside: classify(os.WriteFile(sibling, []byte("changed"), 0600)), Secret: "skipped"}
	if canary != "-" {
		data, err := os.ReadFile(canary)
		result.Secret = classify(err)
		// Linux sandboxes may hide or mask a denied file instead of refusing open.
		if errors.Is(err, os.ErrNotExist) || (err == nil && string(data) != canaryText) {
			result.Secret = "denied"
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, observationPrefix+string(data))
	return err
}

func parseObservation(text string) (observation, error) {
	var result observation
	found := false
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, observationPrefix) {
			continue
		}
		if found {
			return result, errors.New("duplicate access report")
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, observationPrefix)), &result); err != nil {
			return result, err
		}
		found = true
	}
	if !found {
		return result, errors.New("access probe did not run to completion")
	}
	if result.Workspace == "skipped" || result.Outside == "skipped" {
		return result, errors.New("write probes cannot be skipped")
	}
	for _, value := range []string{result.Workspace, result.Outside, result.Secret} {
		if value != "allowed" && value != "denied" && value != "skipped" && !strings.HasPrefix(value, "error: ") {
			return result, errors.New("invalid access report")
		}
	}
	return result, nil
}

func reportObservation(out io.Writer, agent string, got, want observation) error {
	var failures []error
	for _, row := range []struct{ name, got, want string }{
		{"workspace write", got.Workspace, want.Workspace}, {"outside-workspace write", got.Outside, want.Outside}, {"synthetic ~/.pgpass read", got.Secret, want.Secret},
	} {
		status, assessment := "OBSERVED", "expectation unknown"
		switch {
		case row.got == "skipped":
			status, assessment = "SKIPPED", "existing ~/.pgpass preserved; no secrets read"
		case strings.HasPrefix(row.got, "error: "):
			status, assessment = "ERROR", "probe failed to execute"
			failures = append(failures, fmt.Errorf("%s %s: %s", agent, row.name, row.got))
		case row.want != "" && row.want != row.got:
			status, assessment = "FAIL", "contradicts policy; expected "+row.want
			failures = append(failures, fmt.Errorf("%s %s: expected %s, observed %s", agent, row.name, row.want, row.got))
		case row.want != "":
			status, assessment = "PASS", "matches policy"
		}
		fmt.Fprintf(out, "%s %s %s: %s (%s)\n", status, agent, row.name, row.got, assessment)
	}
	return errors.Join(failures...)
}

type behaviorFixture struct{ work, config, sibling, canary string }

func withBehaviorFixture(home string, action func(behaviorFixture) error) error {
	root, err := os.MkdirTemp(filepath.Join(home, "projects"), "verify-access-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	f := behaviorFixture{filepath.Join(root, "workspace"), filepath.Join(root, "config"), filepath.Join(root, "outside-workspace"), "-"}
	for _, path := range []string{f.work, f.config} {
		if err := os.Mkdir(path, 0700); err != nil {
			return err
		}
	}
	if err := os.WriteFile(f.sibling, []byte("original"), 0600); err != nil {
		return err
	}
	canary := filepath.Join(home, ".pgpass")
	file, err := os.OpenFile(canary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		defer os.Remove(canary)
		_, writeErr := file.WriteString(canaryText)
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return err
		}
		f.canary = canary
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	return action(f)
}

func object(value any) map[string]any { result, _ := value.(map[string]any); return result }

func deniesCanary(value any, home string) bool {
	paths, _ := value.([]any)
	for _, path := range paths {
		if path == "~/.pgpass" || path == filepath.Join(home, ".pgpass") || path == home || path == "~" || path == "/" {
			return true
		}
	}
	return false
}

// Infer only expectations supported by known policy shapes. Complex rules and
// lower-scope settings are measured without pretending to resolve their semantics.
func claudeExpectations(policy []byte, home string) (observation, bool, error) {
	var root map[string]any
	if err := json.Unmarshal(policy, &root); err != nil {
		return observation{}, false, err
	}
	sandbox := object(root["sandbox"])
	fs := object(sandbox["filesystem"])
	want := observation{}
	if sandbox["enabled"] == false || fs["disabled"] == true {
		return observation{Workspace: "allowed", Outside: "allowed", Secret: "allowed"}, false, nil
	}
	if sandbox["enabled"] != true {
		return want, false, nil
	}
	simple := true
	for key := range fs {
		if key != "denyRead" && key != "allowManagedReadPathsOnly" && key != "disabled" {
			simple = false
		}
	}
	if simple {
		want.Workspace, want.Outside = "allowed", "denied"
	}
	if deniesCanary(fs["denyRead"], home) && fs["allowRead"] == nil {
		want.Secret = "denied"
	}
	return want, want.Secret == "denied" && fs["allowManagedReadPathsOnly"] == true, nil
}

func codexExpectations(policy, _ []byte, home string) (observation, error) {
	var root map[string]any
	if err := toml.Unmarshal(policy, &root); err != nil {
		return observation{}, err
	}
	// Without a managed default, the effective profile is a user preference.
	// Measure access without treating that preference as a policy violation.
	want := observation{}
	permissions := object(root["permissions"])
	profile, _ := root["default_permissions"].(string)
	selected := object(permissions[profile])
	if profile == ":workspace" || (selected["extends"] == ":workspace" && len(selected) == 1) {
		want = observation{"allowed", "denied", "allowed"}
	}
	if deniesCanary(object(permissions["filesystem"])["deny_read"], home) {
		want.Secret = "denied"
	}
	return want, nil
}

func claudeBehaviorCheck(claude, home string, policy []byte, out io.Writer) error {
	want, locked, err := claudeExpectations(policy, home)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return withBehaviorFixture(home, func(f behaviorFixture) error {
		var failures []error
		for _, override := range []bool{false, true} {
			label := "Claude Code"
			var extra []string
			expected := want
			if override {
				label += " with lower-scope allowRead override"
				expected.Secret = ""
				if locked {
					expected.Secret = "denied"
				}
				if f.canary == "-" {
					fmt.Fprintln(out, "SKIPPED Claude Code read override: existing ~/.pgpass preserved")
					continue
				}
				extra = []string{"--settings", `{"sandbox":{"filesystem":{"allowRead":["~/.pgpass"]}}}`}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			result, runErr := runClaudeSession(ctx, claude, f.work, f.config, quoteArg(exe)+" access-probe "+quoteArg(f.canary)+" "+quoteArg(f.sibling), extra...)
			cancel()
			got, parseErr := parseObservation(strings.Join(result.results, "\n"))
			if parseErr != nil {
				err := errors.Join(runErr, parseErr)
				fmt.Fprintf(out, "ERROR %s behavior: %v\n", label, err)
				failures = append(failures, err)
				continue
			}
			failures = append(failures, reportObservation(out, label, got, expected))
			if runErr != nil {
				fmt.Fprintf(out, "ERROR %s session: %v\n", label, runErr)
				failures = append(failures, runErr)
			} else if result.subtype != "success" {
				err := fmt.Errorf("%s session incomplete: %s", label, result.subtype)
				fmt.Fprintf(out, "ERROR %v\n", err)
				failures = append(failures, err)
			}
		}
		return errors.Join(failures...)
	})
}

func codexBehaviorCheck(home string, policy, bundled []byte, selected codexpolicy.Requirements, out io.Writer) error {
	want, err := codexExpectations(policy, bundled, home)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return withBehaviorFixture(home, func(f behaviorFixture) error {
		var failures []error
		for _, override := range []bool{false, true} {
			label := "Codex"
			args := []string{"codex", "sandbox", "--include-managed-config", "-C", f.work}
			if selected.Default != "" {
				args = append(args, "-P", selected.Default)
			} else {
				// --include-managed-config requires an explicit profile in Codex
				// 0.153.4. Probe the built-in workspace profile without imposing
				// it as the user's selected mode.
				args = append(args, "-P", ":workspace")
				label += " built-in :workspace"
			}
			expected := want
			if override {
				if f.canary == "-" {
					fmt.Fprintln(out, "SKIPPED Codex read override: existing ~/.pgpass preserved")
					continue
				}
				if selected.Default == "" {
					fmt.Fprintln(out, "SKIPPED Codex read override: no managed default profile to override")
					continue
				}
				label += " with lower-scope read override"
				key, _ := json.Marshal(selected.Default)
				args = append(args, "-c", `permissions.`+string(key)+`.filesystem."~/.pgpass"="read"`)
				expected = want
			}
			args = append(args, exe, "access-probe", f.canary, f.sibling)
			stdout, stderr, runErr := probe(args...)
			if override && runErr != nil && strings.Contains(stderr, "conflicts with a config-defined profile") {
				fmt.Fprintln(out, "PASS Codex lower-scope read override: rejected before execution (managed profile preserved)")
				continue
			}
			got, parseErr := parseObservation(stdout)
			if parseErr != nil {
				err := fmt.Errorf("%s behavior: %w: %s", label, errors.Join(runErr, parseErr), stderr)
				fmt.Fprintf(out, "ERROR %v\n", err)
				failures = append(failures, err)
				continue
			}
			failures = append(failures, reportObservation(out, label, got, expected))
			if runErr != nil {
				err := fmt.Errorf("%s session: %w: %s", label, runErr, stderr)
				fmt.Fprintf(out, "ERROR %v\n", err)
				failures = append(failures, err)
			}
		}
		return errors.Join(failures...)
	})
}

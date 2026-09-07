package verification

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"devwright/internal/codexpolicy"
)

func TestBehaviorExpectations(t *testing.T) {
	home := "/home/dev"
	for _, tc := range []struct {
		policy string
		want   observation
		locked bool
	}{
		{`{"sandbox":{"enabled":true,"filesystem":{"denyRead":["~/.pgpass"],"allowManagedReadPathsOnly":true}}}`, observation{"allowed", "denied", "denied"}, true},
		{`{ "sandbox": { "filesystem": {"denyRead": ["~/.pgpass"], "allowManagedReadPathsOnly": true}, "enabled": true }, "model":"custom" }`, observation{"allowed", "denied", "denied"}, true},
		{`{"sandbox":{"enabled":true,"filesystem":{"allowWrite":["/home/dev"]}}}`, observation{}, false},
		{`{"sandbox":{"enabled":true}}`, observation{"allowed", "denied", ""}, false},
		{`{"sandbox":{"enabled":false}}`, observation{"allowed", "allowed", "allowed"}, false},
	} {
		got, locked, err := claudeExpectations([]byte(tc.policy), home)
		if err != nil || got != tc.want || locked != tc.locked {
			t.Fatalf("%s: %+v %v %v", tc.policy, got, locked, err)
		}
	}
	bundled, err := os.ReadFile("../../config/codex/requirements.toml")
	if err != nil {
		t.Fatal(err)
	}
	custom := "default_permissions = 'custom'\n[permissions.custom]\nextends = ':workspace'\n# formatting change\n"
	if got, err := codexExpectations(bundled, bundled, home); err != nil || got != (observation{}) {
		t.Fatalf("feature-only policy must leave filesystem expectations unknown: %+v %v", got, err)
	}
	got, err := codexExpectations([]byte(custom), bundled, home)
	if err != nil || got != (observation{"allowed", "denied", "allowed"}) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestBehaviorReportAllRows(t *testing.T) {
	var out bytes.Buffer
	err := reportObservation(&out, "test", observation{"denied", "allowed", "allowed"}, observation{"allowed", "denied", ""})
	if err == nil || strings.Count(out.String(), "FAIL") != 2 || !strings.Contains(out.String(), "expectation unknown") {
		t.Fatalf("%s %v", &out, err)
	}
	out.Reset()
	if err := reportObservation(&out, "test", observation{"allowed", "allowed", "skipped"}, observation{"allowed", "allowed", "denied"}); err != nil || !strings.Contains(out.String(), "SKIPPED") {
		t.Fatalf("%s %v", &out, err)
	}
	for _, text := range []string{"", observationPrefix + `{}`, observationPrefix + `{"workspace":"allowed","outside":"denied","secret":"denied"}` + "\n" + observationPrefix + `{}`} {
		if _, err := parseObservation(text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
}

func TestBehaviorFixturePreservesExistingSecret(t *testing.T) {
	for _, exists := range []bool{false, true} {
		home := t.TempDir()
		os.Mkdir(filepath.Join(home, "projects"), 0700)
		path := filepath.Join(home, ".pgpass")
		if exists {
			os.WriteFile(path, []byte("personal"), 0600)
		}
		sentinel := errors.New("failed session")
		err := withBehaviorFixture(home, func(f behaviorFixture) error {
			if (f.canary == "-") != exists {
				t.Fatalf("wrong canary %q", f.canary)
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatal(err)
		}
		entries, _ := os.ReadDir(filepath.Join(home, "projects"))
		if len(entries) != 0 {
			t.Fatal("fixtures leaked")
		}
		data, err := os.ReadFile(path)
		if exists {
			if err != nil || string(data) != "personal" {
				t.Fatal("personal secret changed")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal("canary leaked")
		}
	}
}

func TestAccessProbeHelper(t *testing.T) {
	if os.Getenv("DEVWRIGHT_ACCESS_HELPER") != "1" {
		return
	}
	if err := AccessProbe(os.Args[len(os.Args)-2], os.Args[len(os.Args)-1], os.Stdout); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestAccessProbeMeasuresPermissiveFilesystem(t *testing.T) {
	root := t.TempDir()
	canary := filepath.Join(root, "canary")
	sibling := filepath.Join(root, "sibling")
	os.WriteFile(canary, []byte(canaryText), 0600)
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "-test.run=^TestAccessProbeHelper$", "--", canary, sibling)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DEVWRIGHT_ACCESS_HELPER=1")
	stdout, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v", stdout, err)
	}
	got, err := parseObservation(string(stdout))
	if err != nil || got != (observation{"allowed", "allowed", "allowed"}) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestClaudeBehaviorSessions(t *testing.T) {
	requireLoopback(t)
	for _, tc := range []struct {
		mode, policy string
		fail         bool
	}{
		{"success", `{"sandbox":{"enabled":true,"filesystem":{"denyRead":["~/.pgpass"],"allowManagedReadPathsOnly":true}}}`, false},
		{"permissive", `{"sandbox":{"enabled":false}}`, false},
		{"override-accepted", `{"sandbox":{"enabled":true,"filesystem":{"denyRead":["~/.pgpass"],"allowManagedReadPathsOnly":true}}}`, true},
		{"probe-failure", `{"sandbox":{"enabled":true}}`, true},
		{"no-probe", `{}`, true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			home := t.TempDir()
			os.Mkdir(filepath.Join(home, "projects"), 0700)
			var out bytes.Buffer
			err := claudeBehaviorCheck(fakeClaude(t, tc.mode), home, []byte(tc.policy), &out)
			if (err != nil) != tc.fail || !strings.Contains(out.String(), "with lower-scope allowRead override") {
				t.Fatalf("%s %v", &out, err)
			}
			entries, _ := os.ReadDir(filepath.Join(home, "projects"))
			if len(entries) != 0 {
				t.Fatal("fixtures leaked")
			}
			if _, err := os.Lstat(filepath.Join(home, ".pgpass")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("canary leaked")
			}
		})
	}
}

func TestCodexBehaviorCustomProfileAndContinuation(t *testing.T) {
	home := t.TempDir()
	os.Mkdir(filepath.Join(home, "projects"), 0700)
	bin := t.TempDir()
	log := filepath.Join(bin, "args")
	data, _ := json.Marshal(observation{"denied", "allowed", "allowed"})
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + quoteArg(log) + "\ncase \"$*\" in *'permissions.'*) echo 'conflicts with a config-defined profile' >&2; exit 1;; esac\nprintf '%s\\n' " + quoteArg(observationPrefix+string(data)) + "\n"
	os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	bundled, _ := os.ReadFile("../../config/codex/requirements.toml")
	policy := []byte("default_permissions = 'custom'\n[permissions.custom]\nextends = ':workspace'\n")
	var out bytes.Buffer
	err := codexBehaviorCheck(home, policy, bundled, codexpolicy.Requirements{Default: "custom"}, &out)
	if err == nil || !strings.Contains(out.String(), "rejected before execution") || strings.Count(out.String(), "FAIL") != 2 {
		t.Fatalf("%s %v", &out, err)
	}
	args, _ := os.ReadFile(log)
	if !strings.Contains(string(args), "-P custom") || strings.Count(string(args), "access-probe") != 2 {
		t.Fatalf("%s", args)
	}
}

func TestClaudeBehaviorExistingSecretStillTestsWrites(t *testing.T) {
	requireLoopback(t)
	home := t.TempDir()
	os.Mkdir(filepath.Join(home, "projects"), 0700)
	secret := filepath.Join(home, ".pgpass")
	os.WriteFile(secret, []byte("personal"), 0600)
	var out bytes.Buffer
	err := claudeBehaviorCheck(fakeClaude(t, "success"), home, []byte(`{"sandbox":{"enabled":true,"filesystem":{"denyRead":["~/.pgpass"],"allowManagedReadPathsOnly":true}}}`), &out)
	if err != nil || strings.Count(out.String(), "PASS") != 2 || strings.Count(out.String(), "SKIPPED") != 2 {
		t.Fatalf("%s %v", &out, err)
	}
	data, err := os.ReadFile(secret)
	if err != nil || string(data) != "personal" {
		t.Fatal("existing secret changed")
	}
}

func TestAccessProbeIOErrorDoesNotStopOtherObservations(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "workspace-write-ok"), 0700)
	sibling := filepath.Join(root, "sibling")
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "-test.run=^TestAccessProbeHelper$", "--", "-", sibling)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DEVWRIGHT_ACCESS_HELPER=1")
	stdout, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v", stdout, err)
	}
	got, err := parseObservation(string(stdout))
	if err != nil || !strings.HasPrefix(got.Workspace, "error: ") || got.Outside != "allowed" || got.Secret != "skipped" {
		t.Fatalf("%+v %v", got, err)
	}
	var out bytes.Buffer
	if err := reportObservation(&out, "test", got, observation{}); err == nil || !strings.Contains(out.String(), "outside-workspace write: allowed") {
		t.Fatalf("%s %v", &out, err)
	}
}

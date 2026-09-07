package verification

import (
	"bytes"
	"context"
	"github.com/jackc/devwright/internal/codexpolicy"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProtocolBufferedRepliesAndNotifications(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var input bytes.Buffer
	c := newClient(ctx, &input, strings.NewReader("{\"method\":\"notification\"}\n{\"id\":1,\"result\":\"first\"}\n{\"id\":2,\"result\":\"second\"}\n"))
	for i, want := range []string{"first", "second"} {
		var got string
		if err := c.request(ctx, want, map[string]any{}, i+1, &got); err != nil || got != want {
			t.Fatalf("reply %d: %q %v", i, got, err)
		}
	}
	decoder := json.NewDecoder(&input)
	for _, want := range []string{"first", "second"} {
		var message struct{ Method string }
		if err := decoder.Decode(&message); err != nil || message.Method != want {
			t.Fatalf("request: %+v %v", message, err)
		}
	}
}
func TestProtocolTimeoutAndEOF(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newClient(ctx, io.Discard, reader)
	deadline, done := context.WithTimeout(ctx, 20*time.Millisecond)
	defer done()
	var result any
	if err := c.request(deadline, "wait", nil, 1, &result); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	writer.Close()
	if err := c.request(ctx, "closed", nil, 2, &result); err == nil || !strings.Contains(err.Error(), "exited before replying") {
		t.Fatalf("EOF: %v", err)
	}
}
func TestProtocolErrors(t *testing.T) {
	for _, response := range []string{`{"id":1,"error":{"message":"rejected"}}`, `invalid`, `{"id":1}`} {
		t.Run(response, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := newClient(ctx, io.Discard, strings.NewReader(response+"\n"))
			var result any
			if err := c.request(ctx, "check", nil, 1, &result); err == nil {
				t.Fatal("accepted invalid response")
			}
		})
	}
}
func TestCommandOutputStatusAndTimeout(t *testing.T) {
	out, stderr, err := run(context.Background(), "/bin/sh", "-c", "echo out; echo err >&2; exit 7")
	var status *exec.ExitError
	if out != "out\n" || stderr != "err\n" || !errors.As(err, &status) || status.ExitCode() != 7 {
		t.Fatalf("%q %q %v", out, stderr, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := run(ctx, "/bin/sh", "-c", "sleep 30"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
}
func TestTimeoutCleansUpDescendants(t *testing.T) {
	// The shell exits while a TERM-ignoring descendant holds the output pipes.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := run(ctx, "/bin/sh", "-c", "(trap '' TERM; sleep 30) & exit 0")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("descendant kept pipes open")
	}
}
func TestExistingCanaryIsNeverReadOrRemoved(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		home := t.TempDir()
		path := filepath.Join(home, ".pgpass")
		if symlink {
			if err := os.Symlink("missing", path); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(path, []byte("existing synthetic content"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(home, "projects"), 0700); err != nil {
			t.Fatal(err)
		}
		err := withBehaviorFixture(home, func(f behaviorFixture) error {
			if f.canary != "-" {
				t.Fatalf("existing canary was selected: %s", f.canary)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("canary: %v", err)
		}
		if symlink {
			if target, err := os.Readlink(path); err != nil || target != "missing" {
				t.Fatalf("symlink changed: %q %v", target, err)
			}
		} else if data, err := os.ReadFile(path); err != nil || string(data) != "existing synthetic content" {
			t.Fatalf("canary changed: %q %v", data, err)
		}
	}
}

func TestManagedPolicyChecks(t *testing.T) {
	for _, mode := range []string{"valid", "custom", "profiles", "managed-default", "configured-default", "missing-feature", "enabled-feature", "resolved-feature"} {
		t.Run(mode, func(t *testing.T) {
			profiles := map[string]bool{"vm_dev": true}
			managedDefault, configuredDefault := "vm_dev", "vm_dev"
			features := map[string]bool{}
			for _, key := range featureKeys {
				features[key] = false
			}
			expected := codexpolicy.Requirements{Default: "vm_dev", Profiles: map[string]bool{"vm_dev": true}, Features: map[string]bool{}}
			for _, key := range featureKeys {
				expected.Features[key] = false
			}
			switch mode {
			case "custom":
				profiles = map[string]bool{"custom": true}
				managedDefault, configuredDefault = "custom", "custom"
				features = map[string]bool{"apps": true}
				expected = codexpolicy.Requirements{Default: "custom", Profiles: profiles, Features: features}
			case "profiles":
				profiles["other"] = true
			case "managed-default":
				managedDefault = "other"
			case "configured-default":
				configuredDefault = "other"
			case "missing-feature":
				delete(features, "apps")
			case "enabled-feature":
				features["plugins"] = true
			}
			req, err := json.Marshal(map[string]any{"id": 2, "result": map[string]any{"requirements": map[string]any{"allowedPermissionProfiles": profiles, "defaultPermissions": managedDefault, "featureRequirements": features}}})
			if err != nil {
				t.Fatal(err)
			}
			config, err := json.Marshal(map[string]any{"id": 3, "result": map[string]any{"config": map[string]string{"default_permissions": configuredDefault}}})
			if err != nil {
				t.Fatal(err)
			}
			featureOutput := ""
			for _, key := range featureKeys {
				value := "false"
				if (mode == "resolved-feature" || mode == "custom") && key == "apps" {
					value = "true"
				}
				featureOutput += key + " stable " + value + "\n"
			}
			script := "#!/bin/sh\nif [ \"$1\" = app-server ]; then\n" +
				"IFS= read -r init\nprintf '%s\\n' '{\"method\":\"notification\"}' '{\"id\":1,\"result\":{}}'\n" +
				"IFS= read -r initialized\nIFS= read -r requirements\nprintf '%s\\n' '" + string(req) + "'\n" +
				"IFS= read -r config\nprintf '%s\\n' '" + string(config) + "'\ncat >/dev/null\nelse\ncat <<'FEATURES'\n" + featureOutput + "FEATURES\nfi\n"
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			var out bytes.Buffer
			err = checkSelectedPolicy(&out, expected)
			if mode == "valid" || mode == "custom" {
				if err != nil || !strings.Contains(out.String(), "PASS Codex") {
					t.Fatalf("valid policy: %v: %s", err, out.String())
				}
			} else if err == nil {
				t.Fatalf("accepted %s policy", mode)
			}
		})
	}
}

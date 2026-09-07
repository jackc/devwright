package devwright

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestOptions(t *testing.T) {
	for _, args := range [][]string{
		{"create", "test", "--cpus", "8", "--memory=8GiB", "--disk", "100GiB", "--dotfiles-repo", "https://example.invalid/repo", "--dotfiles-install", "setup script"},
		{"--backend", "lima", "create", "--cpus", "8", "--memory=8GiB", "test", "--disk", "100GiB", "--dotfiles-repo", "https://example.invalid/repo", "--dotfiles-install", "setup script"},
	} {
		o, err := parseOptions(args)
		if err != nil {
			t.Fatal(err)
		}
		if o.name != "test" || o.cpus != 8 || o.memory != "8GiB" || o.disk != "100GiB" || o.dotfilesInstall != "setup script" {
			t.Fatalf("unexpected options: %+v", o)
		}
	}
	o, err := parseOptions([]string{"verify"})
	if err != nil || o.name != "dev" {
		t.Fatalf("default name: %+v, %v", o, err)
	}
}

func TestInvalidOptionsNeverExecute(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{
		{}, {"--version"}, {"render", "--version"}, {"version", "extra"}, {"set-token"}, {"start"}, {"shell"}, {"admin"}, {"verify", "test; false"}, {"verify", "../test"},
		{"verify", strings.Repeat("a", 41)}, {"verify", "test", "extra"}, {"-"},
		{"create", "--dotfiles-install", "setup"}, {"verify", "--dotfiles-repo", "repo"},
		{"create", "--dotfiles-repo", ""}, {"create", "--dotfiles-repo=-repo"},
		{"create", "--dotfiles-repo", "repo", "--dotfiles-install", "../install"},
		{"create", "--dotfiles-repo", "repo", "--dotfiles-install", "/install"},
		{"create", "--cpus", "0"}, {"create", "--cpus=-1"}, {"create", "--cpus"},
		{"configure", "--cpus", "8"}, {"verify", "--disk", "8GiB"},
		{"create", "--memory", "0GiB"}, {"create", "--memory", "bogus"},
		{"create", "--disk", "-4GiB"}, {"--cpus", "4", "--", "render", "--disk=8GiB"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			err := Run(context.Background(), args, "test", nil, io.Discard, io.Discard)
			if err == nil || strings.Contains(err.Error(), "Lima") && !strings.Contains(err.Error(), "resource options") {
				t.Fatalf("invalid options reached dependencies: %v", err)
			}
		})
	}
}

func TestOfflineCommands(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"--help"}, "Available Commands:"}, {[]string{"version"}, "devwright v1.2.3\n"},
		{[]string{"render", "--memory", "8GiB"}, `"memory": "8GiB"`},
	} {
		var output bytes.Buffer
		if err := Run(context.Background(), test.args, "v1.2.3", nil, &output, io.Discard); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), test.want) {
			t.Fatalf("unexpected output: %s", output.String())
		}
	}
}

func TestPrerequisites(t *testing.T) {
	for _, test := range []struct {
		version string
		wantErr bool
	}{
		{"limactl version 2.2.0\n", false}, {"limactl version 2.3.1\n", false}, {"limactl version 3.0.0\n", false},
		{"limactl version 2.1.9\n", true}, {"limactl version 1.9.9\n", true},
		{"limactl version 2.2.0-rc.1\n", true}, {"unrecognized", true},
	} {
		t.Run(test.version, func(t *testing.T) {
			sshChecked := false
			run := func(args []string, input io.Reader, capture bool) (string, error) {
				if reflect.DeepEqual(args, []string{"limactl", "--version"}) {
					return test.version, nil
				}
				if args[0] != "ssh" || args[1] != "-G" || !capture {
					t.Fatalf("unexpected prerequisite command: %v", args)
				}
				sshChecked = true
				return "", nil
			}
			err := preflight(run, func(s string) (string, error) { return s, nil }, "darwin")
			if (err != nil) != test.wantErr || sshChecked == test.wantErr {
				t.Fatalf("prerequisite result: %v; SSH checked: %v", err, sshChecked)
			}
		})
	}
	for _, missing := range []string{"limactl", "ssh"} {
		err := preflight(nil, func(s string) (string, error) {
			if s == missing {
				return "", exec.ErrNotFound
			}
			return s, nil
		}, "linux")
		if err == nil {
			t.Fatal("accepted missing dependency")
		}
	}
	if err := preflight(nil, nil, "windows"); err == nil {
		t.Fatal("accepted unsupported host")
	}
	if err := preflight(func(args []string, _ io.Reader, _ bool) (string, error) {
		if args[0] == "limactl" {
			return "limactl version 2.2.0", nil
		}
		return "", errors.New("unsupported option")
	}, func(s string) (string, error) { return s, nil }, "linux"); err == nil {
		t.Fatal("accepted incompatible SSH")
	}
}

func TestCobraArgumentCompatibility(t *testing.T) {
	for _, args := range [][]string{
		{"--backend", "incus", "render", "--container=false", "work", "--cpus=2"},
		{"render", "--backend=incus", "work", "--container=false", "--cpus", "2"},
		{"render", "--cpus=2", "--container=false", "--backend", "incus", "--", "work"},
	} {
		o, err := parseOptions(args)
		if err != nil {
			t.Fatal(err)
		}
		if o.action != "render" || o.name != "work" || o.backend != "incus" || o.cpus != 2 || o.container || !o.set["container"] {
			t.Fatalf("unexpected options for %v: %+v", args, o)
		}
	}
	for _, args := range [][]string{
		{"render", "--", "--cpus=2"},
		{"render", "--container=false"}, // Explicit false must still undergo backend validation.
		{"render", "--unknown"},
		{"--backend", "user", "delete"},
		{"--backend", "user", "list", "work"},
	} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
	}
}

func TestCobraHelpAndVersion(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"-h"}, "Available Commands:"},
		{[]string{"help", "create"}, "devwright create [NAME] [flags]"},
		{[]string{"create", "--help"}, "devwright create [NAME] [flags]"},
		{[]string{"--help", "delete"}, "devwright delete NAME [flags]"},
		{[]string{"list", "-h"}, "devwright list [flags]"},
		{[]string{"version"}, "devwright v1.2.3\n"},
		{[]string{"version", "--help"}, "devwright version [flags]"},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			var output, stderr bytes.Buffer
			if err := Run(context.Background(), test.args, "v1.2.3", nil, &output, &stderr); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), test.want) || stderr.Len() != 0 {
				t.Fatalf("stdout=%q stderr=%q; want %q", output.String(), stderr.String(), test.want)
			}
		})
	}
}

func TestCommandFlagHelp(t *testing.T) {
	for _, test := range []struct {
		command         string
		present, absent []string
	}{
		{"", []string{"--backend", "--help"}, []string{"--cpus", "--codex-config", "--remove-home", "--ssh-port"}},
		{"create", []string{"--cpus", "--codex-config", "--ssh-port"}, []string{"--replace-codex-config", "--remove-home"}},
		{"configure", []string{"--codex-config", "--replace-codex-config", "--ssh-port"}, []string{"--cpus", "--container", "--remove-home"}},
		{"render", []string{"--cpus", "--container", "--ssh-port"}, []string{"--codex-config", "--dotfiles-repo", "--remove-home"}},
		{"delete", []string{"--remove-home"}, []string{"--cpus", "--codex-config", "--ssh-port"}},
		{"verify", []string{"--backend"}, []string{"--cpus", "--codex-config", "--remove-home", "--ssh-port"}},
		{"version", []string{"--help"}, []string{"--cpus", "--codex-config", "--remove-home", "--ssh-port"}},
	} {
		t.Run(test.command, func(t *testing.T) {
			args := []string{"--help"}
			if test.command != "" {
				args = append([]string{test.command}, args...)
			}
			var output bytes.Buffer
			if err := Run(context.Background(), args, "test", nil, &output, io.Discard); err != nil {
				t.Fatal(err)
			}
			for _, flag := range test.present {
				if !strings.Contains(output.String(), flag) {
					t.Errorf("help missing %s", flag)
				}
			}
			for _, flag := range test.absent {
				if strings.Contains(output.String(), flag) {
					t.Errorf("help includes unrelated %s", flag)
				}
			}
		})
	}
}

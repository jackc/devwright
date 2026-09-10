package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeriveProjectName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "full_stack_payments")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	for _, tc := range []struct {
		source, want string
		local        bool
	}{
		{source: ".", local: true, want: "full_stack_payments"},
		{source: dir + "/", local: true, want: "full_stack_payments"},
		{source: "../full_stack_payments/.", local: true, want: "full_stack_payments"},
		{source: "/projects/Repo.Name_2", local: true, want: "Repo.Name_2"},
		{source: "/projects/local.git", local: true, want: "local.git"},
		{source: "https://github.com/team/full_stack_payments.git", want: "full_stack_payments"},
		{source: "https://github.com/team/Repo.Name_2.git/", want: "Repo.Name_2"},
		{source: "git@github.com:team/full_stack_payments.git", want: "full_stack_payments"},
		{source: "git@github.com:full_stack_payments.git", want: "full_stack_payments"},
		{source: "ssh://git@example.com:2222/team/full_stack_payments.git", want: "full_stack_payments"},
		{source: "git@[::1]:team/full_stack_payments.git", want: "full_stack_payments"},
		{source: "git://example.com/team/full_stack_payments", want: "full_stack_payments"},
		{source: "file:///tmp/full_stack_payments.git", want: "full_stack_payments"},
		{source: "https://example.com/team/Project%5FName.git?token=private", want: "Project_Name"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			got, err := deriveProjectName(tc.source, tc.local)
			if err != nil || got != tc.want {
				t.Fatalf("name = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestProjectNameValidation(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../escape", "/absolute", "a/b", `a\b`, "a b", "a\nline", "a\x00b", "$(touch canary)", "a`id`", `a"b`, ".devwright-transfer", "-option", strings.Repeat("a", 256)} {
		if err := validateProjectName(name); err == nil {
			t.Errorf("accepted unsafe project name %q", name)
		}
	}
	for _, name := range []string{"a", "1", "full_stack_payments", "Project.Name-2", strings.Repeat("a", 255)} {
		if err := validateProjectName(name); err != nil {
			t.Errorf("rejected project name %q: %v", name, err)
		}
	}
	for _, source := range []string{"", "https://example.com/", "https://example.com/.git", "https://example.com/team/a%20b.git", "https://private:secret@example.com/%zz", "git@example.com:"} {
		_, err := deriveProjectName(source, false)
		if err == nil || !strings.Contains(err.Error(), "--project-name") || strings.Contains(err.Error(), "secret") {
			t.Errorf("source %q: expected override guidance without credentials, got %v", source, err)
		}
	}
	if _, err := deriveProjectName("/", true); err == nil {
		t.Fatal("root directory needs an explicit project name")
	}
}

// Simulate Lima's control plane without creating a VM. Real Git and directory
// snapshots still run; start deliberately fails after onboarding state is saved.
func projectNameLima(t *testing.T) instance {
	t.Helper()
	s := instance{Name: "fsp-dev-vm", Dir: t.TempDir(), Status: "Running"}
	s.Config.User.Name = "dev"
	s.Config.User.Home = "/home/dev"
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "instance.json")
	writeTest(t, state, string(data))
	t.Setenv("TEST_LIMA_INSTANCE", state)
	bin := t.TempDir()
	writeTest(t, filepath.Join(bin, "limactl"), `#!/bin/bash
set -eu
case "$1 $2" in
  'list --format') exit 0 ;;
  'list --json') cat "$TEST_LIMA_INSTANCE" ;;
  'template copy') cp "$4" "$5" ;;
  'validate '*|'create '*) exit 0 ;;
  'start '*) exit 72 ;;
  *) exit 99 ;;
esac
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return s
}

func TestCreateSavesProjectName(t *testing.T) {
	for _, tc := range []struct {
		name, ref, override string
		gitURL              bool
	}{
		{name: "local directory"},
		{name: "local selected revision", ref: "HEAD"},
		{name: "Git URL", gitURL: true},
		{name: "explicit override", override: "Payments.Worktree_2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := filepath.Join(t.TempDir(), "full_stack_payments.git")
			writeTest(t, filepath.Join(project, ".devwright/lima.yaml"), "cpus: 2\n")
			gitTest(t, project, "init", "-b", "main")
			gitTest(t, project, "config", "user.email", "test@example.invalid")
			gitTest(t, project, "config", "user.name", "Project name test")
			commitTest(t, project)
			s := projectNameLima(t)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Chdir(project)
			source := "."
			want := filepath.Base(project)
			if tc.gitURL {
				source = "file://" + project
				want = "full_stack_payments"
			}
			args := []string{"create", s.Name, "--from", source, "--no-dotfiles"}
			if tc.ref != "" {
				args = append(args, "--ref", tc.ref)
			}
			if tc.override != "" {
				args = append(args, "--project-name", tc.override)
				want = tc.override
			}
			var output bytes.Buffer
			err := Run(context.Background(), args, "test", nil, &output, &output)
			if err == nil || !strings.Contains(err.Error(), "exit status 72") {
				t.Fatalf("expected to stop at VM start, got %v: %s", err, output.String())
			}
			data, err := os.ReadFile(filepath.Join(s.Dir, "devwright/onboarding.json"))
			if err != nil {
				t.Fatal(err)
			}
			var m manifest
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			if m.ProjectName != want {
				t.Fatalf("saved name = %q; want %q", m.ProjectName, want)
			}
			if (m.Project != nil) != (tc.gitURL || tc.ref != "") {
				t.Fatalf("wrong transfer type: %+v", m)
			}
		})
	}
}

func TestCLIRejectsInvalidProjectName(t *testing.T) {
	for _, name := range []string{"", "../escape", "bad name", "$(id)"} {
		var output bytes.Buffer
		err := Run(context.Background(), []string{"create", "fsp-dev-vm", "--project-name", name}, "test", nil, &output, &output)
		if err == nil || !strings.Contains(err.Error(), "project name must be") {
			t.Fatalf("name %q: expected validation before provisioning, got %v", name, err)
		}
	}
}

func TestSavedProjectName(t *testing.T) {
	s := projectNameLima(t)
	a := testApp(t)
	for _, tc := range []struct {
		body, want string
		invalid    bool
	}{
		{body: `{"ProjectName":"full_stack_payments"}`, want: "full_stack_payments"},
		{body: `{}`, want: s.Name},
		{body: `{"ProjectName":"../escape"}`, invalid: true},
		{body: `{"ProjectName":"$(id)"}`, invalid: true},
	} {
		writeTest(t, filepath.Join(s.Dir, "devwright/onboarding.json"), tc.body)
		_, m, err := a.saved(s.Name)
		if tc.invalid {
			if err == nil || !strings.Contains(err.Error(), "invalid saved project name") {
				t.Fatalf("accepted invalid state %s: %v", tc.body, err)
			}
		} else if err != nil || m.ProjectName != tc.want {
			t.Fatalf("state %s: name = %q, %v; want %q", tc.body, m.ProjectName, err, tc.want)
		}
	}
}

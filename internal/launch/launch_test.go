package launch

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testApp(t *testing.T) *app {
	t.Helper()
	return &app{ctx: context.Background(), in: strings.NewReader(""), out: io.Discard, err: io.Discard, home: t.TempDir()}
}
func writeTest(t *testing.T, path, body string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(body), 0700); e != nil {
		t.Fatal(e)
	}
}
func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v: %s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}
func fixture(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	gitTest(t, d, "init", "-b", "main")
	gitTest(t, d, "config", "user.email", "test@example.invalid")
	gitTest(t, d, "config", "user.name", "Acceptance")
	writeTest(t, filepath.Join(d, "README"), "committed project\n")
	return d
}
func commitTest(t *testing.T, d string) {
	t.Helper()
	gitTest(t, d, "add", ".")
	gitTest(t, d, "commit", "-m", "fixture")
}
func TestInitAndNoOverwrite(t *testing.T) {
	a := testApp(t)
	d := t.TempDir()
	if e := a.init(d); e != nil {
		t.Fatal(e)
	}
	if e := a.init(d); e == nil {
		t.Fatal("overwrote recipe")
	}
	b, e := os.ReadFile(filepath.Join(d, ".devwright/lima.yaml"))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(b), "file: setup-system.sh") {
		t.Fatal("not native Lima")
	}
	if _, e = exec.LookPath("limactl"); e == nil {
		c := exec.Command("limactl", "template", "copy", "--embed-all", filepath.Join(d, ".devwright/lima.yaml"), filepath.Join(d, "rendered.yaml"))
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("native template: %v: %s", e, b)
		}
	}
}
func TestSnapshotSelectsCommitAndExcludesUncommittedFiles(t *testing.T) {
	a := testApp(t)
	d := fixture(t)
	commitTest(t, d)
	first := gitTest(t, d, "rev-parse", "HEAD")
	gitTest(t, d, "tag", "first")
	writeTest(t, filepath.Join(d, "README"), "second")
	commitTest(t, d)
	writeTest(t, filepath.Join(d, "private-untracked"), "must not transfer")
	dst := filepath.Join(t.TempDir(), "clone")
	r, e := a.snapshot(d, "first", dst)
	if e != nil {
		t.Fatal(e)
	}
	if r.Commit != first {
		t.Fatalf("wrong commit: %+v", r)
	}
	bundle := dst + ".bundle"
	guest := filepath.Join(t.TempDir(), "guest")
	gitTest(t, d, "clone", "-b", "devwright-transfer", bundle, guest)
	if _, e = os.Stat(filepath.Join(guest, "private-untracked")); !os.IsNotExist(e) {
		t.Fatal("transferred untracked data")
	}
	if _, e = a.snapshot(d, "--help", filepath.Join(t.TempDir(), "bad")); e == nil {
		t.Fatal("accepted invalid ref")
	}
}
func TestCredentialAndShellQuoting(t *testing.T) {
	value := "x'\n$(touch /tmp/devwright-must-not-execute) {{literal}} $HOME"
	c := exec.Command("bash", "-c", "printf '%s' "+quote(value))
	b, e := c.Output()
	if e != nil || string(b) != value {
		t.Fatalf("quoting: %q %v", b, e)
	}
	script, e := credentialScript("EXAMPLE_TOKEN", value)
	if e != nil {
		t.Fatal(e)
	}
	c = exec.Command("bash", "-n")
	c.Stdin = strings.NewReader(script)
	if b, e := c.CombinedOutput(); e != nil {
		t.Fatalf("script syntax: %v %s", e, b)
	}
	for _, name := range []string{"A;exit", "../token", ""} {
		if _, e = credentialScript(name, "value"); e == nil {
			t.Fatal("invalid name accepted")
		}
	}
}
func TestDeclarationsAreStrict(t *testing.T) {
	for _, body := range []string{"- name: TOKEN\n  description: token\n  value: secret\n", "- name: TOKEN\n  description: a\n- name: TOKEN\n  description: b\n", "[]\n---\n[]\n"} {
		path := filepath.Join(t.TempDir(), "credentials.yaml")
		writeTest(t, path, body)
		var cs []credential
		e := readYAML(path, &cs, false)
		if e == nil {
			e = validateCredentials(cs)
		}
		if e == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
func TestSSHConfigPreservesUserSettings(t *testing.T) {
	a := testApp(t)
	main := filepath.Join(a.home, ".ssh/config")
	writeTest(t, main, "Host personal\n  HostName example.invalid\n")
	s := instance{Name: "test", Dir: "/tmp/lima space"}
	s.Config.User.Name = "developer"
	if e := a.installSSH(s); e != nil {
		t.Fatal(e)
	}
	if e := a.installSSH(s); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(main)
	if strings.Count(string(b), "Include") != 1 || !strings.Contains(string(b), "Host personal") {
		t.Fatal(string(b))
	}
	alias, _ := os.ReadFile(filepath.Join(a.home, ".ssh/devwright/test.config"))
	if !strings.Contains(string(alias), "ControlPath ~/.ssh/control-%C") {
		t.Fatal("unsafe multiplexing")
	}
}
func TestCLIRejectsRemovedCommands(t *testing.T) {
	for _, cmd := range []string{"verify", "configure"} {
		var b bytes.Buffer
		if e := Run(context.Background(), []string{cmd, "example"}, "test", nil, &b, &b); e == nil {
			t.Fatal("accepted " + cmd)
		}
	}
}

// Opt-in real VM acceptance. It leaves the VM stopped for inspection.
func TestAcceptance(t *testing.T) {
	name := os.Getenv("DEVWRIGHT_ACCEPTANCE")
	if name == "" {
		t.Skip("set DEVWRIGHT_ACCEPTANCE to a fresh disposable VM name")
	}
	a := testApp(t)
	a.out = os.Stdout
	a.err = os.Stderr
	project := fixture(t)
	if e := a.init(project); e != nil {
		t.Fatal(e)
	}
	// Exercise the starter system script with a different account and home,
	// including ownership, sudo restrictions, and initial agent configuration.
	recipePath := filepath.Join(project, ".devwright/lima.yaml")
	recipe, e := os.ReadFile(recipePath)
	if e != nil {
		t.Fatal(e)
	}
	customRecipe := strings.Replace(string(recipe), "  name: dev\n", "  name: builder\n", 1)
	customRecipe = strings.Replace(customRecipe, `  home: "/home/{{.User}}"`, "  home: /srv/builder-home", 1)
	writeTest(t, recipePath, customRecipe)
	writeTest(t, filepath.Join(project, ".devwright/credentials.yaml"), "- name: DEVWRIGHT_TEST_TOKEN\n  description: Synthetic test token\n")
	writeTest(t, filepath.Join(project, ".devwright/setup-project.sh"), "#!/bin/bash\nset -euo pipefail\ntest \"$DEVWRIGHT_TEST_TOKEN\" = 'synthetic-first'\nprintf 'project setup\\n' >> setup-count\n")
	commitTest(t, project)
	dotfiles := fixture(t)
	writeTest(t, filepath.Join(dotfiles, "install"), "#!/bin/bash\nset -euo pipefail\nprintf 'dotfiles\\n' >> \"$HOME/dotfiles-count\"\n")
	commitTest(t, dotfiles)
	// Force the remote-style fetch path by using file://, including branch selection.
	defer a.run("limactl", "stop", name)
	if os.Getenv("DEVWRIGHT_ACCEPTANCE_RESUME") == "1" {
		e = a.finish(name)
	} else {
		e = a.create(name, options{from: "file://" + project, ref: "main", recipe: ".devwright/lima.yaml", dotfiles: dotfiles, installer: "install"})
	}
	if e == nil || !strings.Contains(e.Error(), "missing DEVWRIGHT_TEST_TOKEN") {
		t.Fatalf("expected missing-credential recovery, got %v", e)
	}
	s, m, e := a.saved(name)
	if e != nil {
		t.Fatal(e)
	}
	for i, value := range []string{"synthetic-first", "synthetic-second"} {
		script, e := credentialScript("DEVWRIGHT_TEST_TOKEN", value)
		if e != nil {
			t.Fatal(e)
		}
		if e = a.ssh(s, s.Config.User.Name, "/bin/bash -s", strings.NewReader(script), a.out); e != nil {
			t.Fatal(e)
		}
		if e = a.finish(name); e != nil {
			t.Fatal(e)
		}
		if i == 0 {
			if e = a.ssh(s, s.Config.User.Name, "printf 'user data' > \"$HOME/projects/"+m.ProjectName+"/user-data\"", nil, a.out); e != nil {
				t.Fatal(e)
			}
		}
	}
	check := `set -euo pipefail
. "$HOME/.config/devwright/credentials.sh"
test "$(id -un)" = builder
test "$HOME" = /srv/builder-home
test "$(stat -c %U "$HOME/.codex/config.toml")" = builder
test "$(stat -c %U "$HOME/.claude/settings.json")" = builder
test "$DEVWRIGHT_TEST_TOKEN" = synthetic-second
test "$(wc -l < "$HOME/dotfiles-count")" -eq 1
test "$(wc -l < "$HOME/projects/` + m.ProjectName + `/setup-count")" -eq 1
test "$(cat "$HOME/projects/` + m.ProjectName + `/user-data")" = 'user data'
test "$(stat -c %a "$HOME/.config/devwright/credentials.d/DEVWRIGHT_TEST_TOKEN.sh")" = 600
test ! -e /usr/local/share/devwright/verify
command -v codex
command -v claude
! sudo -n true
`
	if e = a.ssh(s, s.Config.User.Name, check, nil, a.out); e != nil {
		t.Fatal(e)
	}
	// Change source files after snapshot; finish and restart must not use them.
	writeTest(t, filepath.Join(project, ".devwright/setup-project.sh"), "exit 99\n")
	if e = a.run("limactl", "stop", name); e != nil {
		t.Fatal(e)
	}
	if e = a.finish(name); e != nil {
		t.Fatal(e)
	}
	if e = a.ssh(s, s.Config.User.Name, check, nil, a.out); e != nil {
		t.Fatal(e)
	}
	t.Log("PASS native recipe, repository transfer, dev-only dotfiles, credential recovery/rotation, once-only project hook, restart, user state")
}

func TestCustomAcceptance(t *testing.T) {
	name := os.Getenv("DEVWRIGHT_CUSTOM_ACCEPTANCE")
	if name == "" {
		t.Skip("set DEVWRIGHT_CUSTOM_ACCEPTANCE to a fresh disposable VM name")
	}
	a := testApp(t)
	a.out = os.Stdout
	a.err = os.Stderr
	project := fixture(t)
	writeTest(t, filepath.Join(project, ".devwright/lima.yaml"), `minimumLimaVersion: "2.2.0"
base:
  - template:_images/ubuntu-26.04
plain: true
mounts: []
cpus: 2
memory: 2GiB
user:
  name: developer
  home: /home/developer
  uid: 1001
  shell: /bin/bash
  passwordlessSudo: false
env:
  PROJECT_ENV: literal-value
provision:
  - mode: system
    file: system.sh
probes:
  - mode: readiness
    description: Custom project readiness
    script: |
      #!/bin/bash
      set -eu
      test -f /var/tmp/project-ready
      test "{{.User}}" = developer
      test "{{.Home}}" = /home/developer
`)
	writeTest(t, filepath.Join(project, ".devwright/system.sh"), `#!/bin/bash
set -eu
export HOME=/root DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y --no-install-recommends git
install -d -m 700 /root/.ssh
install -m 600 /home/developer/.ssh/authorized_keys /root/.ssh/authorized_keys
groupadd builders
usermod -g builders developer
touch /var/tmp/dotfiles-fail-root /var/tmp/dotfiles-fail-user /var/tmp/dotfiles-symlink-canary
chmod 600 /var/tmp/dotfiles-symlink-canary
sha256sum /root/.bashrc /root/.profile > /root/startup-before
touch /var/tmp/project-ready
`)
	commitTest(t, project)
	// Local recipe edits must be honored without copying uncommitted project data.
	writeTest(t, filepath.Join(project, ".devwright/setup-project.sh"), "#!/bin/bash\nset -eu\ntest \"$PROJECT_ENV\" = literal-value\nprintf done > local-hook-result\n")
	dotfiles := fixture(t)
	writeTest(t, filepath.Join(dotfiles, "install"), `#!/bin/bash
set -eu
test "$DEVWRIGHT_USER" = developer
test "$DEVWRIGHT_HOME" = /home/developer
test "$DEVWRIGHT_UID" = 1001
test "$(id -u)" = 0
test "$HOME" = /root
test "$(stat -c '%a:%G' /var/tmp/dotfiles-symlink-canary)" = 600:root
printf 'installed\n' >> "$HOME/dotfiles-count"
test ! -e /var/tmp/dotfiles-fail-root
touch /var/tmp/dotfiles-system-ready
exec runuser -u "$DEVWRIGHT_USER" -- env HOME="$DEVWRIGHT_HOME" /bin/bash scripts/install-user
`)
	writeTest(t, filepath.Join(dotfiles, "settings.sh"), "DOTFILES_SETTING=loaded\n")
	writeTest(t, filepath.Join(dotfiles, "scripts/install-user"), `#!/bin/bash
set -eu
test "$(id -un)" = developer
test "$(id -gn)" = builders
test "$HOME" = "$DEVWRIGHT_HOME"
test "$USER" = "$DEVWRIGHT_USER"
test "$DEVWRIGHT_UID" = 1001
test -f /var/tmp/dotfiles-system-ready
test ! -w install
test ! -w .
. ./settings.sh
test "$DOTFILES_SETTING" = loaded
! sudo -n true
printf 'installed\n' >> "$HOME/dotfiles-count"
test ! -e /var/tmp/dotfiles-fail-user
`)
	if e := os.Symlink("/var/tmp/dotfiles-symlink-canary", filepath.Join(dotfiles, "outside")); e != nil {
		t.Fatal(e)
	}
	commitTest(t, dotfiles)
	defer a.run("limactl", "stop", name)
	if e := a.create(name, options{from: project, projectName: "custom_project", recipe: ".devwright/lima.yaml", dotfiles: dotfiles, installer: "install", rootDotfiles: true}); e == nil || !strings.Contains(e.Error(), "dotfiles installer as root") {
		t.Fatalf("expected root setup failure, got %v", e)
	}
	s, m, e := a.saved(name)
	if e != nil {
		t.Fatal(e)
	}
	if !m.RootDotfiles {
		t.Fatalf("expected saved privileged execution mode, got %+v", m)
	}
	if m.ProjectName != "custom_project" {
		t.Fatalf("wrong saved project name: %q", m.ProjectName)
	}
	if e = a.ssh(s, "root", "set -eu; test ! -e "+sharedDotfilesPath+"/.git/devwright-installed; test ! -e /home/developer/dotfiles-count; rm /var/tmp/dotfiles-fail-root", nil, a.out); e != nil {
		t.Fatal(e)
	}
	if e = a.finish(name); e == nil || !strings.Contains(e.Error(), "dotfiles installer as root") {
		t.Fatalf("expected user setup failure propagated through root entry point, got %v", e)
	}
	if e = a.ssh(s, "root", "set -eu; test ! -e "+sharedDotfilesPath+"/.git/devwright-installed; rm /var/tmp/dotfiles-fail-user", nil, a.out); e != nil {
		t.Fatal(e)
	}
	if e = a.finish(name); e != nil {
		t.Fatal(e)
	}
	check := "set -eu; test \"$(wc -l < \"$HOME/dotfiles-count\")\" -eq 2; ! sudo -n true; test \"$(cat \"$HOME/projects/" + m.ProjectName + "/local-hook-result\")\" = done; test ! -e /usr/local/bin/codex; test ! -e \"$HOME/.local/share/devwright/dotfiles\""
	for range 2 {
		if e = a.finish(name); e != nil {
			t.Fatal(e)
		}
		if e = a.ssh(s, "developer", check, nil, a.out); e != nil {
			t.Fatal(e)
		}
		if e = a.ssh(s, "root", "set -eu; test \"$(wc -l < /root/dotfiles-count)\" -eq 3; test -f "+sharedDotfilesPath+"/.git/devwright-installed; test \"$(stat -c %U "+sharedDotfilesPath+")\" = root; test ! -e /root/.local/share/devwright/dotfiles; sha256sum -c /root/startup-before", nil, a.out); e != nil {
			t.Fatal(e)
		}
	}
	alias := filepath.Join(a.home, ".ssh/devwright", name+".config")
	if e = a.run("ssh", "-F", alias, "-o", "BatchMode=yes", "lima-"+name, "test \"$(id -un)\" = developer"); e != nil {
		t.Fatal(e)
	}
	// A failing saved readiness check must block finish on an already-running VM.
	if e = a.ssh(s, "root", "rm /var/tmp/project-ready", nil, a.out); e != nil {
		t.Fatal(e)
	}
	if e = a.finish(name); e == nil || !strings.Contains(e.Error(), "readiness probe") {
		t.Fatalf("readiness was bypassed: %v", e)
	}
	if e = a.ssh(s, "root", "touch /var/tmp/project-ready", nil, a.out); e != nil {
		t.Fatal(e)
	}
	t.Log("PASS one root entry point, user handoff, shared read-only checkout, failure recovery, unchanged sudo/root startup files, custom account and group, SSH alias, readiness failure")
}

func TestCloudReadiness(t *testing.T) {
	if e := cloudReady(`{"status":"done","errors":[],"recoverable_errors":{"WARNING":["image interface rename warning"]}}`); e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{`{"status":"error","errors":["script failed"]}`, `{"status":"done","errors":["script failed"]}`, `{"status":"running","errors":[]}`, `invalid`} {
		if cloudReady(raw) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

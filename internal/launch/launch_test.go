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
	writeTest(t, filepath.Join(project, ".devwright/credentials.yaml"), "- name: DEVWRIGHT_TEST_TOKEN\n  description: Synthetic test token\n")
	writeTest(t, filepath.Join(project, ".devwright/setup-project.sh"), "#!/bin/bash\nset -euo pipefail\ntest \"$DEVWRIGHT_TEST_TOKEN\" = 'synthetic-first'\nprintf 'project setup\\n' >> setup-count\n")
	commitTest(t, project)
	dotfiles := fixture(t)
	writeTest(t, filepath.Join(dotfiles, "install"), "#!/bin/bash\nset -euo pipefail\nprintf 'dotfiles\\n' >> \"$HOME/dotfiles-count\"\n")
	commitTest(t, dotfiles)
	// Force the remote-style fetch path by using file://, including branch selection.
	defer a.run("limactl", "stop", name)
	var e error
	if os.Getenv("DEVWRIGHT_ACCEPTANCE_RESUME") == "1" {
		e = a.finish(name)
	} else {
		e = a.create(name, options{from: "file://" + project, ref: "main", recipe: ".devwright/lima.yaml", dotfiles: dotfiles, installer: "install"})
	}
	if e == nil || !strings.Contains(e.Error(), "missing DEVWRIGHT_TEST_TOKEN") {
		t.Fatalf("expected missing-credential recovery, got %v", e)
	}
	s, _, e := a.saved(name)
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
			if e = a.ssh(s, s.Config.User.Name, "printf 'user data' > \"$HOME/projects/"+name+"/user-data\"", nil, a.out); e != nil {
				t.Fatal(e)
			}
		}
	}
	check := `set -euo pipefail
. "$HOME/.config/devwright/credentials.sh"
test "$DEVWRIGHT_TEST_TOKEN" = synthetic-second
test "$(wc -l < "$HOME/dotfiles-count")" -eq 1
test "$(wc -l < "$HOME/projects/` + name + `/setup-count")" -eq 1
test "$(cat "$HOME/projects/` + name + `/user-data")" = 'user data'
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
  passwordlessSudo: true
param:
  Expected: works
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
      test "{{.Param.Expected}}" = works
      test "$PARAM_Expected" = works
`)
	writeTest(t, filepath.Join(project, ".devwright/system.sh"), `#!/bin/bash
set -eu
export HOME=/root DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y --no-install-recommends git
install -d -m 700 /root/.ssh
install -m 600 /home/developer/.ssh/authorized_keys /root/.ssh/authorized_keys
touch /var/tmp/project-ready
`)
	commitTest(t, project)
	// Local recipe edits must be honored without copying uncommitted project data.
	writeTest(t, filepath.Join(project, ".devwright/setup-project.sh"), "#!/bin/bash\nset -eu\ntest \"$PROJECT_ENV\" = literal-value\nprintf done > local-hook-result\n")
	dotfiles := fixture(t)
	writeTest(t, filepath.Join(dotfiles, "install"), "#!/bin/bash\nset -eu\nprintf '%s' \"$(id -un)\" > \"$HOME/dotfiles-account\"\n")
	commitTest(t, dotfiles)
	defer a.run("limactl", "stop", name)
	if e := a.create(name, options{from: project, recipe: ".devwright/lima.yaml", dotfiles: dotfiles, installer: "install", rootDotfiles: true}); e != nil {
		t.Fatal(e)
	}
	s, _, e := a.saved(name)
	if e != nil {
		t.Fatal(e)
	}
	check := "set -eu; test \"$(cat \"$HOME/dotfiles-account\")\" = developer; sudo -n true; test \"$(cat \"$HOME/projects/" + name + "/local-hook-result\")\" = done; test ! -e /usr/local/bin/codex"
	if e = a.ssh(s, "developer", check, nil, a.out); e != nil {
		t.Fatal(e)
	}
	if e = a.ssh(s, "root", "test \"$(cat /root/dotfiles-account)\" = root", nil, a.out); e != nil {
		t.Fatal(e)
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
	t.Log("PASS custom account, sudo-enabled recipe without agents, native template parameters, local recipe edits, explicit root dotfiles, SSH alias, readiness failure")
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

package launch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Run the actual remote shell scripts against isolated, already-transferred
// checkouts. SSH supplies the execution account's environment, as it does in a VM.
func TestDotfilesExecutionAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, fail, first, resumed string
		root                       bool
	}{
		{name: "user only", first: "developer\n", resumed: "developer\n"},
		{name: "user failure retries invocation", fail: "developer", first: "developer\n", resumed: "developer\ndeveloper\n"},
		{name: "privileged single invocation", root: true, first: "root\n", resumed: "root\n"},
		{name: "privileged failure retries invocation", root: true, fail: "root", first: "root\n", resumed: "root\nroot\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t)
			var stderr strings.Builder
			a.err = &stderr
			t.Cleanup(func() {
				if t.Failed() {
					t.Log(stderr.String())
				}
			})
			s := instance{Name: "dotfiles", Dir: t.TempDir(), Status: "Running"}
			s.Config.User.Name = "developer"
			s.Config.User.Home = filepath.Join(t.TempDir(), "developer's home $literal")
			s.Config.User.UID = 1234
			rootHome := t.TempDir()
			shared := t.TempDir()
			log := filepath.Join(t.TempDir(), "install-log")
			t.Setenv("TEST_ROOT_HOME", rootHome)
			t.Setenv("TEST_USER_HOME", s.Config.User.Home)
			t.Setenv("TEST_SHARED_PATH", shared)
			t.Setenv("TEST_INSTALL_LOG", log)
			t.Setenv("TEST_FAIL_ACCOUNT", tc.fail)
			bin := t.TempDir()
			writeTest(t, filepath.Join(bin, "ssh"), `#!/bin/bash
set -eu
while [ "$#" -gt 1 ]; do
  if [ "$1" = -l ]; then export USER="$2"; shift; fi
  shift
done
case "$USER" in
  root) export HOME="$TEST_ROOT_HOME" ;;
  developer) export HOME="$TEST_USER_HOME" ;;
  *) exit 90 ;;
esac
export LOGNAME="$USER" SHELL=/bin/bash
command=${1//\/usr\/local\/share\/devwright\/dotfiles/$TEST_SHARED_PATH}
exec /bin/bash -c "$command"
`)
			// Simulate ownership setup for an unprivileged test process. The real
			// VM acceptance test checks root ownership and access as another UID.
			writeTest(t, filepath.Join(bin, "install"), `#!/bin/bash
set -eu
while [ "$#" -gt 1 ]; do
  case "$1" in -o|-g|-m) shift ;; esac
  shift
done
mkdir -p "$1"
`)
			writeTest(t, filepath.Join(bin, "id"), `#!/bin/bash
if [ "$*" = '-g -- developer' ]; then exec /usr/bin/id -g; fi
exec /usr/bin/id "$@"
`)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			writeTest(t, filepath.Join(s.Dir, "devwright/dotfiles.bundle"), "already transferred")
			for _, target := range []string{filepath.Join(rootHome, ".local/share/devwright/dotfiles"), filepath.Join(s.Config.User.Home, ".local/share/devwright/dotfiles"), shared} {
				writeTest(t, filepath.Join(target, ".git/devwright-source"), "selected-commit")
				writeTest(t, filepath.Join(target, "install"), `#!/bin/bash
set -eu
test "$DEVWRIGHT_USER" = developer
test "$DEVWRIGHT_HOME" = "$TEST_USER_HOME"
test "$DEVWRIGHT_UID" = 1234
test "$USER" = "$LOGNAME"
printf '%s\n' "$USER" >> "$TEST_INSTALL_LOG"
test "$USER" != "$TEST_FAIL_ACCOUNT"
if [ "$USER" = root ]; then
  test "$HOME" = "$TEST_ROOT_HOME"
else
  test "$HOME" = "$DEVWRIGHT_HOME"
fi
`)
			}
			m := manifest{Dotfiles: &repository{Commit: "selected-commit", Branch: "main"}, Installer: "install", RootDotfiles: tc.root}
			err := a.installDotfiles(s, m)
			if tc.fail == "" && err != nil {
				t.Fatal(err)
			}
			if tc.fail != "" && (err == nil || !strings.Contains(err.Error(), "dotfiles installer as "+tc.fail)) {
				t.Fatalf("expected failure as %s, got %v", tc.fail, err)
			}
			checkLog := func(want string) {
				t.Helper()
				b, err := os.ReadFile(log)
				if err != nil || string(b) != want {
					t.Fatalf("installer log = %q, %v; want %q", b, err, want)
				}
			}
			checkLog(tc.first)
			t.Setenv("TEST_FAIL_ACCOUNT", "")
			for range 2 {
				if err := a.installDotfiles(s, m); err != nil {
					t.Fatal(err)
				}
				checkLog(tc.resumed)
			}
			if !tc.root {
				if _, err := os.Stat(filepath.Join(rootHome, ".local/share/devwright/dotfiles/.git/devwright-installed")); !os.IsNotExist(err) {
					t.Fatalf("unexpected root install marker: %v", err)
				}
			}
			if tc.root {
				for _, home := range []string{rootHome, s.Config.User.Home} {
					if _, err := os.Stat(filepath.Join(home, ".local/share/devwright/dotfiles/.git/devwright-installed")); !os.IsNotExist(err) {
						t.Fatalf("unexpected private checkout install marker: %v", err)
					}
				}
				info, err := os.Stat(filepath.Join(shared, "install"))
				if err != nil || info.Mode().Perm()&0077 != 0050 {
					t.Fatalf("shared installer must be executable/readable, not writable by the group: %v, %v", info, err)
				}
			}
		})
	}
}

func TestDeclaredCredentialsInUserShells(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "sh"} {
		t.Run(shell, func(t *testing.T) {
			program, err := exec.LookPath(shell)
			if err != nil {
				t.Skip(shell + " is not installed")
			}
			home := t.TempDir()
			t.Setenv("HOME", home)
			directory := filepath.Join(home, ".config/devwright/credentials.d")
			if err := os.MkdirAll(directory, 0700); err != nil {
				t.Fatal(err)
			}
			writeTest(t, filepath.Join(home, ".config/devwright/environment.sh"), "export DEVWRIGHT_TEST_ENV=loaded\n")
			for _, value := range []string{"", "synthetic"} {
				if value != "" {
					writeTest(t, filepath.Join(directory, "TOKEN.sh"), "export DEVWRIGHT_TEST_VALUE="+quote(value)+"\n")
				}
				// The production loader must export into the caller's shell, while
				// leaving Zsh's usual unmatched-glob behavior in place afterward.
				script := "set -eu\nunset DEVWRIGHT_TEST_VALUE\n" + declaredCredentialsLoader + `
test "$DEVWRIGHT_TEST_ENV" = loaded
test "${DEVWRIGHT_TEST_VALUE-}" = ` + quote(value) + `
if [ -n "${ZSH_VERSION-}" ]; then [[ -o nomatch ]]; fi
`
				c := exec.Command(program, "-f", "-c", script)
				// In Bash/sh, -f disables globbing; use it only for Zsh's startup
				// suppression. All shells must actually expand the credential glob.
				if shell != "zsh" {
					c = exec.Command(program, "-c", script)
				}
				if b, err := c.CombinedOutput(); err != nil || len(b) != 0 {
					t.Fatalf("%s loader (value %q): %v: %s", shell, value, err, b)
				}
			}
		})
	}
}

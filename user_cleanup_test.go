package devsandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func acceptanceFunction(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("tests/users-macos.sh")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(data), name+"() {\n")
	if !ok {
		t.Fatalf("missing acceptance function %s", name)
	}
	body, _, ok := strings.Cut(rest, "\n}\n")
	if !ok {
		t.Fatalf("unterminated acceptance function %s", name)
	}
	return name + "() {\n" + body + "\n}\n"
}

func TestUserProcessDiagnostics(t *testing.T) {
	text := " 0 1 /sbin/launchd\n1002 22 /usr/sbin/distnoted\n1002 23 /usr/sbin/cfprefsd\n1003 24 /bin/sleep\n1002 25 /bin/sh\n"
	want := []string{"22 (/usr/sbin/distnoted)", "23 (/usr/sbin/cfprefsd)", "25 (/bin/sh)"}
	if got := describeUserProcesses(text, 1002); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMacOSFixtureHelperFilter(t *testing.T) {
	function := acceptanceFunction(t, "only_macos_helpers")
	for _, tc := range []struct {
		name, processes string
		allowed         bool
	}{
		{"Apple helpers", "1002 22 1 /usr/sbin/distnoted\n1002 23 1 /usr/sbin/cfprefsd\n", true},
		{"one helper", "1002 22 1 /usr/sbin/distnoted\n", true},
		{"no processes", "", false},
		{"other UID", "501 22 1 /usr/sbin/distnoted\n", false},
		{"active workload", "1002 22 1 /usr/sbin/distnoted\n1002 23 10 /bin/sh\n", false},
		{"lookalike path", "1002 22 1 /tmp/distnoted\n", false},
		{"unmanaged child", "1002 22 99 /usr/sbin/cfprefsd\n", false},
		{"unexpected daemon", "1002 22 1 /usr/libexec/other\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("/bin/bash", "-c", function+"only_macos_helpers 1002\n")
			cmd.Stdin = strings.NewReader(tc.processes)
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.allowed {
				t.Fatalf("err=%v, output=%s", err, out)
			}
		})
	}
}

func TestMacOSFixtureDomainCleanup(t *testing.T) {
	functions := acceptanceFunction(t, "only_macos_helpers") + acceptanceFunction(t, "wait_idle")
	for _, mode := range []string{"helpers", "idle", "workload", "identity-mismatch", "bootout-failed", "linux"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			script := `set -eu
fixture=$1
mode=$2
uname() { if [ "$mode" = linux ]; then echo Linux; else echo Darwin; fi; }
id() { echo 1002; }
macos_fixture_uid() { test "$mode" != identity-mismatch || return 1; echo 1002; }
seq() { printf '1\n2\n'; }
sleep() { :; }
ps() {
  printf '501 50 1 /usr/sbin/distnoted\n'
  test "$mode" != idle && test ! -f "$fixture/stopped" || return 0
  printf '1002 22 1 /usr/sbin/distnoted\n1002 23 1 /usr/sbin/cfprefsd\n'
  if [ "$mode" = workload ]; then printf '1002 24 5 /bin/sh\n'; fi
}

sudo() {
  test "$*" = '-n /bin/launchctl bootout user/1002' || return 91
  printf '%s\n' "$*" > "$fixture/bootout-called"
  test "$mode" != bootout-failed || return 1
  touch "$fixture/stopped"
}
` + functions + "wait_idle fixture\n"
			out, err := exec.Command("/bin/bash", "-c", script, "fixture", dir, mode).CombinedOutput()
			wantSuccess := mode == "helpers" || mode == "idle"
			if (err == nil) != wantSuccess {
				t.Fatalf("err=%v, output=%s", err, out)
			}
			_, callErr := os.Stat(filepath.Join(dir, "bootout-called"))
			wantBootout := mode == "helpers" || mode == "bootout-failed"
			if (callErr == nil) != wantBootout {
				t.Fatalf("unexpected bootout call: %v; %s", callErr, out)
			}
		})
	}
}

func TestMacOSFixtureIdentityBeforeDomainCleanup(t *testing.T) {
	function := acceptanceFunction(t, "macos_fixture_uid")
	for _, mode := range []string{"valid", "foreign-name", "registry-symlink", "registry-owner", "registry-mode", "operator", "uid", "gid", "guid", "home", "record-account", "record-home", "root-uid", "version", "record-name"} {
		t.Run(mode, func(t *testing.T) {
			// Bash functions intercept only this test's system calls; the shipped
			// script continues to use fixed paths for Directory Services/plutil.
			script := `set -eu
mode=$1
base=/private/var/db/dev-sandbox
homes=/Users
names=(nta ntb)
id() {
  if [ "$#" = 1 ]; then echo 501
  elif [ "$1" = -g ] && [ "$mode" = gid ]; then echo 1999
  elif [ "$1" = -u ] && [ "$mode" = uid ]; then echo 1999
  else echo 1002; fi
}
function /usr/bin/dscl() {
  case "$4" in
    GeneratedUID) if [ "$mode" = guid ]; then echo 'GeneratedUID: DIFFERENT'; else echo 'GeneratedUID: FIXTURE-GUID'; fi ;;
    NFSHomeDirectory) if [ "$mode" = home ]; then echo 'NFSHomeDirectory: /Users/unrelated'; else echo 'NFSHomeDirectory: /Users/dsb-nta'; fi ;;
    *) return 91 ;;
  esac
}
sudo() {
  test "$1" = -n || return 91
  shift
  case "$1" in
    test) test "$2" != '!' || test "$mode" != registry-symlink ;;
    /usr/bin/stat)
      case "$mode" in registry-owner) echo '501 600' ;; registry-mode) echo '0 666' ;; *) echo '0 600' ;; esac ;;
    /usr/bin/plutil)
      test "$2" = -extract || return 91
      case "$3" in
        uid) if [ "$mode" = root-uid ]; then echo 0; else echo 1002; fi ;;
        gid) echo 1002 ;;
        guid) echo FIXTURE-GUID ;;
        owner) if [ "$mode" = operator ]; then echo 502; else echo 501; fi ;;
        version) if [ "$mode" = version ]; then echo 2; else echo 1; fi ;;
        name) if [ "$mode" = record-name ]; then echo ntb; else echo nta; fi ;;
        account) if [ "$mode" = record-account ]; then echo dsb-other; else echo dsb-nta; fi ;;
        home) if [ "$mode" = record-home ]; then echo /Users/unrelated; else echo /Users/dsb-nta; fi ;;
        *) return 91 ;;
      esac ;;
    *) return 91 ;;
  esac
}
` + function + `
if [ "$mode" = foreign-name ]; then macos_fixture_uid other; else macos_fixture_uid nta; fi
`
			out, err := exec.Command("/bin/bash", "-c", script, "fixture", mode).CombinedOutput()
			if mode == "valid" {
				if err != nil || strings.TrimSpace(string(out)) != "1002" {
					t.Fatalf("valid fixture: %v, %s", err, out)
				}
			} else if err == nil || strings.TrimSpace(string(out)) != "" {
				t.Fatalf("accepted mismatched fixture: %v, %s", err, out)
			}
		})
	}
}

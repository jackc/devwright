package launch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnboardingSSHDisablesConnectionSharing(t *testing.T) {
	realSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH is not installed")
	}
	bin := t.TempDir()
	// Evaluate the actual production arguments without opening a connection.
	writeTest(t, filepath.Join(bin, "ssh"), "#!/bin/sh\nexec "+quote(realSSH)+" -G \"$@\"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := instance{Name: "test", Dir: t.TempDir(), Status: "Running"}
	// Include wildcard defaults to ensure configuration cannot re-enable sharing.
	defaults := filepath.Join(s.Dir, "defaults.config")
	writeTest(t, defaults, "Host *\n  ControlMaster auto\n  ControlPath ~/.ssh/cm-%C\n  ControlPersist 60s\n")
	writeTest(t, filepath.Join(s.Dir, "ssh.config"), "Include \""+defaults+"\"\nHost lima-test\n  HostName 127.0.0.1\n  Port 60022\n  User dev\n")
	for _, user := range []string{"dev", "root"} {
		t.Run(user, func(t *testing.T) {
			a := testApp(t)
			var output, stderr strings.Builder
			a.err = &stderr
			if err := a.ssh(s, user, "true", nil, &output); err != nil {
				t.Fatalf("SSH config evaluation: %v: %s", err, stderr.String())
			}
			config := map[string]string{}
			for _, line := range strings.Split(output.String(), "\n") {
				key, value, ok := strings.Cut(line, " ")
				if ok {
					config[key] = value
				}
			}
			for key, want := range map[string]string{
				"controlmaster": "false", "controlpersist": "no",
				"user": user, "hostname": "127.0.0.1", "port": "60022",
			} {
				if got := config[key]; got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
			// OpenSSH may omit disabled ControlPath or render it as 'none'.
			if got := config["controlpath"]; got != "" && got != "none" {
				t.Errorf("connection sharing still has a socket path: %q", got)
			}
		})
	}
}

package devsandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSSHInstallPreservesAndUpdates(t *testing.T) {
	v := testVM(t, "install-ssh")
	state := testState(t, v)
	target := filepath.Join(v.home, ".ssh/dev-sandbox/test.config")
	config := filepath.Join(v.home, ".ssh/config")
	original := "Host personal\n  HostName example.invalid\n"
	writeTestFile(t, target, sshHeader+"old alias")
	writeTestFile(t, config, original)
	for i := 0; i < 2; i++ {
		if err := v.installSSH(state); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(target)
	if string(data) != sshHeader+sshConfig(state) {
		t.Fatalf("SSH entry not updated: %s", data)
	}
	data, _ = os.ReadFile(config)
	if string(data) != sshInclude+"\n\n"+original {
		t.Fatalf("personal config changed: %s", data)
	}
	backups, _ := filepath.Glob(config + ".before-dev-sandbox-*")
	if len(backups) != 1 {
		t.Fatalf("backup count: %v", backups)
	}
	data, _ = os.ReadFile(backups[0])
	if string(data) != original {
		t.Fatal("backup changed")
	}
	for _, path := range []string{config, target, backups[0]} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("file permissions: %s %v", path, err)
		}
	}
}

func TestSSHInstallRefusesUnmanagedAndSymlinks(t *testing.T) {
	for _, kind := range []string{"unmanaged", "empty", "target-symlink", "config-symlink", "dangling-config-symlink"} {
		t.Run(kind, func(t *testing.T) {
			v := testVM(t, "install-ssh")
			state := testState(t, v)
			target := filepath.Join(v.home, ".ssh/dev-sandbox/test.config")
			config := filepath.Join(v.home, ".ssh/config")
			personal := filepath.Join(v.home, "personal")
			writeTestFile(t, target, sshHeader+"old")
			writeTestFile(t, personal, "personal content")
			switch kind {
			case "unmanaged":
				writeTestFile(t, target, "personal content")
			case "empty":
				writeTestFile(t, target, "")
			case "target-symlink":
				os.Remove(target)
				if err := os.Symlink(personal, target); err != nil {
					t.Fatal(err)
				}
			case "config-symlink":
				if err := os.Symlink(personal, config); err != nil {
					t.Fatal(err)
				}
			case "dangling-config-symlink":
				if err := os.Symlink(filepath.Join(v.home, "absent"), config); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(target)
			if err := v.installSSH(state); err == nil {
				t.Fatal("overwrote unmanaged or symlinked file")
			}
			after, _ := os.ReadFile(target)
			if string(before) != string(after) {
				t.Fatal("modified generated entry before refusing config")
			}
			data, _ := os.ReadFile(personal)
			if string(data) != "personal content" {
				t.Fatal("modified symlink destination")
			}
		})
	}
}

func TestSSHConfigSymlinkWithIncludeIsPreserved(t *testing.T) {
	v := testVM(t, "install-ssh")
	state := testState(t, v)
	source := filepath.Join(v.home, "dotfiles/ssh")
	writeTestFile(t, source, sshInclude+"\nHost personal\n")
	if err := os.Mkdir(filepath.Join(v.home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(v.home, ".ssh/config")
	if err := os.Symlink(source, config); err != nil {
		t.Fatal(err)
	}
	if err := v.installSSH(state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(config)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("replaced existing config symlink")
	}
}

func TestSSHUsesCurrentLimaConfigAndSeparatesUsers(t *testing.T) {
	v := testVM(t, "ssh-config")
	state := testState(t, v)
	state.Dir = filepath.Join(t.TempDir(), "a path")
	wrapper := filepath.Join(state.Dir, "wrapper.config")
	writeTestFile(t, wrapper, sshConfig(state))
	sockets := map[string]bool{}
	for _, port := range []int{1234, 5678} {
		writeTestFile(t, filepath.Join(state.Dir, "ssh.config"), fmt.Sprintf("Host lima-test\n  HostName 127.0.0.1\n  Port %d\n  User dev\n  ControlMaster auto\n  ControlPath /tmp/admin-socket\n", port))
		for _, user := range []string{"dev", "root"} {
			destination := "lima-test"
			if user == "root" {
				destination = "root@" + destination
			}
			output, err := exec.Command("ssh", "-G", "-T", "-F", wrapper, destination).Output()
			if err != nil {
				t.Fatal(err)
			}
			values := map[string]string{}
			for _, line := range strings.Split(string(output), "\n") {
				key, value, _ := strings.Cut(line, " ")
				values[key] = value
			}
			for key, want := range map[string]string{"port": fmt.Sprint(port), "user": user, "controlmaster": "auto", "controlpersist": "60", "identityagent": "none", "forwardagent": "no"} {
				if values[key] != want {
					t.Fatalf("%s: got %q, want %q", key, values[key], want)
				}
			}
			sockets[values["controlpath"]] = true
		}
	}
	if len(sockets) != 4 {
		t.Fatalf("users and ports share sockets: %v", sockets)
	}
}

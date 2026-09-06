package verification

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func Check(out io.Writer) error {
	dev, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil {
		return err
	}
	if dev.Username != "dev" {
		return errors.New("Must run as dev")
	}
	if _, ok := os.LookupEnv("SSH_AUTH_SOCK"); ok {
		return errors.New("Agent socket was forwarded")
	}
	if _, _, err := probe("sudo", "-n", "true"); err == nil {
		return errors.New("dev has sudo access")
	} else {
		var status interface{ ExitCode() int }
		if !errors.As(err, &status) {
			return fmt.Errorf("sudo probe: %w", err)
		}
	}
	groups, err := os.Getgroups()
	if err != nil {
		return err
	}
	for _, gid := range groups {
		if strconv.Itoa(gid) != dev.Gid {
			return errors.New("Unexpected supplementary groups")
		}
	}
	if _, err := os.ReadDir("/root"); !errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("Administrator home access did not fail with permission denial: %v", err)
	}
	for _, path := range []string{"/etc/codex", "/etc/codex/requirements.toml", "/usr/local/bin", "/usr/local/share/agent-vm"} {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Sys().(*syscall.Stat_t).Uid != 0 || unix.Access(path, unix.W_OK) == nil {
			return fmt.Errorf("Writable policy/tool path: %s", path)
		}
	}
	mounts, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return err
	}
	for _, kind := range []string{"virtiofs", "9p", "fuse.sshfs"} {
		if strings.Contains(string(mounts), kind) {
			return errors.New("Unexpected shared filesystem")
		}
	}
	if _, err := os.Lstat("/run/host-services/ssh-auth.sock"); err == nil {
		return errors.New("Host agent socket is present")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if unix.Access("/var/run/docker.sock", unix.R_OK|unix.W_OK) == nil {
		return errors.New("Docker socket accessible")
	}
	expected, err := os.ReadFile("/usr/local/share/agent-vm/requirements.sha256")
	if err != nil {
		return err
	}
	policy, err := os.ReadFile("/etc/codex/requirements.toml")
	if err != nil {
		return err
	}
	fields := strings.Fields(string(expected))
	if len(fields) == 0 || fmt.Sprintf("%x", sha256.Sum256(policy)) != fields[0] {
		return errors.New("Managed policy differs from provisioned recipe")
	}
	stdout, stderr, err := probe("codex", "--version")
	if err != nil || !strings.HasPrefix(stdout, "codex-cli ") {
		return fmt.Errorf("Codex version check failed: %v: %s", err, stderr)
	}
	fmt.Fprintf(out, "Codex installed: %s\n", strings.TrimSpace(stdout))
	fmt.Fprintln(out, "PASS Linux account, policy ownership, mounts, SSH forwarding, and Codex installation")
	if err := checkPolicy(out); err != nil {
		return err
	}
	if err := sandboxCheck(dev.HomeDir, out); err != nil {
		return err
	}
	fmt.Fprintln(out, "NOT TESTED: authenticated model run, private GitHub repository scope, desktop-provided tool inventory")
	return nil
}

const canaryText = "agent-vm-synthetic-canary\n"

func sandboxCheck(home string, out io.Writer) error {
	canary := filepath.Join(home, ".pgpass")
	if _, err := os.Lstat(canary); err == nil {
		return errors.New("Cannot run canary test: ~/.pgpass already exists; no secrets were read")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fixture, err := os.MkdirTemp(filepath.Join(home, "projects"), "verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(fixture)
	work, sibling := filepath.Join(fixture, "workspace"), filepath.Join(fixture, "outside-workspace")
	if err := os.Mkdir(work, 0700); err != nil {
		return err
	}
	if err := os.WriteFile(sibling, []byte("original"), 0600); err != nil {
		return err
	}
	file, err := os.OpenFile(canary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(canary)
	_, err = file.WriteString(canaryText)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	data, err := os.ReadFile(canary)
	if err != nil {
		return err
	}
	if string(data) != canaryText {
		return errors.New("Canary unavailable outside sandbox")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	for _, override := range []bool{false, true} {
		args := []string{"codex", "sandbox", "--include-managed-config", "-P", "vm_dev", "-C", work}
		if override {
			args = append(args, "-c", `permissions.vm_dev.filesystem."~/.pgpass"="read"`)
		}
		args = append(args, exe, "sandbox-probe", canary, sibling)
		stdout, stderr, err := probe(args...)
		if !override {
			if err != nil || !strings.Contains(stdout, "PASS Codex") {
				return fmt.Errorf("sandbox probe failed: %v: %s%s", err, stderr, stdout)
			}
			fmt.Fprintln(out, strings.TrimSpace(stdout))
		} else if err == nil || !strings.Contains(stderr, "conflicts with a config-defined profile") {
			return fmt.Errorf("conflicting override was not rejected: %v: %s%s", err, stderr, stdout)
		}
	}
	data, err = os.ReadFile(sibling)
	if err != nil {
		return err
	}
	if string(data) != "original" {
		return errors.New("Outside-workspace file changed")
	}
	fmt.Fprintln(out, "PASS conflicting config override rejected before execution")
	return nil
}

// SandboxProbe runs as a child of Codex, using only synthetic fixture paths.
func SandboxProbe(canary, sibling string, out io.Writer) error {
	if err := os.WriteFile("workspace-write-ok", []byte("ok"), 0600); err != nil {
		return err
	}
	if _, err := os.ReadFile(canary); !errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("managed deny-read did not hold: %v", err)
	}
	if err := os.WriteFile(sibling, []byte("changed"), 0600); !errors.Is(err, os.ErrPermission) && !errors.Is(err, syscall.EROFS) {
		return fmt.Errorf("outside-workspace write denial did not hold: %v", err)
	}
	fmt.Fprintln(out, "PASS Codex workspace write, outside-workspace write denial, and managed secret read denial")
	return nil
}

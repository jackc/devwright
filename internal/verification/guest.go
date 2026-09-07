package verification

import (
	"crypto/sha256"
	"devwright/internal/codexpolicy"
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
	for _, path := range []string{"/etc/codex", "/etc/codex/requirements.toml", "/usr/local/bin", "/usr/local/share/devwright", "/etc/claude-code", "/etc/claude-code/managed-settings.json", "/usr/bin/claude"} {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Sys().(*syscall.Stat_t).Uid != 0 || unix.Access(path, unix.W_OK) == nil {
			return fmt.Errorf("Writable policy/tool path: %s", path)
		}
	}
	// Check ownership and permissions without reading credential values.
	credentials := filepath.Join(dev.HomeDir, ".config/devwright/credentials.sh")
	info, err := os.Stat(credentials)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 ||
		int(info.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() ||
		unix.Access(credentials, unix.R_OK|unix.W_OK) != nil {
		return errors.New("Expected dev-owned ~/.config/devwright/credentials.sh with mode 0600")
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
	expected, err := os.ReadFile("/usr/local/share/devwright/requirements.sha256")
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
	selected, err := codexpolicy.Parse(policy)
	if err != nil {
		return fmt.Errorf("managed requirements: %w", err)
	}
	var failures []error
	if err := checkSelectedPolicy(out, selected); err != nil {
		fmt.Fprintf(out, "FAIL Codex selected policy: %v\n", err)
		failures = append(failures, err)
	}
	bundled, err := os.ReadFile("/usr/local/share/devwright/default-requirements.toml")
	if err != nil {
		failures = append(failures, err)
	} else if err := codexBehaviorCheck(dev.HomeDir, policy, bundled, selected, out); err != nil {
		failures = append(failures, err)
	}
	if err := checkClaude(dev.HomeDir, out); err != nil {
		failures = append(failures, err)
	}
	fmt.Fprintln(out, "NOT TESTED: authenticated model run, private GitHub repository scope, desktop-provided tool inventory")
	return errors.Join(failures...)
}

const canaryText = "devwright-synthetic-canary\n"

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

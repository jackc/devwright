package verification

import (
	"dev-sandbox/internal/userpolicy"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func nativeIdentity(account, home string) error {
	u, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil {
		return err
	}
	if os.Geteuid() == 0 || u.Username != account || u.HomeDir != home || os.Getenv("HOME") != home {
		return errors.New("native probe must run as the specified restricted account with its HOME")
	}
	return nil
}

const credentialsHook = `# BEGIN DEV-SANDBOX CREDENTIALS
if [ -r "$HOME/.config/dev-sandbox/credentials.sh" ]; then
  . "$HOME/.config/dev-sandbox/credentials.sh"
fi
# END DEV-SANDBOX CREDENTIALS`
const pathHook = `# BEGIN DEV-SANDBOX PATH
export PATH="$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin"
# END DEV-SANDBOX PATH`

// StartupHook preserves user content and refuses malformed/overlapping blocks.
func StartupHook(content, begin, end, hook string) (string, error) {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if content == "" {
		lines = nil
	}
	var kept []string
	inside := false
	for _, line := range lines {
		switch line {
		case begin:
			if inside {
				return "", errors.New("malformed startup block")
			}
			inside = true
		case end:
			if !inside {
				return "", errors.New("malformed startup block")
			}
			inside = false
		default:
			if !inside {
				kept = append(kept, line)
			}
		}
	}
	if inside {
		return "", errors.New("malformed startup block")
	}
	prefix := ""
	if len(kept) > 0 && strings.HasPrefix(kept[0], "#!") {
		prefix = kept[0] + "\n"
		kept = kept[1:]
	}
	result := prefix + hook + "\n"
	if len(kept) > 0 {
		result += strings.Join(kept, "\n") + "\n"
	}
	return result, nil
}
func SetupUser(account, home string) error {
	if err := nativeIdentity(account, home); err != nil {
		return err
	}
	dir := filepath.Join(home, ".config", "dev-sandbox")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	credentials := filepath.Join(dir, "credentials.sh")
	file, err := os.OpenFile(credentials, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		_, err = file.WriteString("# Private credentials for this account's shells and child processes.\n# export GH_TOKEN='replace-me'\n# export OTHER_API_KEY='replace-me'\n")
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Stat(credentials)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || int(info.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
		return errors.New("credential file must be owned by the restricted account")
	}
	if err := os.Chmod(credentials, 0600); err != nil {
		return err
	}
	for _, name := range []string{".profile", ".bashrc", ".bash_profile", ".bash_login", ".zshenv"} {
		path := filepath.Join(home, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) && (name == ".bash_profile" || name == ".bash_login") {
			continue
		}
		mode := os.FileMode(0600)
		content := []byte{}
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				path, err = filepath.EvalSymlinks(path)
				if err != nil {
					return fmt.Errorf("dangling startup symlink: %s", name)
				}
			}
			info, err = os.Stat(path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("startup file is not regular")
			}
			mode = info.Mode().Perm()
			content, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// Insert credentials first, then PATH ahead of it; both precede early returns.
		next, err := StartupHook(string(content), "# BEGIN DEV-SANDBOX CREDENTIALS", "# END DEV-SANDBOX CREDENTIALS", credentialsHook)
		if err != nil {
			return err
		}
		next, err = StartupHook(next, "# BEGIN DEV-SANDBOX PATH", "# END DEV-SANDBOX PATH", pathHook)
		if err != nil {
			return err
		}
		if next == string(content) {
			continue
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), ".dev-sandbox-startup-")
		if err != nil {
			return err
		}
		if err := tmp.Chmod(mode); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return err
		}
		_, writeErr := tmp.WriteString(next)
		closeErr := tmp.Close()
		if writeErr == nil {
			writeErr = closeErr
		}
		if writeErr == nil {
			writeErr = os.Rename(tmp.Name(), path)
		}
		os.Remove(tmp.Name())
		if writeErr != nil {
			return writeErr
		}
	}
	return nil
}
func CheckNative(account, home string, out io.Writer) error {
	if err := nativeIdentity(account, home); err != nil {
		return err
	}
	if _, ok := os.LookupEnv("SSH_AUTH_SOCK"); ok {
		return errors.New("SSH agent environment present")
	}
	info, err := os.Stat(home)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0700 {
		return errors.New("managed home must have mode 0700")
	}
	groups, _, err := probe("/usr/bin/id", "-Gn")
	if err != nil {
		return err
	}
	if err := userpolicy.CheckGroups(runtime.GOOS, account, groups, func(name string) (string, error) {
		record, _, err := probe("/usr/bin/dscl", ".", "-read", "/Groups/"+name)
		return record, err
	}); err != nil {
		return err
	}
	if _, _, err := probe("/usr/bin/sudo", "-n", "true"); err == nil {
		return errors.New("restricted account can sudo")
	}
	credentials := filepath.Join(home, ".config", "dev-sandbox", "credentials.sh")
	info, err = os.Stat(credentials)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || int(info.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
		return errors.New("credentials must be account-owned with mode 0600")
	}
	base := "/var/lib/dev-sandbox"
	if runtime.GOOS == "darwin" {
		base = "/private/var/db/dev-sandbox"
	}
	for _, path := range []string{base, filepath.Join(base, "users"), "/etc/ssh/dev-sandbox", "/etc/sudoers.d"} {
		if unix.Access(path, unix.W_OK) == nil {
			return fmt.Errorf("writable management path: %s", path)
		}
	}
	for _, path := range []string{"/var/run/docker.sock", "/run/incus/unix.socket", "/var/lib/incus/unix.socket", "/run/libvirt/libvirt-sock"} {
		if unix.Access(path, unix.R_OK|unix.W_OK) == nil {
			return fmt.Errorf("privileged host socket accessible: %s", path)
		}
	}
	codex := filepath.Join(home, ".local", "bin", "codex")
	version, stderr, err := probe(codex, "--version")
	if err != nil {
		return fmt.Errorf("Codex installation: %w: %s", err, stderr)
	}
	fmt.Fprintln(out, "PASS native account, private home/credentials, groups, management paths, and no SSH agent")
	fmt.Fprintln(out, strings.TrimSpace(version))
	// Strict config parsing does not require authentication or a model invocation.
	stdout, stderr, err := probe(codex, "features", "list")
	if err != nil {
		return fmt.Errorf("Codex configuration: %w: %s%s", err, stdout, stderr)
	}
	work, err := os.MkdirTemp(filepath.Join(home, "projects"), "verify-native-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	sibling := filepath.Join(home, filepath.Base(work)+"-outside")
	file, err := os.OpenFile(sibling, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	file.WriteString("original")
	file.Close()
	defer os.Remove(sibling)
	// The built-in workspace profile checks ordinary defaults; this is explicitly
	// not a claim that editable account defaults are managed requirements.
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	stdout, stderr, err = probe(codex, "sandbox", "--include-managed-config", "-P", ":workspace", "-C", work, exe, "native-sandbox", sibling)
	if err != nil {
		return fmt.Errorf("Codex workspace sandbox (check host namespace/OS support and host requirements): %w: %s%s", err, stdout, stderr)
	}
	fmt.Fprintln(out, strings.TrimSpace(stdout))
	fmt.Fprintln(out, "PASS Codex startup and workspace sandbox; user defaults are editable, not enforced policy")
	claude := filepath.Join(home, ".local", "bin", "claude")
	version, stderr, err = probe(claude, "--version")
	if err != nil {
		return fmt.Errorf("Claude Code installation: %w: %s", err, stderr)
	}
	if _, err := parseClaudeVersion(version); err != nil {
		return err
	}
	fmt.Fprintln(out, strings.TrimSpace(version))
	if err := checkClaudeUserSettings(home); err != nil {
		return err
	}
	if err := nativeClaudeCheck(claude, home, exe, out); err != nil {
		return err
	}
	fmt.Fprintln(out, "PASS Claude Code startup and workspace sandbox; user defaults are editable, not enforced policy")
	fmt.Fprintln(out, "NOT TESTED: authenticated model run, private repository scope, desktop tools, arbitrary host vulnerabilities or resource isolation")
	return nil
}
func NativeSandboxProbe(sibling string, out io.Writer) error {
	if err := os.WriteFile("workspace-write-ok", []byte("ok"), 0600); err != nil {
		return err
	}
	if err := os.WriteFile(sibling, []byte("changed"), 0600); !errors.Is(err, os.ErrPermission) && !errors.Is(err, syscall.EROFS) {
		return fmt.Errorf("outside-workspace write was not denied: %v", err)
	}
	fmt.Fprintln(out, "PASS Codex workspace write and outside-workspace write denial")
	return nil
}

// WriteUserConfig installs the account's Codex defaults.
func WriteUserConfig(account, home string, replace bool, data []byte) error {
	return WriteUserSettings(account, home, ".codex", "config.toml", replace, data)
}

// WriteUserSettings performs leaf replacement portably, including replacing a
// symlink itself rather than following it. The whole operation runs as the user.
func WriteUserSettings(account, home, subdir, name string, replace bool, data []byte) error {
	if err := nativeIdentity(account, home); err != nil {
		return err
	}
	dir := filepath.Join(home, subdir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	if _, err := os.Lstat(path); err == nil && !replace {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

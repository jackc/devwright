package devsandbox

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/sys/unix"
)

type userAdmin struct {
	ctx             context.Context
	out             io.Writer
	goos, base, etc string
	owner           int
}

func newUserAdmin(ctx context.Context, out io.Writer) *userAdmin {
	a := &userAdmin{ctx: ctx, out: out, goos: runtime.GOOS, base: "/var/lib/dev-sandbox", etc: "/etc", owner: 0}
	if a.goos == "darwin" {
		a.base = "/private/var/db/dev-sandbox"
		a.etc = "/private/etc"
	}
	if v := os.Getenv("SUDO_UID"); v != "" {
		if n, e := strconv.Atoi(v); e == nil {
			a.owner = n
		}
	}
	return a
}
func (a *userAdmin) command(input io.Reader, args ...string) (string, error) {
	return a.commandIn("/", input, args...)
}
func (a *userAdmin) commandIn(dir string, input io.Reader, args ...string) (string, error) {
	cmd := exec.CommandContext(a.ctx, args[0], args[1:]...)
	// Never inherit an operator's private (or deleted) working directory across
	// a privilege boundary. Target-user commands explicitly select their home.
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=/var/root", "LANG=C", "LC_ALL=C"}
	if a.goos == "linux" {
		cmd.Env[1] = "HOME=/root"
	}
	cmd.Stdin = input
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = a.out
	if err := cmd.Run(); err != nil {
		return output.String(), fmt.Errorf("%s: %w", filepath.Base(args[0]), err)
	}
	return output.String(), nil
}
func validateUserRequest(r userRequest) error {
	action := r.Action
	if action == "create-check" {
		action = "create"
	}
	args := []string{action, "--backend", "user"}
	if action != "list" {
		args = append(args, r.Name)
	}
	if r.PortSet {
		args = append(args, "--ssh-port", strconv.Itoa(r.Port))
	}
	if r.RemoveHome {
		args = append(args, "--remove-home")
	}
	if r.Repository != "" {
		args = append(args, "--dotfiles-repo", r.Repository, "--dotfiles-install", r.Installer)
	}
	if len(r.Config) > 0 {
		args = append(args, "--codex-config", "payload")
	}
	if r.ReplaceConfig {
		args = append(args, "--replace-codex-config")
	}
	if len(r.ClaudeConfig) > 0 {
		args = append(args, "--claude-config", "payload")
	}
	if r.ReplaceClaudeConfig {
		args = append(args, "--replace-claude-config")
	}
	if _, err := parseOptions(args); err != nil {
		return err
	}
	if r.Port < 1 || r.Port > 65535 {
		return errors.New("invalid SSH port")
	}
	if len(r.Config) > 0 {
		var v map[string]any
		if err := toml.Unmarshal(r.Config, &v); err != nil {
			return err
		}
	}
	if len(r.ClaudeConfig) > 0 {
		if err := checkClaudeSettings(r.ClaudeConfig); err != nil {
			return err
		}
	}
	if r.PublicKey != "" {
		if r.Action != "create" {
			return errors.New("key enrollment is creation-only")
		}
		if err := validUserKey(r.PublicKey); err != nil {
			return err
		}
	}
	if r.Action == "create" && r.PublicKey == "" {
		return errors.New("missing creation key")
	}
	return nil
}
func validUserKey(key string) error {
	if strings.ContainsAny(key, "\r\n\x00") {
		return errors.New("invalid public key")
	}
	fields := strings.Fields(key)
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return errors.New("expected Ed25519 public key")
	}
	data, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil || len(data) != 51 || !bytes.Equal(data[:19], append([]byte{0, 0, 0, 11}, []byte("ssh-ed25519\x00\x00\x00\x20")...)) {
		return errors.New("invalid Ed25519 key encoding")
	}
	return nil
}
func runUserHelper(ctx context.Context, input io.Reader, output, diagnostics io.Writer) error {
	if os.Geteuid() != 0 {
		return errors.New("native helper requires sudo/root")
	}
	var req userRequest
	dec := json.NewDecoder(io.LimitReader(input, 2*1024*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errors.New("unexpected helper input")
	}
	if err := validateUserRequest(req); err != nil {
		return err
	}
	a := newUserAdmin(ctx, diagnostics)
	if a.goos != "linux" && a.goos != "darwin" {
		return errors.New("unsupported native OS")
	}
	if err := secureUserDir(a.base, 0755); err != nil {
		return err
	}
	if err := secureUserDir(filepath.Join(a.base, "users"), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(a.base, "users", ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("another native administration operation is running")
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	reply, err := a.execute(req)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(reply)
}

// Every privileged writable ancestor must be root-owned and non-writable by
// other users. macOS callers use canonical /private paths, never /var or /etc.
func secureUserDir(path string, mode os.FileMode) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("invalid administrative path")
	}
	parent := filepath.Dir(path)
	if parent != path {
		if _, err := os.Lstat(parent); errors.Is(err, os.ErrNotExist) {
			if err := secureUserDir(parent, 0755); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	if err := os.Mkdir(path, mode); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if mode == 0700 {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode().Perm() != 0700 {
			return fmt.Errorf("private administrative directory has unsafe permissions: %s", path)
		}
	}
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Sys().(*syscall.Stat_t).Uid != 0 || info.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("unsafe administrative directory: %s", p)
		}
		if err := checkDarwinACL(p); err != nil {
			return err
		}
		if p == "/" {
			break
		}
	}

	if runtime.GOOS == "darwin" && mode == 0700 {
		if err := exec.Command("/bin/chmod", "-N", path).Run(); err != nil {
			return fmt.Errorf("remove ACL from private managed directory: %w", err)
		}
	}
	return nil
}
func secureUserFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Uid != 0 || info.Mode().Perm()&0022 != 0 || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return fmt.Errorf("unsafe administrative file: %s", path)
	}
	return checkDarwinACL(path)
}
func writeUserFile(path string, data []byte, mode os.FileMode) error {
	if err := secureUserDir(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		if err := secureUserFile(path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".dev-sandbox-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func (a *userAdmin) statePath(name string) string {
	return filepath.Join(a.base, "users", name+".json")
}
func (a *userAdmin) save(s *userState) error {
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return e
	}
	return writeUserFile(a.statePath(s.Name), append(b, '\n'), 0600)
}
func (a *userAdmin) read(name string) (*userState, error) {
	p := a.statePath(name)
	if err := secureUserFile(p); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var s userState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.Version != 1 || s.Name != name || s.Account != "dsb-"+name || s.Home != userHome(name, a.goos) || s.UID < 501 || s.GID < 501 || s.Port < 1 || s.Port > 65535 {
		return nil, errors.New("invalid managed account registry")
	}
	if a.owner != 0 && a.owner != s.Owner {
		return nil, errors.New("use the operator account that created this boundary (or root)")
	}
	if err := validUserKey(s.PublicKey); err != nil {
		return nil, err
	}
	return &s, nil
}
func (a *userAdmin) execute(r userRequest) (userReply, error) {
	var reply userReply
	if r.Action == "list" {
		entries, err := os.ReadDir(filepath.Join(a.base, "users"))
		if err != nil {
			return reply, err
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			s, err := a.read(strings.TrimSuffix(e.Name(), ".json"))
			if err != nil {
				return reply, err
			}
			reply.States = append(reply.States, *s)
		}
		return reply, nil
	}
	if r.Action == "create-check" || r.Action == "create" {
		if err := a.preflight(r.Port); err != nil {
			return reply, err
		}
		for _, p := range []string{a.statePath(r.Name), userHome(r.Name, a.goos), a.sshRulePath(r.Name), a.keysPath(r.Name), a.sudoPath(r.Name)} {
			if _, err := os.Lstat(p); err == nil {
				return reply, fmt.Errorf("refusing existing account resource: %s", p)
			} else if !errors.Is(err, os.ErrNotExist) {
				return reply, err
			}
		}
		if _, err := a.lookup("dsb-" + r.Name); err == nil {
			return reply, errors.New("account already exists; adoption is unsupported")
		} else if !errors.Is(err, errUserAbsent) {
			return reply, err
		}
		if a.groupExists("dsb-" + r.Name) {
			return reply, errors.New("group already exists")
		}
		if a.goos == "darwin" {
			for _, kind := range []string{"user", "group"} {
				text, err := a.command(nil, "/usr/bin/dscacheutil", "-q", kind, "-a", "name", "dsb-"+r.Name)
				if err != nil {
					return reply, err
				}
				if strings.TrimSpace(text) != "" {
					return reply, errors.New("account/group name already exists in Directory Services")
				}
			}
		}
		if r.Action == "create-check" {
			return reply, nil
		}
		uid, gid, err := a.allocateIDs()
		if err != nil {
			return reply, err
		}
		s := &userState{Version: 1, Name: r.Name, Account: "dsb-" + r.Name, UID: uid, GID: gid, Owner: a.owner, Home: userHome(r.Name, a.goos), Port: r.Port, Phase: "creating", PublicKey: r.PublicKey}
		if a.goos == "darwin" {
			id, err := a.command(nil, "/usr/bin/uuidgen")
			if err != nil {
				return reply, err
			}
			s.GUID = strings.TrimSpace(id)
		}
		if err := a.save(s); err != nil {
			return reply, err
		}
		if err := a.createAccount(s); err != nil {
			return reply, err
		}
		s.Phase = "configuring"
		if err := a.save(s); err != nil {
			return reply, err
		}
	}
	s, err := a.read(r.Name)
	if err != nil {
		return reply, err
	}
	reply.State = s
	if s.Phase == "deleted" {
		if r.Action == "delete" {
			if r.RemoveHome && s.Archive != "" {
				if err := a.purgeArchive(s); err != nil {
					return reply, err
				}
				s.Archive = ""
				if err := a.save(s); err != nil {
					return reply, err
				}
			}
			return reply, nil
		}
		return reply, errors.New("account was deleted; archived registry reserves its identity")
	}
	if r.Action == "delete" {
		return a.deleteAccount(s, r.RemoveHome)
	}
	if s.Phase == "deleting" {
		return reply, errors.New("deletion is incomplete; rerun delete to finish")
	}
	// A creation interrupted while creating the OS account can resume only after
	// matching any partial resources to the reserved IDs in the protected registry.
	if r.Action == "configure" && s.Phase == "creating" {
		if err := a.createAccount(s); err != nil {
			return reply, err
		}
	}
	if err := a.checkIdentity(s); err != nil {
		return reply, err
	}
	if r.PortSet {
		s.Port = r.Port
	}
	if err := a.preflight(s.Port); err != nil {
		return reply, err
	}
	if r.Action == "create" || r.Action == "configure" {
		s.Phase = "configuring"
		if err := a.save(s); err != nil {
			return reply, err
		}
		if err := a.restrictAccount(s); err != nil {
			return reply, err
		}
		if err := a.installSSH(s); err != nil {
			return reply, err
		}
		if err := a.installVerifier(); err != nil {
			return reply, err
		}
		// Reject inherited privileges before executing any installer or home code.
		if err := a.audit(s); err != nil {
			return reply, err
		}
		if err := a.setupHome(s, r); err != nil {
			return reply, err
		}
	}
	if err := a.audit(s); err != nil {
		return reply, err
	}
	keys, err := a.hostKeys()
	if err != nil {
		return reply, err
	}
	reply.HostKeys = keys
	reply.Verifier = a.verifierPath()
	if r.Action == "create" || r.Action == "configure" {
		s.Phase = "configured"
		if err := a.save(s); err != nil {
			return reply, err
		}
	}
	return reply, nil
}
func (a *userAdmin) verifierPath() string {
	return filepath.Join(a.base, "bin", "verify-"+a.goos+"-"+runtime.GOARCH)
}
func (a *userAdmin) installVerifier() error {
	data, err := recipe.ReadFile("guestbin/verify-" + a.goos + "-" + runtime.GOARCH + ".gz")
	if err != nil {
		return err
	}
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return writeUserFile(a.verifierPath(), b, 0755)
}
func (a *userAdmin) preflight(port int) error {
	required := []string{"/usr/sbin/sshd", "/usr/bin/ssh-keygen", "/usr/bin/sudo", "/usr/bin/git", "/usr/bin/curl", "/bin/bash"}
	if a.goos == "linux" {
		// Claude Code's Linux sandbox needs bubblewrap and socat; the backend installs no packages.
		required = append(required, "/usr/sbin/useradd", "/usr/sbin/usermod", "/usr/sbin/userdel", "/usr/sbin/groupadd", "/usr/sbin/groupdel", "/usr/bin/getent", "/usr/bin/systemctl", "/usr/bin/bwrap", "/usr/bin/socat")
	} else {
		required = append(required, "/usr/bin/dscl", "/usr/sbin/dseditgroup", "/usr/bin/plutil", "/usr/bin/dscacheutil")
	}
	for _, p := range required {
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("missing host prerequisite %s; install host tools administratively", p)
		}
	}
	if a.goos == "linux" {
		if err := checkLinuxSandboxPolicy("/proc/sys/kernel/apparmor_restrict_unprivileged_userns", "/sys/kernel/security/apparmor/profiles"); err != nil {
			return err
		}
	}
	if _, err := a.command(nil, "/usr/bin/git", "--version"); err != nil {
		return errors.New("Git is unavailable; on macOS install Command Line Tools")
	}
	if err := validateSystemSSH(port, func() error {
		if _, err := a.command(nil, "/usr/sbin/sshd", "-t"); err != nil {
			return fmt.Errorf("system SSH configuration or host keys are invalid: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(a.etc, "ssh", "sshd_config"))
	if err != nil {
		return err
	}
	hasInclude := false
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && strings.EqualFold(f[0], "Match") {
			break
		}
		if len(f) > 1 && strings.EqualFold(f[0], "Include") {
			for _, p := range f[1:] {
				if p == "/etc/ssh/sshd_config.d/*" || p == "/etc/ssh/sshd_config.d/*.conf" || p == a.etc+"/ssh/sshd_config.d/*" || p == a.etc+"/ssh/sshd_config.d/*.conf" {
					hasInclude = true
				}
			}
		}
	}
	if !hasInclude {
		return errors.New("system sshd_config needs a global Include /etc/ssh/sshd_config.d/*.conf; add it administratively before setup")
	}
	return nil
}

// Ubuntu's unprivileged user-namespace restriction plus its stock
// bwrap-userns-restrict profile stop Claude Code's sandbox from applying its
// seccomp filter. The backend changes no host security policy, so it refuses
// to create accounts until an administrator installs the documented
// unconfined bwrap profile; see the README's native-user prerequisites.
func checkLinuxSandboxPolicy(sysctlPath, profilesPath string) error {
	restrict, err := os.ReadFile(sysctlPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(restrict)) != "1" {
		return nil
	}
	profiles, err := os.ReadFile(profilesPath)
	if err != nil {
		return fmt.Errorf("read loaded AppArmor profiles: %w", err)
	}
	for _, line := range strings.Split(string(profiles), "\n") {
		if strings.HasPrefix(line, "bwrap (") && strings.Contains(line, "unconfined") {
			return nil
		}
	}
	return errors.New("the kernel restricts unprivileged user namespaces and /usr/bin/bwrap has no unconfined AppArmor profile, so Claude Code's sandbox cannot run; install the bwrap profile from the README's native-user prerequisites administratively, then retry")
}

// macOS Remote Login creates missing host keys in its socket-activated wrapper.
// Wait for that existing service before invoking sshd directly for validation.
// The banner establishes readiness only; host keys are still pinned from disk.
func validateSystemSSH(port int, validate func() error) error {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("localhost", strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		return fmt.Errorf("existing SSH service is unreachable on localhost:%d; enable SSH/Remote Login administratively or select --ssh-port", port)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var banner [4]byte
	if _, err := io.ReadFull(conn, banner[:]); err != nil || string(banner[:]) != "SSH-" {
		return errors.New("existing service did not present an SSH server banner; check the system SSH service or macOS Remote Login and its host keys")
	}
	return validate()
}

// POSIX ACL write masks are reflected in Linux mode bits. macOS ACLs can grant
// writes independently, so reject write-capable ACL entries on privileged paths.
func checkDarwinACL(path string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	cmd := exec.Command("/bin/ls", "-lde", path)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	text, err := cmd.Output()
	if err != nil {
		return err
	}
	if !readOnlyDarwinACL(string(text)) {
		return fmt.Errorf("write-capable ACL on administrative path: %s", path)
	}
	return nil
}
func readOnlyDarwinACL(text string) bool {
	allowed := map[string]bool{"read": true, "list": true, "search": true, "execute": true, "readattr": true, "readextattr": true, "readsecurity": true, "inherited": true, "file_inherit": true, "directory_inherit": true, "limit_inherit": true, "only_inherit": true}
	for _, line := range strings.Split(text, "\n") {
		_, perms, ok := strings.Cut(line, " allow ")
		if !ok {
			continue
		}
		for _, perm := range strings.Split(strings.TrimSpace(perms), ",") {
			if !allowed[strings.TrimSpace(perm)] {
				return false
			}
		}
	}
	return true
}

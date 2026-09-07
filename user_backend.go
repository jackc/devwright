package devwright

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

type userRequest struct {
	Action              string
	Name                string
	Port                int
	PortSet             bool
	RemoveHome          bool
	Repository          string
	Installer           string
	Config              []byte
	ReplaceConfig       bool
	ClaudeConfig        []byte
	ReplaceClaudeConfig bool
	PublicKey           string
}

type userState struct {
	Version   int    `json:"version"`
	Name      string `json:"name"`
	Account   string `json:"account"`
	UID       int    `json:"uid"`
	GID       int    `json:"gid"`
	Owner     int    `json:"owner"`
	Home      string `json:"home"`
	Port      int    `json:"ssh_port"`
	Phase     string `json:"phase"`
	PublicKey string `json:"public_key"`
	GUID      string `json:"guid,omitempty"`
	Archive   string `json:"archive,omitempty"`
}
type userReply struct {
	State    *userState  `json:"state,omitempty"`
	States   []userState `json:"states,omitempty"`
	HostKeys []string    `json:"host_keys,omitempty"`
	Verifier string      `json:"verifier,omitempty"`
}

func userHome(name, goos string) string {
	if goos == "darwin" {
		return "/Users/devwright-" + name
	}
	return "/home/devwright-" + name
}

func requestForUser(o options) userRequest {
	return userRequest{Action: o.action, Name: o.name, Port: o.sshPort, PortSet: o.set["ssh-port"], RemoveHome: o.removeHome, Repository: o.dotfilesRepo, Installer: o.dotfilesInstall, Config: o.configPayload, ReplaceConfig: o.replaceCodexConfig, ClaudeConfig: o.claudeConfigPayload, ReplaceClaudeConfig: o.replaceClaudeConfig}
}

func runUserBackend(ctx context.Context, o options, stdin io.Reader, stdout, stderr io.Writer) error {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return errors.New("user backend supports Linux and macOS")
	}
	if o.action == "render" {
		configSource := "built-in editable defaults"
		if o.codexConfig != "" {
			configSource = o.codexConfig
		}
		claudeSource := "built-in editable defaults"
		if o.claudeConfig != "" {
			claudeSource = o.claudeConfig
		}
		return json.NewEncoder(stdout).Encode(map[string]any{
			"backend": "user", "account": "devwright-" + o.name, "group": "devwright-" + o.name,
			"home": userHome(o.name, runtime.GOOS), "home_mode": "0700",
			"ssh_host": "localhost", "ssh_port": o.sshPort, "ssh_service": "existing system OpenSSH",
			"codex_policy":  "editable user defaults; host requirements untouched",
			"claude_policy": "editable user defaults; host managed settings untouched",
			"provisioning": map[string]any{
				"run_as": "devwright-" + o.name, "codex": "latest stable release in ~/.local/bin", "claude": "latest release in ~/.local/bin",
				"config_source": configSource, "replace_config": o.replaceCodexConfig,
				"claude_config_source": claudeSource, "replace_claude_config": o.replaceClaudeConfig,
				"dotfiles_repository": o.dotfilesRepo, "dotfiles_installer": o.dotfilesInstall,
				"host_packages": "require existing prerequisites",
			},
		})
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if !filepath.IsAbs(home) || strings.ContainsAny(home, "\r\n\x00") {
		return errors.New("invalid operator home")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	call := func(req userRequest) (userReply, error) {
		var reply userReply
		payload, err := json.Marshal(req)
		if err != nil {
			return reply, err
		}
		args := []string{exe, "__user-helper"}
		if os.Geteuid() != 0 {
			args = append([]string{"/usr/bin/sudo", "--"}, args...)
		}
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + home, "TERM=" + os.Getenv("TERM")}
		cmd.Stdin = bytes.NewReader(payload)
		cmd.Stderr = stderr
		var output bytes.Buffer
		cmd.Stdout = &output
		if err := cmd.Run(); err != nil {
			return reply, fmt.Errorf("native account administration failed: %w (see diagnostic above)", err)
		}
		if err := json.Unmarshal(output.Bytes(), &reply); err != nil {
			return reply, fmt.Errorf("invalid administrative response: %w", err)
		}
		return reply, nil
	}
	req := requestForUser(o)
	dir := filepath.Join(home, ".ssh", "devwright", "user", o.name)
	key := filepath.Join(dir, "id_ed25519")
	if o.action == "create" {
		check := req
		check.Action = "create-check"
		if _, err := call(check); err != nil {
			return err
		}
		if _, err := os.Lstat(dir); err == nil {
			return fmt.Errorf("SSH identity already exists at %s; use configure for an incomplete account, or move stale identity aside", dir)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if runtime.GOOS == "darwin" {
			if err := exec.CommandContext(ctx, "/bin/chmod", "-N", dir).Run(); err != nil {
				return fmt.Errorf("make native SSH directory private: %w", err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "managed"), []byte("devwright-user-v1\n"), 0600); err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, "/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "devwright-user-"+o.name, "-f", key)
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			return err
		}
		pub, err := os.ReadFile(key + ".pub")
		if err != nil {
			return err
		}
		req.PublicKey = strings.TrimSpace(string(pub))
	}
	reply, err := call(req)
	if err != nil {
		return err
	}
	if o.action == "list" {
		for _, s := range reply.States {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\tSSH %d\n", s.Name, s.Account, s.Phase, s.Home, s.Port)
		}
		return nil
	}
	if o.action == "delete" {
		if err := removeUserClient(home, o.name); err != nil {
			return err
		}
		if reply.State.Archive != "" {
			fmt.Fprintln(stdout, "Home archived at", reply.State.Archive)
		}
		fmt.Fprintln(stdout, "Deleted managed account", reply.State.Account)
		return nil
	}
	if reply.State == nil {
		return errors.New("missing account identity in administrative response")
	}
	s := *reply.State
	// The privileged process never writes into the operator's home.
	if err := checkUserClient(dir, s.PublicKey); err != nil {
		return err
	}
	var known strings.Builder
	for _, k := range reply.HostKeys {
		fmt.Fprintf(&known, "user-%s %s\n", o.name, k)
	}
	knownPath := filepath.Join(dir, "known_hosts")
	old, err := optionalFile(knownPath)
	if err != nil {
		return err
	}
	if old != nil && string(old) != known.String() {
		return fmt.Errorf("system SSH host keys changed; inspect %s and move it aside after confirming the rotation", knownPath)
	}
	if old == nil {
		if err := atomicWrite(knownPath, []byte(known.String())); err != nil {
			return err
		}
	}
	config := userSSHConfig(s, dir)
	switch o.action {
	case "ssh-config":
		_, err := io.WriteString(stdout, config)
		return err
	case "install-ssh":
		v := vm{home: home, out: stdout}
		return v.installSSHText(o.name+".user.config", config, "SSH ready: ssh user-"+o.name)
	case "shell":
		args := append(userSSHArgs(s, dir), "-tt")
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Env = childEnvironment()
		cmd.Stdin = stdin
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		return cmd.Run()
	default:
		args := append(userSSHArgs(s, dir), shellJoin([]string{reply.Verifier, "native", s.Account, s.Home}))
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Env = childEnvironment()
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("native SSH verification failed: %w; check the system SSH service, access policy, and port %d", err, s.Port)
		}
		fmt.Fprintf(stdout, "Verified %s. Access: devwright shell %s --backend user\n", s.Account, s.Name)
		return nil
	}
}

func checkUserClient(dir, public string) error {
	for _, name := range []string{"", "managed", "id_ed25519", "id_ed25519.pub"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("missing local SSH identity; use the operator account that created this boundary: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || (name != "" && !info.Mode().IsRegular()) {
			return errors.New("unsafe native SSH identity path")
		}
		if int(info.Sys().(*syscall.Stat_t).Uid) != os.Getuid() {
			return errors.New("native SSH identity is not owned by the operator")
		}
		if (name == "" && info.Mode().Perm() != 0700) || (name == "id_ed25519" && info.Mode().Perm() != 0600) {
			return errors.New("native SSH directory/key must have mode 0700/0600")
		}
		if runtime.GOOS == "darwin" && (name == "" || name == "id_ed25519") {
			text, err := exec.Command("/bin/ls", "-lde", filepath.Join(dir, name)).Output()
			if err != nil {
				return err
			}
			if strings.Contains(string(text), " allow ") {
				return errors.New("native SSH directory/key has an access-granting ACL; remove it before continuing")
			}
		}
	}
	marker, err := os.ReadFile(filepath.Join(dir, "managed"))
	if err != nil || string(marker) != "devwright-user-v1\n" {
		return errors.New("unmanaged native SSH identity")
	}
	pub, err := os.ReadFile(filepath.Join(dir, "id_ed25519.pub"))
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(pub)) != public {
		return errors.New("local SSH identity differs from managed account")
	}
	return nil
}
func userSSHArgs(s userState, dir string) []string {
	return []string{"/usr/bin/ssh", "-F", os.DevNull, "-p", strconv.Itoa(s.Port), "-i", filepath.Join(dir, "id_ed25519"), "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none", "-o", "ForwardAgent=no", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "StrictHostKeyChecking=yes", "-o", "HostKeyAlias=user-" + s.Name, "-o", "UserKnownHostsFile=" + filepath.Join(dir, "known_hosts"), "-l", s.Account, "localhost"}
}
func userSSHConfig(s userState, dir string) string {
	quote := func(p string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(p) + `"` }
	return fmt.Sprintf("Host user-%s\n  HostName localhost\n  User %s\n  Port %d\n  IdentityFile %s\n  UserKnownHostsFile %s\n  HostKeyAlias user-%s\n  StrictHostKeyChecking yes\n  IdentitiesOnly yes\n  IdentityAgent none\n  ForwardAgent no\n  ControlMaster no\n  ControlPath none\n\nHost *\n", s.Name, s.Account, s.Port, quote(filepath.Join(dir, "id_ed25519")), quote(filepath.Join(dir, "known_hosts")), s.Name)
}
func removeUserClient(home, name string) error {
	dir := filepath.Join(home, ".ssh", "devwright", "user", name)
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() {
			return errors.New("refusing non-directory client identity")
		}
		data, err := os.ReadFile(filepath.Join(dir, "managed"))
		if err != nil || string(data) != "devwright-user-v1\n" {
			return errors.New("refusing to remove unmanaged client identity")
		}
		for _, file := range []string{"managed", "id_ed25519", "id_ed25519.pub", "known_hosts"} {
			if err := os.Remove(filepath.Join(dir, file)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := os.Remove(dir); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	config := filepath.Join(home, ".ssh", "devwright", name+".user.config")
	if info, err := os.Lstat(config); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("refusing non-regular SSH configuration")
		}
		data, err := os.ReadFile(config)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(string(data), sshHeader+"Host user-"+name+"\n") {
			return errors.New("refusing to remove unrelated SSH configuration")
		}
		return os.Remove(config)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

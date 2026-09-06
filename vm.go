package agentvm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

type instanceConfig struct {
	Plain     bool              `json:"plain"`
	Mounts    []json.RawMessage `json:"mounts"`
	Provision []json.RawMessage `json:"provision"`
	SSH       struct {
		ForwardAgent bool `json:"forwardAgent"`
	} `json:"ssh"`
	User struct {
		Name string `json:"name"`
	} `json:"user"`
}

type instance struct {
	Backend string         `json:"-"`
	Name    string         `json:"name"`
	Dir     string         `json:"dir"`
	Status  string         `json:"status"`
	Config  instanceConfig `json:"config"`
}

type vm struct {
	options
	home        string
	ctx         context.Context
	out         io.Writer
	run         runner
	withContext func(context.Context) runner
	readToken   func() (string, error)
}

func (v *vm) render() ([]byte, error) {
	if v.backend == "incus" {
		return v.renderIncus()
	}
	data, err := recipe.ReadFile("lima/agent.json")
	if err != nil {
		return nil, err
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	config["provision"] = []any{}
	if v.cpus > 0 {
		config["cpus"] = v.cpus
	}
	if v.memory != "" {
		config["memory"] = v.memory
	}
	if v.disk != "" {
		config["disk"] = v.disk
	}
	data, err = json.MarshalIndent(config, "", "  ")
	return append(data, '\n'), err
}

func (v *vm) provision() (string, error) {
	data, err := recipe.ReadFile("lima/provision.sh")
	if err != nil {
		return "", err
	}
	script := string(data)
	for key, path := range map[string]string{
		"REQUIREMENTS": "config/codex/requirements.toml", "CONFIG": "config/codex/config.toml",
		"DOTFILES": "lima/dotfiles.sh", "VERIFY_ARM64": "guestbin/verify-linux-arm64.gz", "VERIFY_AMD64": "guestbin/verify-linux-amd64.gz",
	} {
		payload, err := recipe.ReadFile(path)
		if err != nil {
			return "", err
		}
		script = strings.ReplaceAll(script, "__"+key+"_B64__", base64.StdEncoding.EncodeToString(payload))
	}
	return "dotfiles_repository=" + shellQuote(v.dotfilesRepo) + "\ndotfiles_install=" + shellQuote(v.dotfilesInstall) + "\n" + script, nil
}

func (v *vm) info() (instance, error) {
	if v.backend == "incus" {
		return v.incusInfo()
	}
	output, err := v.run([]string{"limactl", "list", "--json", v.name}, nil, true)
	if err != nil {
		return instance{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	var state instance
	if err := decoder.Decode(&state); err != nil {
		return state, fmt.Errorf("expected exactly one VM named %s in Lima output", v.name)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF || state.Name != v.name {
		return state, fmt.Errorf("expected exactly one VM named %s in Lima output", v.name)
	}
	return state, checkInstance(state)
}

func checkInstance(state instance) error {
	cfg := state.Config
	if !cfg.Plain || len(cfg.Mounts) != 0 || cfg.SSH.ForwardAgent {
		return errors.New("unexpected mounts, forwarding, or non-plain mode; inspect Lima overrides")
	}
	if len(cfg.Provision) != 0 {
		return errors.New("boot provisioning is unsupported; create a fresh VM with this recipe")
	}
	if cfg.User.Name != "dev" {
		return errors.New("unexpected primary account")
	}
	if !validName.MatchString(state.Name) || !filepath.IsAbs(state.Dir) || strings.ContainsAny(state.Dir, "\r\n\x00") {
		return errors.New("invalid VM name or directory in Lima output")
	}
	return nil
}

func sshArgs(state instance, user string) []string {
	if state.Backend == "incus" {
		return incusSSHArgs(state, user)
	}
	return []string{"ssh", "-F", filepath.Join(state.Dir, "ssh.config"),
		"-o", "IdentityAgent=none", "-o", "ForwardAgent=no", "-o", "ControlPath=~/.ssh/control-%C",
		"-o", "ControlMaster=auto", "-o", "ControlPersist=60", "-l", user, "lima-" + state.Name}
}

func (v *vm) remote(state instance, command []string, user string, input io.Reader) error {
	_, err := v.run(append(sshArgs(state, user), shellJoin(command)), input, false)
	return err
}

func (v *vm) bootstrap(state instance) error {
	if state.Backend == "incus" {
		return v.bootstrapIncus(state)
	}
	script, err := recipe.ReadFile("lima/bootstrap.sh")
	if err != nil {
		return err
	}
	if err := v.remote(state, []string{"sudo", "-n", "/bin/bash", "-s"}, "dev", strings.NewReader(string(script))); err != nil {
		return err
	}
	// Establish root administration before provisioning removes dev's sudo.
	return v.remote(state, []string{"test", "-s", "/root/.ssh/authorized_keys"}, "root", nil)
}

func (v *vm) verify(state instance) error {
	return v.remote(state, []string{"/usr/local/share/agent-vm/verify"}, "dev", nil)
}

func (v *vm) configure(state instance) error {
	script, err := v.provision()
	if err != nil {
		return err
	}
	if err := v.remote(state, []string{"/bin/bash", "-s"}, "root", strings.NewReader(script)); err != nil {
		return err
	}
	return v.verify(state)
}

func (v *vm) execute() error {
	if v.action == "render" {
		data, err := v.render()
		if err != nil {
			return err
		}
		_, err = v.out.Write(data)
		return err
	}
	if v.action == "create" && v.backend == "incus" {
		if err := v.createIncus(); err != nil {
			return err
		}
	}
	if v.action == "create" && v.backend != "incus" {
		data, err := v.render()
		if err != nil {
			return err
		}
		// Each invocation gets its own file; installed binaries need no writable checkout.
		dir, err := os.MkdirTemp("", "agent-vm-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		template := filepath.Join(dir, "agent.yaml")
		if err := os.WriteFile(template, data, 0o600); err != nil {
			return err
		}
		if _, err := v.run([]string{"limactl", "validate", template}, nil, false); err != nil {
			return err
		}
		// Lima refuses existing names; never adopt or overwrite another VM.
		if _, err := v.run([]string{"limactl", "create", "--tty=false", "--name=" + v.name, template}, nil, false); err != nil {
			return err
		}
	}
	state, err := v.info()
	if err != nil {
		return err
	}
	if v.action == "create" || (v.action == "configure" && state.Status != "Running") {
		args := []string{"limactl", "start", "--tty=false", v.name}
		if v.backend == "incus" {
			args = incusArgs("start", v.name)
		}
		if _, err := v.run(args, nil, false); err != nil {
			return err
		}
		state, err = v.info()
		if err != nil {
			return err
		}
	}
	if state.Status != "Running" {
		manager := "limactl"
		if v.backend == "incus" {
			manager = "incus --force-local --project default"
		}
		return fmt.Errorf("instance is not running; start it first with %s start %s", manager, v.name)
	}
	if v.backend == "incus" && v.action != "ssh-config" && v.action != "install-ssh" {
		if err := v.waitIncus(); err != nil {
			return err
		}
	}
	switch v.action {
	case "create":
		if err := v.bootstrap(state); err != nil {
			return err
		}
		// Recheck resolved configuration before every provisioning operation.
		state, err = v.info()
		if err != nil {
			return err
		}
		if err := v.configure(state); err != nil {
			return err
		}
		fmt.Fprintf(v.out, "Created and verified. Set up SSH: agent-vm install-ssh %s --backend %s\n", v.name, v.backend)
	case "configure":
		return v.configure(state)
	case "verify":
		return v.verify(state)
	case "ssh-config":
		_, err = io.WriteString(v.out, sshConfig(state))
		return err
	case "install-ssh":
		return v.installSSH(state)
	case "set-token":
		token, err := v.readToken()
		if err != nil {
			return err
		}
		if token == "" || strings.IndexFunc(token, unicode.IsSpace) >= 0 || strings.ContainsRune(token, 0) {
			return errors.New("expected a nonempty token without whitespace")
		}
		// Token travels only on SSH stdin, never in a process argument or recipe.
		command := []string{"/bin/sh", "-c", "umask 077; mkdir -p ~/.config/agent-vm; cat > ~/.config/agent-vm/github-token; chmod 600 ~/.config/agent-vm/github-token"}
		if err := v.remote(state, command, "dev", strings.NewReader(token+"\n")); err != nil {
			return err
		}
		fmt.Fprintln(v.out, "Token saved for dev. Verify scope against allowed and disallowed PRIVATE repositories.")
	}
	return nil
}

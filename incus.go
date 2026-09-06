package agentvm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var validIncusResource = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

const incusMarker = "agent-sandbox-config-v1"
const incusImage = "images:ubuntu/26.04"

// Always address the local server and default project, independently of the
// user's currently selected remote/project. SSH's proxy uses the same scope.
func incusArgs(args ...string) []string {
	if len(args) == 2 && args[0] == "query" {
		// query accepts the project in the URL, not the global --project flag.
		return []string{"incus", "--force-local", "query", args[1] + "?project=default"}
	}
	return append([]string{"incus", "--force-local", "--project", "default"}, args...)
}

func incusPreflight(run runner, lookPath func(string) (string, error), goos string) error {
	if goos != "linux" {
		return errors.New("the Incus backend requires a Linux host; run agent-vm inside your Linux VM")
	}
	for _, name := range []string{"incus", "ssh", "ssh-keygen"} {
		if _, err := lookPath(name); err != nil {
			return fmt.Errorf("%s is required for the Incus backend; install Incus and OpenSSH on the Linux host", name)
		}
	}
	if _, err := run(incusArgs("query", "/1.0"), nil, true); err != nil {
		return fmt.Errorf("cannot access the local Incus server; initialize it with incus admin init and grant your host account access: %w", err)
	}
	_, err := run([]string{"ssh", "-G", "-T", "-F", os.DevNull, "-o", "IdentityAgent=none", "-o", "ForwardAgent=no",
		"-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ProxyCommand=incus --version",
		"-o", "ControlPath=~/.ssh/control-%C", "-o", "ControlMaster=auto", "-o", "ControlPersist=60", "incus-check"}, nil, true)
	return err
}

type incusInstance struct {
	Name            string                       `json:"name"`
	Status          string                       `json:"status"`
	Type            string                       `json:"type"`
	Profiles        []string                     `json:"profiles"`
	Config          map[string]string            `json:"config"`
	Devices         map[string]map[string]string `json:"devices"`
	ExpandedConfig  map[string]string            `json:"expanded_config,omitempty"`
	ExpandedDevices map[string]map[string]string `json:"expanded_devices,omitempty"`
}

func (v *vm) renderIncus() ([]byte, error) {
	cpus, memory, disk := v.cpus, v.memory, v.disk
	if cpus == 0 {
		cpus = 4
	}
	if memory == "" {
		memory = "4GiB"
	}
	if disk == "" {
		disk = "60GiB"
	}
	config := map[string]string{
		"user.agent-vm": incusMarker, "limits.cpu": strconv.Itoa(cpus), "limits.memory": memory,
	}
	if v.container {
		// Bubblewrap needs nested namespaces. Keep the outer container
		// unprivileged with a separate host UID/GID range.
		config["security.privileged"] = "false"
		config["security.idmap.isolated"] = "true"
		config["security.nesting"] = "true"
	}
	data, err := json.MarshalIndent(struct {
		Profiles []string                     `json:"profiles"`
		Config   map[string]string            `json:"config"`
		Devices  map[string]map[string]string `json:"devices"`
	}{
		Profiles: []string{}, Config: config,
		Devices: map[string]map[string]string{
			"root": {"type": "disk", "path": "/", "pool": v.storage, "size": disk},
			"eth0": {"type": "nic", "name": "eth0", "network": v.network},
		},
	}, "", "  ")
	return append(data, '\n'), err
}

func checkIncusInstance(s incusInstance) error {
	if !validName.MatchString(s.Name) || (s.Type != "container" && s.Type != "virtual-machine") {
		return errors.New("invalid Incus instance name or type")
	}
	if len(s.Profiles) != 0 || s.ExpandedConfig["user.agent-vm"] != incusMarker {
		return errors.New("unmanaged Incus instance or inherited profiles; create a fresh instance with this recipe")
	}
	for key, value := range s.ExpandedConfig {
		switch {
		case key == "user.agent-vm", key == "limits.cpu", key == "limits.memory", key == "boot.autostart":
		case strings.HasPrefix(key, "image."), strings.HasPrefix(key, "volatile."):
		case s.Type == "container" && key == "security.privileged" && value == "false":
		case s.Type == "container" && (key == "security.nesting" || key == "security.idmap.isolated") && value == "true":
		default:
			return fmt.Errorf("unsupported Incus setting %s; remove overrides before operating on this instance", key)
		}
	}
	if s.Type == "container" && (s.ExpandedConfig["security.privileged"] != "false" ||
		s.ExpandedConfig["security.idmap.isolated"] != "true" || s.ExpandedConfig["security.nesting"] != "true") {
		return errors.New("Incus containers must be unprivileged with isolated IDs and nested namespaces")
	}
	root, nic := s.ExpandedDevices["root"], s.ExpandedDevices["eth0"]
	if len(s.ExpandedDevices) != 2 || len(root) != 4 || root["type"] != "disk" || root["path"] != "/" ||
		!validIncusResource.MatchString(root["pool"]) || !validSize.MatchString(root["size"]) ||
		len(nic) != 3 || nic["type"] != "nic" || nic["name"] != "eth0" || !validIncusResource.MatchString(nic["network"]) {
		return errors.New("unexpected Incus devices; only a pool-backed root disk and managed network are supported (no host mounts or sockets)")
	}
	return nil
}

func (v *vm) incusDir() string {
	return filepath.Join(v.home, ".ssh", "agent-vms", "incus", v.name)
}

func (v *vm) incusInfo() (instance, error) {
	output, err := v.run(incusArgs("query", "/1.0/instances/"+v.name), nil, true)
	if err != nil {
		return instance{}, err
	}
	var state incusInstance
	if err := json.Unmarshal([]byte(output), &state); err != nil {
		return instance{}, fmt.Errorf("invalid Incus instance response: %w", err)
	}
	if state.Name != v.name {
		return instance{}, errors.New("Incus returned a different instance")
	}
	if err := checkIncusInstance(state); err != nil {
		return instance{}, err
	}
	if !filepath.IsAbs(v.home) || strings.ContainsAny(v.home, "\r\n\x00") {
		return instance{}, errors.New("invalid host home directory")
	}
	return instance{Backend: "incus", Name: state.Name, Dir: v.incusDir(), Status: state.Status}, nil
}

func (v *vm) createIncus() error {
	// Never reuse a key belonging to a deleted instance with the same name.
	if _, err := os.Lstat(v.incusDir()); err == nil {
		return fmt.Errorf("Incus SSH state already exists at %s; move it aside before recreating this name", v.incusDir())
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := v.renderIncus()
	if err != nil {
		return err
	}
	args := incusArgs("init", incusImage, v.name, "--no-profiles")
	if !v.container {
		args = append(args, "--vm")
	}
	// init refuses existing names. No user boot scripts or host profiles are used.
	_, err = v.run(args, strings.NewReader(string(data)), false)
	return err
}

func (v *vm) waitIncus() error {
	ctx := v.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	run := v.run
	if v.withContext != nil {
		run = v.withContext(ctx)
	}
	fmt.Fprintln(v.out, "Waiting for Incus guest readiness...")
	for {
		output, err := run(incusArgs("query", "/1.0/instances/"+v.name+"/state"), nil, true)
		if err != nil {
			return err
		}
		var state struct {
			Status    string `json:"status"`
			Processes *int   `json:"processes"`
		}
		if err := json.Unmarshal([]byte(output), &state); err != nil || state.Processes == nil {
			return errors.New("invalid Incus readiness response")
		}
		// VM process counts come from incus-agent; Incus returns -1 until
		// the agent is available. Containers report their processes directly.
		if state.Status == "Running" && *state.Processes > 0 {
			// A process count alone does not mean networking or SSH has started.
			// A degraded boot can still provide SSH (e.g. an optional image unit
			// failed); the subsequent SSH and guest acceptance checks are decisive.
			_, err := run(incusArgs("exec", v.name, "--mode=non-interactive", "--", "timeout", "180", "/bin/sh", "-c",
				`while [ ! -S /run/systemd/private ]; do sleep 1; done; state=$(systemctl is-system-running --wait); [ "$state" = running ] || [ "$state" = degraded ]`), nil, true)
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for Incus guest execution (VMs require incus-agent): %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func (v *vm) bootstrapIncus(state instance) error {
	if err := os.MkdirAll(filepath.Dir(state.Dir), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(state.Dir, 0o700); err != nil {
		return err
	}
	key := filepath.Join(state.Dir, "identity")
	if _, err := v.run([]string{"ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "agent-vm-incus-" + state.Name, "-f", key}, nil, true); err != nil {
		return err
	}
	public, err := os.ReadFile(key + ".pub")
	if err != nil {
		return err
	}
	script, err := recipe.ReadFile("incus/bootstrap.sh")
	if err != nil {
		return err
	}
	input := "public_key_b64=" + shellQuote(base64.StdEncoding.EncodeToString(public)) + "\n" + string(script)
	if _, err := v.run(incusArgs("exec", state.Name, "--mode=non-interactive", "--", "/bin/bash", "-s"), strings.NewReader(input), false); err != nil {
		return err
	}
	// Trust the host key delivered by the authenticated local Incus control plane,
	// then require that key on every SSH connection. No trust-on-first-use prompt.
	publicHost, err := v.run(incusArgs("exec", state.Name, "--mode=non-interactive", "--", "cat", "/etc/ssh/ssh_host_ed25519_key.pub"), nil, true)
	if err != nil {
		return err
	}
	fields := strings.Fields(publicHost)
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return errors.New("Incus guest returned an invalid SSH host key")
	}
	if _, err := base64.StdEncoding.DecodeString(fields[1]); err != nil {
		return errors.New("Incus guest returned an invalid SSH host key")
	}
	known := "incus-" + state.Name + " " + fields[0] + " " + fields[1] + "\n"
	if err := atomicWrite(filepath.Join(state.Dir, "known_hosts"), []byte(known)); err != nil {
		return err
	}
	return v.remote(state, []string{"test", "-s", "/root/.ssh/authorized_keys"}, "root", nil)
}

func incusProxy(state instance) string {
	return shellJoin(incusArgs("exec", state.Name, "--mode=non-interactive", "--", "/usr/bin/nc", "127.0.0.1", "22"))
}

func incusSSHArgs(state instance, user string) []string {
	return []string{"ssh", "-F", os.DevNull, "-o", "IdentityAgent=none", "-o", "ForwardAgent=no",
		"-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + sshPath(filepath.Join(state.Dir, "known_hosts")), "-o", "GlobalKnownHostsFile=" + os.DevNull,
		"-o", "ProxyCommand=" + incusProxy(state), "-i", strings.ReplaceAll(filepath.Join(state.Dir, "identity"), "%", "%%"),
		"-o", "ControlPath=~/.ssh/control-%C", "-o", "ControlMaster=auto", "-o", "ControlPersist=60",
		"-l", user, "incus-" + state.Name}
}

func sshPath(path string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(path) + `"`
}

func incusSSHConfig(state instance) string {
	return fmt.Sprintf(`Host incus-%s
  HostName incus-%s
  User dev
  IdentityAgent none
  ForwardAgent no
  IdentitiesOnly yes
  IdentityFile %s
  StrictHostKeyChecking yes
  UserKnownHostsFile %s
  GlobalKnownHostsFile %s
  ProxyCommand %s
  ControlMaster auto
  ControlPath ~/.ssh/control-%%C
  ControlPersist 60

Host *
`, state.Name, state.Name, sshPath(filepath.Join(state.Dir, "identity")), sshPath(filepath.Join(state.Dir, "known_hosts")), os.DevNull, incusProxy(state))
}

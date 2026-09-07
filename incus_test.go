package devwright

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func incusTestVM(t *testing.T, action string, container bool) (*vm, *incusInstance) {
	t.Helper()
	v := testVM(t, action)
	v.backend, v.container = "incus", container
	data, err := v.render()
	if err != nil {
		t.Fatal(err)
	}
	s := &incusInstance{Name: v.name, Status: "Stopped", Type: "virtual-machine"}
	if container {
		s.Type = "container"
	}
	if err := json.Unmarshal(data, s); err != nil {
		t.Fatal(err)
	}
	s.ExpandedConfig, s.ExpandedDevices = s.Config, s.Devices
	return v, s
}

func incusRecorder(t *testing.T, state *incusInstance, calls *[]recordedCommand) runner {
	t.Helper()
	return func(args []string, input io.Reader, capture bool) (string, error) {
		var data []byte
		if input != nil {
			var err error
			data, err = io.ReadAll(input)
			if err != nil {
				t.Fatal(err)
			}
		}
		*calls = append(*calls, recordedCommand{args: args, input: string(data)})
		if args[0] == "ssh-keygen" {
			output, err := exec.Command(args[0], args[1:]...).CombinedOutput()
			return string(output), err
		}
		if reflect.DeepEqual(args, incusArgs("query", "/1.0/instances/test")) {
			data, _ := json.Marshal(state)
			return string(data), nil
		}
		if reflect.DeepEqual(args, incusArgs("start", "test")) {
			state.Status = "Running"
		}
		if reflect.DeepEqual(args, incusArgs("query", "/1.0/instances/test/state")) {
			return `{"status":"Running","processes":20}`, nil
		}
		if args[len(args)-1] == "/etc/ssh/ssh_host_ed25519_key.pub" {
			return "ssh-ed25519 c3ludGhldGlj host\n", nil
		}
		return "", nil
	}
}

func TestIncusOptionsAndOfflineRendering(t *testing.T) {
	for _, args := range [][]string{
		{"render", "--backend", "unknown"}, {"create", "--container"},
		{"configure", "--backend", "incus", "--container"}, {"verify", "--backend", "incus", "--storage", "default"},
		{"create", "--backend", "incus", "--network", "../bad"}, {"create", "--backend", "incus", "--storage", ""},
	} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("accepted options: %v", args)
		}
	}
	t.Setenv("PATH", t.TempDir())
	for _, container := range []bool{false, true} {
		args := []string{"--backend", "incus", "render", "--cpus", "8", "--memory", "8GiB", "--disk", "100GiB", "--network", "custom-net", "--storage", "pool"}
		if container {
			args = append(args, "--container")
		}
		var output bytes.Buffer
		if err := Run(context.Background(), args, "test", nil, &output, io.Discard); err != nil {
			t.Fatal(err)
		}
		var s incusInstance
		if err := json.Unmarshal(output.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		if s.Config["limits.cpu"] != "8" || s.Config["limits.memory"] != "8GiB" || s.Devices["root"]["size"] != "100GiB" ||
			s.Devices["root"]["pool"] != "pool" || s.Devices["eth0"]["network"] != "custom-net" || s.Profiles == nil || len(s.Profiles) != 0 {
			t.Fatalf("unexpected recipe: %s", output.String())
		}
		if container != (s.Config["security.nesting"] == "true") {
			t.Fatal("wrong instance type settings")
		}
	}
}

func TestIncusPreflightDoesNotRequireLima(t *testing.T) {
	var found []string
	look := func(s string) (string, error) { found = append(found, s); return s, nil }
	run := func(args []string, _ io.Reader, _ bool) (string, error) {
		if args[0] != "ssh" && !reflect.DeepEqual(args, []string{"incus", "--force-local", "query", "/1.0?project=default"}) {
			t.Fatalf("unexpected dependency: %v", args)
		}
		return "", nil
	}
	if err := incusPreflight(run, look, "linux"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(found, []string{"incus", "ssh", "ssh-keygen"}) {
		t.Fatalf("dependencies: %v", found)
	}
	if err := incusPreflight(nil, nil, "darwin"); err == nil {
		t.Fatal("accepted non-Linux Incus host")
	}
	if err := incusPreflight(run, func(string) (string, error) { return "", exec.ErrNotFound }, "linux"); err == nil {
		t.Fatal("accepted missing tools")
	}
	if err := incusPreflight(func([]string, io.Reader, bool) (string, error) { return "", errors.New("permission denied") }, look, "linux"); err == nil {
		t.Fatal("accepted unreachable daemon")
	}
}

func TestIncusRejectsUnsafeResolvedConfiguration(t *testing.T) {
	for name, change := range map[string]func(*incusInstance){
		"unmanaged":         func(s *incusInstance) { delete(s.ExpandedConfig, "user.devwright") },
		"profiles":          func(s *incusInstance) { s.Profiles = []string{"default"} },
		"privileged":        func(s *incusInstance) { s.ExpandedConfig["security.privileged"] = "true" },
		"idmap":             func(s *incusInstance) { delete(s.ExpandedConfig, "security.idmap.isolated") },
		"raw":               func(s *incusInstance) { s.ExpandedConfig["raw.lxc"] = "lxc.apparmor.profile=unconfined" },
		"cloud-init":        func(s *incusInstance) { s.ExpandedConfig["cloud-init.user-data"] = "boot commands" },
		"legacy-cloud-init": func(s *incusInstance) { s.ExpandedConfig["user.user-data"] = "boot commands" },
		"environment":       func(s *incusInstance) { s.ExpandedConfig["environment.SSH_AUTH_SOCK"] = "/host/agent" },
		"host-mount": func(s *incusInstance) {
			s.ExpandedDevices["home"] = map[string]string{"type": "disk", "source": "/home", "path": "/mnt"}
		},
		"root-source":      func(s *incusInstance) { s.ExpandedDevices["root"]["source"] = "/" },
		"socket":           func(s *incusInstance) { s.ExpandedDevices["agent"] = map[string]string{"type": "proxy"} },
		"nic":              func(s *incusInstance) { s.ExpandedDevices["eth0"]["nictype"] = "physical" },
		"type":             func(s *incusInstance) { s.Type = "unknown" },
		"name":             func(s *incusInstance) { s.Name = "other" },
		"missing-expanded": func(s *incusInstance) { s.ExpandedConfig = nil },
	} {
		t.Run(name, func(t *testing.T) {
			v, s := incusTestVM(t, "configure", true)
			change(s)
			data, _ := json.Marshal(s)
			v.run = func(args []string, _ io.Reader, _ bool) (string, error) {
				if !reflect.DeepEqual(args, incusArgs("query", "/1.0/instances/test")) {
					t.Fatalf("unsafe instance reached: %v", args)
				}
				return string(data), nil
			}
			if err := v.execute(); err == nil {
				t.Fatal("accepted unsafe instance")
			}
		})
	}
}

func TestIncusCreationAndSSHBootstrap(t *testing.T) {
	for _, container := range []bool{false, true} {
		v, s := incusTestVM(t, "create", container)
		var calls []recordedCommand
		v.run = incusRecorder(t, s, &calls)
		if err := v.execute(); err != nil {
			t.Fatal(err)
		}
		want, _ := v.render()
		if !slices.Contains(calls[0].args, "--no-profiles") || slices.Contains(calls[0].args, "--vm") == container || calls[0].input != string(want) {
			t.Fatalf("wrong create arguments: %v", calls[0].args)
		}
		var sshUsers []string
		for _, call := range calls {
			if call.args[0] == "ssh" {
				sshUsers = append(sshUsers, call.args[slices.Index(call.args, "-l")+1])
			}
		}
		if !reflect.DeepEqual(sshUsers, []string{"root", "root", "dev"}) {
			t.Fatalf("bootstrap, provisioning, verification users: %v", sshUsers)
		}
		for _, file := range []string{"identity", "known_hosts"} {
			info, err := os.Stat(filepath.Join(v.incusDir(), file))
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("SSH state permissions: %s %v", file, err)
			}
		}
		if err := v.createIncus(); err == nil {
			t.Fatal("reused an existing SSH identity")
		}
	}
}

func TestIncusFailuresStopCreation(t *testing.T) {
	// Exercise every subprocess boundary, including init, boot, key generation,
	// root access, provisioning, and verification. Never continue after failure.
	baseline, state := incusTestVM(t, "create", false)
	var calls []recordedCommand
	baseline.run = incusRecorder(t, state, &calls)
	if err := baseline.execute(); err != nil {
		t.Fatal(err)
	}
	for failAt := range calls {
		v, s := incusTestVM(t, "create", false)
		ctx, cancel := context.WithCancel(context.Background())
		v.ctx = ctx
		var recorded []recordedCommand
		run := incusRecorder(t, s, &recorded)
		count := 0
		v.run = func(args []string, input io.Reader, capture bool) (string, error) {
			if count > failAt {
				t.Fatalf("continued after command %d failed", failAt)
			}
			count++
			if count-1 == failAt {
				cancel() // also abort the deliberate guest readiness retry
				return "", errors.New("synthetic failure")
			}
			return run(args, input, capture)
		}
		if err := v.execute(); err == nil {
			t.Fatalf("ignored failure at command %d", failAt)
		}
		cancel()
	}
}

func TestIncusConfigureAndTokenTransport(t *testing.T) {
	v, s := incusTestVM(t, "configure", true)
	var calls []recordedCommand
	v.run = incusRecorder(t, s, &calls)
	if err := v.execute(); err != nil {
		t.Fatal(err)
	}
	bootWaited := false
	for _, c := range calls {
		if c.args[0] == "incus" && strings.Contains(c.args[len(c.args)-1], "systemctl is-system-running --wait") {
			bootWaited = true
		}
		if c.args[0] == "ssh" && !bootWaited {
			t.Fatal("SSH attempted before guest boot completed")
		}
		if c.args[0] == "ssh-keygen" || strings.Contains(c.input, "public_key_b64=") {
			t.Fatal("configure replaced SSH keys")
		}
	}

}

func TestIncusRechecksAfterStart(t *testing.T) {
	v, s := incusTestVM(t, "configure", false)
	var calls []recordedCommand
	run := incusRecorder(t, s, &calls)
	v.run = func(args []string, input io.Reader, capture bool) (string, error) {
		if args[0] == "ssh" || slices.Contains(args, "exec") {
			t.Fatal("unsafe instance reached guest execution")
		}
		if slices.Contains(args, "start") {
			s.ExpandedConfig["raw.qemu"] = "unsafe override"
		}
		return run(args, input, capture)
	}
	if err := v.execute(); err == nil {
		t.Fatal("configuration not rechecked after start")
	}
}

func TestIncusReadinessRequiresAgentAndHonorsCancellation(t *testing.T) {
	for _, output := range []string{`{"status":"Running","processes":-1}`, `{"status":"Stopped","processes":0}`, `{}`, `not json`} {
		v, _ := incusTestVM(t, "configure", false)
		ctx, cancel := context.WithCancel(context.Background())
		v.ctx = ctx
		v.run = func(args []string, _ io.Reader, _ bool) (string, error) {
			if !reflect.DeepEqual(args, incusArgs("query", "/1.0/instances/test/state")) {
				t.Fatalf("unexpected readiness probe: %v", args)
			}
			cancel()
			return output, nil
		}
		if err := v.waitIncus(); err == nil {
			t.Fatalf("accepted an unready guest: %s", output)
		}
		cancel()
	}
}

func TestIncusSSHConfigurationAndLimaCoexist(t *testing.T) {
	v, s := incusTestVM(t, "install-ssh", false)
	s.Status = "Running"
	var calls []recordedCommand
	v.run = incusRecorder(t, s, &calls)
	state, err := v.info()
	if err != nil {
		t.Fatal(err)
	}
	limaPath := filepath.Join(v.home, ".ssh", "devwright", "incus-test.config")
	writeTestFile(t, limaPath, "personal Lima entry")
	if err := v.installSSH(state); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(limaPath)
	if string(data) != "personal Lima entry" {
		t.Fatal("overwrote Lima SSH entry")
	}
	wrapper := filepath.Join(v.home, "a path", "wrapper")
	state.Dir = filepath.Join(v.home, "a path")
	writeTestFile(t, wrapper, sshConfig(state))
	sockets := map[string]bool{}
	for _, user := range []string{"dev", "root"} {
		for _, args := range [][]string{
			append([]string{"-G", "-T"}, incusSSHArgs(state, user)[1:]...),
			{"-G", "-T", "-F", wrapper, user + "@incus-test"},
		} {
			output, err := exec.Command("ssh", args...).Output()
			if err != nil {
				t.Fatal(err)
			}
			values := map[string]string{}
			for _, line := range strings.Split(string(output), "\n") {
				key, value, _ := strings.Cut(line, " ")
				values[key] = value
			}
			for key, want := range map[string]string{"user": user, "hostname": "incus-test", "identityagent": "none", "forwardagent": "no", "identitiesonly": "yes", "stricthostkeychecking": "true"} {
				if values[key] != want {
					t.Fatalf("%s: got %q, want %q", key, values[key], want)
				}
			}
			if !strings.Contains(values["proxycommand"], "'--force-local' '--project' 'default'") || !strings.Contains(values["userknownhostsfile"], "a path/known_hosts") {
				t.Fatalf("SSH transport settings: %s", output)
			}
			sockets[values["controlpath"]] = true
		}
	}
	if len(sockets) != 2 {
		t.Fatal("root and dev share a control socket or generated settings differ")
	}
}

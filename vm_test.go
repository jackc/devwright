package agentvm

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testVM(t *testing.T, action string) *vm {
	t.Helper()
	o, err := parseOptions([]string{action, "test"})
	if err != nil {
		t.Fatal(err)
	}
	return &vm{options: o, home: t.TempDir(), out: io.Discard}
}

func testState(t *testing.T, v *vm) instance {
	t.Helper()
	data, err := v.render()
	if err != nil {
		t.Fatal(err)
	}
	state := instance{Name: "test", Dir: "/tmp/a path", Status: "Running"}
	if err := json.Unmarshal(data, &state.Config); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestEmbeddedRecipe(t *testing.T) {
	v := testVM(t, "render")
	data, err := v.render()
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config["cpus"] != float64(4) || config["memory"] != "4GiB" || config["disk"] != "60GiB" || config["plain"] != true {
		t.Fatalf("unexpected default recipe: %s", data)
	}
	if len(config["provision"].([]any)) != 0 {
		t.Fatal("boot provisioning present")
	}
	v.cpus, v.memory, v.disk = 8, "8GiB", "100GiB"
	data, _ = v.render()
	json.Unmarshal(data, &config)
	if config["cpus"] != float64(8) || config["memory"] != "8GiB" || config["disk"] != "100GiB" {
		t.Fatalf("resource overrides: %s", data)
	}
	v.dotfilesRepo, v.dotfilesInstall = "https://example.com/a'$(false).git", "setup script"
	script, err := v.provision()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, "_B64__") {
		t.Fatal("unresolved payload")
	}
	for _, path := range []string{"config/codex/requirements.toml", "config/codex/config.toml", "lima/dotfiles.sh", "guestbin/verify-linux-arm64.gz", "guestbin/verify-linux-amd64.gz"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(script, base64.StdEncoding.EncodeToString(data)) {
			t.Fatalf("payload changed: %s", path)
		}
	}
	assignments := strings.Join(strings.Split(script, "\n")[:2], "\n")
	output, err := exec.Command("/bin/bash", "-c", assignments+"\nprintf '%s\\n' \"$dotfiles_repository\" \"$dotfiles_install\"").Output()
	if err != nil || string(output) != v.dotfilesRepo+"\n"+v.dotfilesInstall+"\n" {
		t.Fatalf("dotfiles quoting: %s %v", output, err)
	}
}

func TestUnsafeInstances(t *testing.T) {
	v := testVM(t, "verify")
	for name, change := range map[string]func(*instance){
		"plain":  func(s *instance) { s.Config.Plain = false },
		"mounts": func(s *instance) { s.Config.Mounts = []json.RawMessage{json.RawMessage(`{}`)} },
		"agent":  func(s *instance) { s.Config.SSH.ForwardAgent = true },
		"boot":   func(s *instance) { s.Config.Provision = []json.RawMessage{json.RawMessage(`{}`)} },
		"user":   func(s *instance) { s.Config.User.Name = "root" },
		"name":   func(s *instance) { s.Name = "../../personal" },
		"path":   func(s *instance) { s.Dir = "/tmp/bad\nHost *" },
	} {
		t.Run(name, func(t *testing.T) {
			state := testState(t, v)
			change(&state)
			data, _ := json.Marshal(state)
			v.run = func(args []string, _ io.Reader, _ bool) (string, error) {
				if !reflect.DeepEqual(args, []string{"limactl", "list", "--json", "test"}) {
					t.Fatalf("unsafe VM reached command: %v", args)
				}
				return string(data), nil
			}
			if err := v.execute(); err == nil {
				t.Fatal("accepted unsafe VM")
			}
		})
	}
	good, _ := json.Marshal(testState(t, v))
	for _, output := range []string{"", "[]", "{}", string(good) + "\n" + string(good), string(good) + "garbage", strings.Replace(string(good), `"plain":true`, `"plain":"yes"`, 1)} {
		v.run = func(_ []string, _ io.Reader, _ bool) (string, error) { return output, nil }
		if _, err := v.info(); err == nil {
			t.Fatalf("accepted malformed Lima output: %s", output)
		}
	}
}

type recordedCommand struct {
	args  []string
	input string
}

func recordingRunner(t *testing.T, state *instance, calls *[]recordedCommand) runner {
	t.Helper()
	return func(args []string, input io.Reader, capture bool) (string, error) {
		if reflect.DeepEqual(args, []string{"limactl", "list", "--json", "test"}) {
			data, _ := json.Marshal(state)
			return string(data) + "\n", nil
		}
		var data []byte
		if input != nil {
			var err error
			data, err = io.ReadAll(input)
			if err != nil {
				t.Fatal(err)
			}
		}
		*calls = append(*calls, recordedCommand{append([]string{}, args...), string(data)})
		if args[0] == "limactl" && args[1] == "start" {
			state.Status = "Running"
		}
		return "", nil
	}
}

func TestConfigureLifecycle(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		v := testVM(t, "configure")
		state := testState(t, v)
		if stopped {
			state.Status = "Stopped"
		}
		var calls []recordedCommand
		v.run = recordingRunner(t, &state, &calls)
		if err := v.execute(); err != nil {
			t.Fatal(err)
		}
		if stopped {
			if !reflect.DeepEqual(calls[0].args, []string{"limactl", "start", "--tty=false", "test"}) {
				t.Fatalf("start ordering: %+v", calls)
			}
			calls = calls[1:]
		}
		provision, _ := v.provision()
		if len(calls) != 2 || calls[0].input != provision || !reflect.DeepEqual(calls[0].args, append(sshArgs(state, "root"), shellJoin([]string{"/bin/bash", "-s"}))) {
			t.Fatalf("configure ordering: %+v", calls)
		}
		if !reflect.DeepEqual(calls[1].args, append(sshArgs(state, "dev"), shellJoin([]string{"/usr/local/share/agent-vm/verify"}))) {
			t.Fatalf("verify command: %v", calls[1].args)
		}
	}
}

func TestCreateLifecycleAndTemporaryRecipe(t *testing.T) {
	v := testVM(t, "create")
	state := testState(t, v)
	state.Status = "Stopped"
	var calls []recordedCommand
	record := recordingRunner(t, &state, &calls)
	var template string
	v.run = func(args []string, input io.Reader, capture bool) (string, error) {
		if args[0] == "limactl" && args[1] == "validate" {
			template = args[2]
			data, err := os.ReadFile(template)
			want, _ := v.render()
			if err != nil || string(data) != string(want) {
				t.Fatalf("temporary recipe: %v", err)
			}
		}
		return record(args, input, capture)
	}
	if err := v.execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(template); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary recipe remains: %v", err)
	}
	bootstrap, _ := recipe.ReadFile("lima/bootstrap.sh")
	if len(calls) != 7 || calls[0].args[1] != "validate" || calls[1].args[1] != "create" || calls[2].args[1] != "start" || calls[3].input != string(bootstrap) {
		t.Fatalf("create ordering: %+v", calls)
	}
	if !reflect.DeepEqual(calls[3].args, append(sshArgs(state, "dev"), shellJoin([]string{"sudo", "-n", "/bin/bash", "-s"}))) {
		t.Fatal("wrong bootstrap user")
	}
	if !reflect.DeepEqual(calls[4].args, append(sshArgs(state, "root"), shellJoin([]string{"test", "-s", "/root/.ssh/authorized_keys"}))) {
		t.Fatal("root not checked before provisioning")
	}
}

func TestLifecycleStopsOnFailure(t *testing.T) {
	for _, action := range []string{"create", "configure"} {
		for _, failure := range []string{"create", "start", "bootstrap", "root-check", "provision"} {
			if action == "configure" && (failure == "create" || failure == "bootstrap" || failure == "root-check") {
				continue
			}
			t.Run(action+"/"+failure, func(t *testing.T) {
				v := testVM(t, action)
				state := testState(t, v)
				state.Status = "Stopped"
				var calls []recordedCommand
				record := recordingRunner(t, &state, &calls)
				failed := false
				var template string
				v.run = func(args []string, input io.Reader, capture bool) (string, error) {
					if failed {
						t.Fatalf("continued after failure: %v", args)
					}
					if args[0] == "limactl" && args[1] == "validate" {
						template = args[2]
					}
					last := args[len(args)-1]
					if (args[0] == "limactl" && args[1] == failure) ||
						(failure == "bootstrap" && strings.Contains(last, "sudo")) ||
						(failure == "root-check" && strings.Contains(last, "authorized_keys")) ||
						(failure == "provision" && last == shellJoin([]string{"/bin/bash", "-s"})) {
						failed = true
						return "", errors.New("synthetic failure")
					}
					return record(args, input, capture)
				}
				if err := v.execute(); err == nil || !failed {
					t.Fatalf("expected %s failure: %v", failure, err)
				}
				if template != "" {
					if _, err := os.Stat(template); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("temporary file remains after failure")
					}
				}
			})
		}
	}
}

func TestConfigurationRecheckedAfterStart(t *testing.T) {
	v := testVM(t, "configure")
	state := testState(t, v)
	state.Status = "Stopped"
	v.run = func(args []string, _ io.Reader, _ bool) (string, error) {
		if args[0] == "ssh" {
			t.Fatal("unsafe VM reached SSH")
		}
		if args[1] == "start" {
			state.Status = "Running"
			state.Config.Plain = false
		}
		data, _ := json.Marshal(state)
		return string(data), nil
	}
	if err := v.execute(); err == nil {
		t.Fatal("accepted unsafe config after start")
	}
}

func TestTokenOnlyOnStdin(t *testing.T) {
	v := testVM(t, "set-token")
	state := testState(t, v)
	var calls []recordedCommand
	v.run = recordingRunner(t, &state, &calls)
	v.readToken = func() (string, error) { return "synthetic-token", nil }
	if err := v.execute(); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].input != "synthetic-token\n" || strings.Contains(strings.Join(calls[0].args, " "), "synthetic-token") {
		t.Fatalf("token transport: %+v", calls)
	}
	for _, token := range []string{"", "space token", "line\nbreak", "nul\x00byte"} {
		calls = nil
		v.readToken = func() (string, error) { return token, nil }
		if err := v.execute(); err == nil || len(calls) != 0 {
			t.Fatal("sent invalid token")
		}
	}
}

func TestDotfilesIndependentAndRepeatable(t *testing.T) {
	script, _ := recipe.ReadFile("lima/dotfiles.sh")
	setup := strings.Split(strings.Split(string(script), "<<'USER_SETUP'\n")[1], "\nUSER_SETUP")[0]
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	installer := "#!/bin/bash\nset -eu\ntest -f './custom setup'\nprintf '%s\\n' \"$HOME\" >> \"$HOME/installed\"\ngit config --global credential.helper 'cache --timeout=7200'\n"
	if err := os.WriteFile(filepath.Join(source, "custom setup"), []byte(installer), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", source}, {"-C", source, "add", "."}, {"-C", source, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Env = append(childEnvironment(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture: %s %v", output, err)
		}
	}
	for _, account := range []string{"root", "dev"} {
		home := filepath.Join(dir, account)
		if err := os.Mkdir(home, 0o700); err != nil {
			t.Fatal(err)
		}
		env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1"}
		for i := 0; i < 2; i++ {
			cmd := exec.Command("/bin/bash", "-s", "--", source, "custom setup")
			cmd.Env, cmd.Stdin = env, strings.NewReader(setup)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("dotfiles: %s %v", output, err)
			}
		}
		data, err := os.ReadFile(filepath.Join(home, "installed"))
		if err != nil || string(data) != home+"\n"+home+"\n" {
			t.Fatalf("independent installs: %s %v", data, err)
		}
		cmd := exec.Command("git", "config", "--global", "--get-all", "credential.https://github.com.helper")
		cmd.Env = env
		output, err := cmd.Output()
		if err != nil || string(output) != "\n!/usr/local/bin/gh auth git-credential\n" {
			t.Fatalf("Git helper: %s %v", output, err)
		}
	}
}

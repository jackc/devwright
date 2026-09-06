package devsandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNativeCommandWorkingDirectory(t *testing.T) {
	operator := t.TempDir()
	target := t.TempDir()
	t.Chdir(operator)
	t.Setenv("GH_TOKEN", "synthetic-host-token")
	a := newUserAdmin(context.Background(), io.Discard)
	adminDir, err := a.command(nil, "/bin/pwd", "-P")
	if err != nil || strings.TrimSpace(adminDir) != "/" {
		t.Fatalf("administration inherited operator cwd: %q, %v", adminDir, err)
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.commandIn(target, nil, "/bin/sh", "-c", `test -z "${GH_TOKEN+x}" && pwd -P`)
	if err != nil || strings.TrimSpace(result) != target {
		t.Fatalf("target command did not use explicit cwd and clean environment: %q, %v", result, err)
	}
}

func TestSystemSSHReadinessBeforeValidation(t *testing.T) {
	for _, tc := range []struct {
		name, banner string
		configError  bool
	}{
		{"socket activation creates keys", "SSH-2.0-fixture\r\n", false},
		{"invalid configuration still fails", "SSH-2.0-fixture\r\n", true},
		{"wrong service", "HTTP/1.1 200 OK\r\n", false},
		{"incomplete banner", "SS", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			key := filepath.Join(t.TempDir(), "synthetic-host-key")
			done := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				// Model Apple's wrapper: keys exist only after the connection.
				if err := os.WriteFile(key, []byte("synthetic"), 0600); err != nil {
					done <- err
					return
				}
				_, err = io.WriteString(conn, tc.banner)
				done <- err
			}()
			called := false
			invalid := errors.New("invalid fixture SSH configuration")
			err = validateSystemSSH(listener.Addr().(*net.TCPAddr).Port, func() error {
				called = true
				if _, err := os.Stat(key); err != nil {
					t.Error("validated before socket activation created the host key")
					return err
				}
				if tc.configError {
					return invalid
				}
				return nil
			})
			if serverErr := <-done; serverErr != nil {
				t.Fatal(serverErr)
			}
			if strings.HasPrefix(tc.banner, "SSH-") {
				if !called || (tc.configError && !errors.Is(err, invalid)) || (!tc.configError && err != nil) {
					t.Fatalf("called=%v, err=%v", called, err)
				}
			} else if called || err == nil || !strings.Contains(err.Error(), "SSH server banner") {
				t.Fatalf("accepted non-SSH service: called=%v, err=%v", called, err)
			}
		})
	}
}

func TestAcceptanceSSHPreflight(t *testing.T) {
	data, err := os.ReadFile("tests/users-macos.sh")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(data), "check_system_ssh() {\n")
	if !ok {
		t.Fatal("missing acceptance preflight")
	}
	body, _, ok := strings.Cut(rest, "\n}\n")
	if !ok {
		t.Fatal("unterminated acceptance preflight")
	}
	for _, mode := range []string{"ready", "unreachable", "invalid-config"} {
		t.Run(mode, func(t *testing.T) {
			// Run the actual standalone function with synthetic system commands.
			// A direct sshd validation before a connection must fail this fixture.
			script := `set -eu
fixture=$1
mode=$2
ssh-keyscan() {
  test "$mode" != unreachable || return 1
  printf synthetic > "$fixture/host-keys-created"
}
sudo() {
  test -f "$fixture/host-keys-created" || return 90
  test "$*" = '-n /usr/sbin/sshd -t' || return 91
  printf validated > "$fixture/validated"
  test "$mode" != invalid-config
}
check_system_ssh() {
` + body + "\n}\ncheck_system_ssh\n"
			dir := t.TempDir()
			out, err := exec.Command("/bin/bash", "-c", script, "fixture", dir, mode).CombinedOutput()
			switch mode {
			case "ready":
				if err != nil {
					t.Fatalf("%v: %s", err, out)
				}
			case "unreachable":
				if err == nil || !strings.Contains(string(out), "Enable Remote Login") {
					t.Fatalf("%v: %s", err, out)
				}
				if _, err := os.Stat(filepath.Join(dir, "validated")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("validated before a successful connection")
				}
			case "invalid-config":
				if err == nil || !strings.Contains(string(out), "configuration or host keys failed validation") {
					t.Fatalf("%v: %s", err, out)
				}
			}
		})
	}
}

func fixtureUserKey() string {
	b := append([]byte{0, 0, 0, 11}, []byte("ssh-ed25519\x00\x00\x00\x20")...)
	b = append(b, make([]byte, 32)...)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(b) + " synthetic"
}
func TestUserOptions(t *testing.T) {
	for _, action := range []string{"create", "configure", "verify", "render", "ssh-config", "install-ssh", "shell", "delete"} {
		o, err := parseOptions([]string{action, "example", "--backend", "user"})
		if err != nil || o.sshPort != 22 {
			t.Fatalf("%s: %+v %v", action, o, err)
		}
	}
	for _, args := range [][]string{{"create", "x", "--backend", "user", "--cpus", "2"}, {"create", "x", "--backend", "user", "--memory", "4GiB"}, {"create", "x", "--backend", "user", "--container"}, {"create", "x", "--backend", "user", "--codex-requirements", "x"}, {"configure", "x", "--backend", "user", "--reset-codex-requirements"}, {"create", "x", "--backend", "user", "--claude-managed-settings", "x"}, {"configure", "x", "--backend", "user", "--reset-claude-managed-settings"}, {"delete", "--backend", "user"}, {"list", "x", "--backend", "user"}, {"create", strings.Repeat("a", 29), "--backend", "user"}, {"create", "x", "--ssh-port", "22"}, {"create", "x", "--backend", "user", "--ssh-port", "0"}, {"verify", "x", "--backend", "user", "--ssh-port", "22"}, {"create", "x", "--backend", "user", "--remove-home"}} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
func TestUserOfflineRender(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var out strings.Builder
	if err := Run(context.Background(), []string{"render", "example", "--backend", "user", "--ssh-port", "2222"}, "test", nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out.String()), &v); err != nil {
		t.Fatal(err)
	}
	if v["ssh_port"] != float64(2222) || v["account"] != "dsb-example" || v["group"] != "dsb-example" || v["ssh_service"] != "existing system OpenSSH" {
		t.Fatal(v)
	}
	provisioning, ok := v["provisioning"].(map[string]any)
	if !ok || provisioning["run_as"] != "dsb-example" || provisioning["config_source"] != "built-in editable defaults" || provisioning["claude_config_source"] != "built-in editable defaults" {
		t.Fatal(v)
	}
}
func TestHelperValidation(t *testing.T) {
	good := userRequest{Action: "create", Name: "example", Port: 22, Installer: "install", PublicKey: fixtureUserKey()}
	if err := validateUserRequest(good); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*userRequest){func(r *userRequest) { r.Name = "../root" }, func(r *userRequest) { r.Action = "exec" }, func(r *userRequest) { r.PublicKey += "\ncommand=bad" }, func(r *userRequest) { r.PublicKey = "ssh-rsa junk" }, func(r *userRequest) { r.PublicKey = "" }, func(r *userRequest) { r.Config = []byte("[broken") }, func(r *userRequest) { r.ClaudeConfig = []byte("[]") }, func(r *userRequest) { r.Repository = "https://example.invalid/dotfiles"; r.Installer = "../../root" }, func(r *userRequest) { r.Port = 65536 }, func(r *userRequest) { r.RemoveHome = true }} {
		r := good
		mutate(&r)
		if err := validateUserRequest(r); err == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
}
func TestNativeSudoAudit(t *testing.T) {
	if err := checkUserSudo("User dsb-x may run the following commands:\n    (ALL : ALL) !ALL\n"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"", "    (ALL) NOPASSWD: ALL\n    (ALL) !ALL", "    (root) /usr/bin/vi", "    (ALL) !ALL, /bin/sh"} {
		if checkUserSudo(s) == nil {
			t.Fatalf("accepted sudo grants %q", s)
		}
	}
}
func TestNativeSSHConfiguration(t *testing.T) {
	s := userState{Name: "example", Account: "dsb-example", Port: 2222}
	dir := filepath.Join(t.TempDir(), "keys with spaces")
	config := userSSHConfig(s, dir)
	file := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(file, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	text, err := exec.Command("ssh", "-G", "-F", file, "user-example").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"hostname localhost", "user dsb-example", "port 2222", "forwardagent no", "identityagent none", "stricthostkeychecking true", "controlmaster false"} {
		if !strings.Contains(string(text), want) {
			t.Fatalf("missing %s: %s", want, text)
		}
	}
	args := userSSHArgs(s, dir)
	for _, unwanted := range []string{"sudo", "ForwardAgent=yes", "StrictHostKeyChecking=no"} {
		for _, arg := range args {
			if arg == unwanted {
				t.Fatal(args)
			}
		}
	}
}
func TestScopedNativeSSHD(t *testing.T) {
	if _, err := os.Stat("/usr/sbin/sshd"); err != nil {
		t.Skip("no host sshd")
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "hostkey")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	a := userAdmin{etc: "/etc"}
	s := userState{Name: "example", Account: "dsb-example"}
	part := filepath.Join(dir, "part")
	os.WriteFile(part, a.sshRule(&s), 0600)
	config := filepath.Join(dir, "config")
	os.WriteFile(config, []byte("HostKey "+key+"\nInclude "+part+"\nUsePAM yes\nPasswordAuthentication yes\n"), 0600)
	for _, account := range []string{"root", "dsb-example"} {
		out, err := exec.Command("/usr/sbin/sshd", "-T", "-f", config, "-C", "user="+account+",host=localhost,addr=127.0.0.1").CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		want := "passwordauthentication yes"
		if account == "dsb-example" {
			want = "passwordauthentication no"
		}
		if !strings.Contains(string(out), want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
}
func TestSSHInstallNativePreservesOtherBackends(t *testing.T) {
	home := t.TempDir()
	v := vm{home: home, out: io.Discard}
	os.MkdirAll(filepath.Join(home, ".ssh"), 0700)
	original := []byte("Host example\n  HostName example.invalid\n")
	os.WriteFile(filepath.Join(home, ".ssh", "config"), original, 0600)
	if err := v.installSSHText("x.user.config", "Host user-x\n  HostName localhost\n", "ok"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if !strings.Contains(string(before), string(original)) {
		t.Fatal(string(before))
	}
	if err := v.installSSHText("x.user.config", "Host user-x\n  HostName localhost\n", "ok"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("non-idempotent SSH include")
	}
}
func TestNativeClientSymlinkRefusal(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "managed"), []byte("dev-sandbox-user-v1\n"), 0600)
	os.WriteFile(filepath.Join(dir, "id_ed25519.pub"), []byte(fixtureUserKey()), 0600)
	os.Symlink("id_ed25519.pub", filepath.Join(dir, "id_ed25519"))
	if checkUserClient(dir, fixtureUserKey()) == nil {
		t.Fatal("accepted symlinked private key")
	}
}

func TestNativePrivateKeyPermissions(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	for name, data := range map[string]string{"managed": "dev-sandbox-user-v1\n", "id_ed25519": "synthetic private fixture", "id_ed25519.pub": fixtureUserKey()} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkUserClient(dir, fixtureUserKey()); err != nil {
		t.Fatal(err)
	}
	os.Chmod(filepath.Join(dir, "id_ed25519"), 0644)
	if err := checkUserClient(dir, fixtureUserKey()); err == nil {
		t.Fatal("accepted publicly readable private key")
	}
}
func TestNativeDeletionClientIsolation(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, ".ssh", "dev-sandbox")
	dir := filepath.Join(base, "user", "fixture")
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, "managed"), []byte("dev-sandbox-user-v1\n"), 0600)
	os.WriteFile(filepath.Join(base, "fixture.user.config"), []byte(sshHeader+"Host user-fixture\n  User dsb-fixture\n"), 0600)
	unrelated := filepath.Join(base, "fixture.incus.config")
	os.WriteFile(unrelated, []byte("unrelated"), 0600)
	if err := removeUserClient(home, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := removeUserClient(home, "fixture"); err != nil {
		t.Fatalf("cleanup not idempotent: %v", err)
	}
	b, err := os.ReadFile(unrelated)
	if err != nil || string(b) != "unrelated" {
		t.Fatal("changed unrelated backend configuration")
	}
}

func TestDarwinAdministrativeACLs(t *testing.T) {
	for _, text := range []string{"drwxr-xr-x root wheel\n", " 0: group:everyone deny delete\n", " 0: group:everyone allow list,search,readattr,readsecurity\n"} {
		if !readOnlyDarwinACL(text) {
			t.Fatal("rejected read-only/deny ACL")
		}
	}
	for _, permission := range []string{"write", "add_file", "add_subdirectory", "delete", "delete_child", "append", "writeattr", "writeextattr", "writesecurity", "chown", "unknown"} {
		if readOnlyDarwinACL(" 0: user:fixture allow read," + permission + "\n") {
			t.Fatalf("accepted ACL permission %s", permission)
		}
	}
}

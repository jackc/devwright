package agentvm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func buildCLI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "agent-vm")
	cmd := exec.Command("go", "build", "-o", binary, "./cmd/agent-vm")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v: %s", err, output)
	}
	return binary
}

func TestInstalledCLIOutsideCheckout(t *testing.T) {
	binary := buildCLI(t)
	cmd := exec.Command(binary, "render", "--cpus", "8", "--memory", "8GiB")
	cmd.Dir = t.TempDir()
	data, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config["cpus"] != float64(8) || config["memory"] != "8GiB" || len(config["provision"].([]any)) != 0 {
		t.Fatalf("unexpected recipe: %s", data)
	}
}

func TestTokenRealTerminal(t *testing.T) {
	binary := buildCLI(t)
	for _, mode := range []string{"success", "interrupt", "terminate"} {
		t.Run(mode, func(t *testing.T) { runPrompt(t, binary, mode) })
	}
}

const promptShell = `before=$(stty -g)
"$AGENT_VM_TEST_BINARY" set-token test &
child=$!
printf 'child-pid:%s\n' "$child"
wait "$child"
result=$?
after=$(stty -g)
printf '\nterminal-before:%s\nterminal-after:%s\n' "$before" "$after"
exit "$result"
`

func runPrompt(t *testing.T, binary, mode string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{
		"limactl": "#!/bin/sh\ncase \"$1\" in\n--version) echo 'limactl version 2.2.0' ;;\nlist) cat \"$AGENT_VM_TEST_STATE\" ;;\n*) exit 9 ;;\nesac\n",
		"ssh":     "#!/bin/sh\nif [ \"$1\" = -G ]; then exit 0; fi\ncat > \"$AGENT_VM_TEST_CAPTURE\"\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	state := instance{Name: "test", Dir: dir, Status: "Running"}
	state.Config.Plain = true
	state.Config.User.Name = "dev"
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	statePath, capture := filepath.Join(dir, "state.json"), filepath.Join(dir, "captured")
	if err := os.WriteFile(statePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	master, slave, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", promptShell)
	cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin", "AGENT_VM_TEST_BINARY="+binary, "AGENT_VM_TEST_STATE="+statePath, "AGENT_VM_TEST_CAPTURE="+capture)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if !waited {
			_ = cmd.Wait()
		}
	}()
	slave.Close()
	chunks := make(chan string)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer close(chunks)
		buffer := make([]byte, 4096)
		for {
			n, err := master.Read(buffer)
			if n > 0 {
				select {
				case chunks <- string(buffer[:n]):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() { cancel(); master.Close(); <-readDone }()
	var output strings.Builder
	prompted := false
	for chunk := range chunks {
		output.WriteString(chunk)
		if prompted || !strings.Contains(output.String(), "(hidden): ") {
			continue
		}
		prompted = true
		switch mode {
		case "success":
			_, err = io.WriteString(master, "synthetic-terminal-token\r")
		case "interrupt":
			_, err = io.WriteString(master, "\x03")
		case "terminate":
			match := regexp.MustCompile(`child-pid:(\d+)`).FindStringSubmatch(output.String())
			if len(match) != 2 {
				t.Fatalf("missing child PID: %s", output.String())
			}
			pid, parseErr := strconv.Atoi(match[1])
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			err = syscall.Kill(pid, syscall.SIGTERM)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	err = cmd.Wait()
	waited = true
	if ctx.Err() != nil {
		t.Fatalf("terminal timeout: %s", output.String())
	}
	if !prompted {
		t.Fatalf("no prompt: %s", output.String())
	}
	expected := 0
	if mode != "success" {
		expected = 1
	}
	if cmd.ProcessState.ExitCode() != expected {
		t.Fatalf("exit %v: %s", err, output.String())
	}
	value := func(key string) string {
		match := regexp.MustCompile(key + `:([^\r\n]+)`).FindStringSubmatch(output.String())
		if len(match) != 2 {
			t.Fatalf("missing %s: %s", key, output.String())
		}
		return normalizeTerminal(match[1])
	}
	if value("terminal-before") != value("terminal-after") {
		t.Fatalf("terminal settings changed: %s", output.String())
	}
	if strings.Contains(output.String(), "synthetic-terminal-token") {
		t.Fatal("token echoed")
	}
	if mode == "success" {
		data, err := os.ReadFile(capture)
		if err != nil || string(data) != "synthetic-terminal-token\n" {
			t.Fatalf("token transfer: %q %v", data, err)
		}
	} else if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatalf("Canceled token input reached SSH: %v", err)
	}
}

func normalizeTerminal(value string) string {
	if runtime.GOOS != "darwin" {
		return value
	}
	// Darwin's transient PENDIN flag is set when canonical input is restored.
	return regexp.MustCompile(`lflag=([0-9a-f]+)`).ReplaceAllStringFunc(value, func(flag string) string {
		number, _ := strconv.ParseUint(strings.TrimPrefix(flag, "lflag="), 16, 64)
		return fmt.Sprintf("lflag=%x", number&^0x20000000)
	})
}

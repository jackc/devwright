package devsandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Launch this test binary as a real child, without depending on host Ruby.
func TestProcessHelper(t *testing.T) {
	if os.Getenv("DEV_SANDBOX_PROCESS_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	switch args[0] {
	case "args":
		json.NewEncoder(os.Stdout).Encode(args[1:])
	case "env":
		for _, key := range []string{"SSH_AUTH_SOCK", "GH_TOKEN", "GITHUB_TOKEN", "OPENAI_API_KEY"} {
			if _, ok := os.LookupEnv(key); ok {
				os.Exit(9)
			}
		}
		os.Stdout.WriteString("absent\n")
	case "fail":
		os.Stderr.WriteString("diagnostic\n")
		os.Exit(7)
	case "sleep":
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

func TestSubprocessSafety(t *testing.T) {
	t.Setenv("DEV_SANDBOX_PROCESS_HELPER", "1")
	for _, key := range []string{"SSH_AUTH_SOCK", "GH_TOKEN", "GITHUB_TOKEN", "OPENAI_API_KEY"} {
		t.Setenv(key, "synthetic-token")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base := []string{executable, "-test.run=^TestProcessHelper$", "--"}
	var stderr bytes.Buffer
	run := commandRunner(context.Background(), nil, io.Discard, &stderr)
	output, err := run(append(base, "env"), nil, true)
	if err != nil || output != "absent\n" || os.Getenv("GH_TOKEN") != "synthetic-token" {
		t.Fatalf("environment filtering: %s %v", output, err)
	}
	values := []string{"a path with spaces", "$(false)", "`false`", "one; two", "quote'and\"double", ""}
	output, err = run(append(append([]string{}, append(base, "args")...), values...), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	var actual []string
	if err := json.Unmarshal([]byte(output), &actual); err != nil || !reflect.DeepEqual(actual, values) {
		t.Fatalf("argument preservation: %q %v", output, err)
	}
	_, err = run(append(base, "fail", "synthetic-token"), nil, true)
	if err == nil || !strings.Contains(err.Error(), "exit 7") || strings.Contains(err.Error(), "synthetic-token") || stderr.String() != "diagnostic\n" {
		t.Fatalf("failure reporting: %v, %q", err, stderr.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = commandRunner(ctx, nil, io.Discard, io.Discard)(append(base, "sleep"), nil, true)
	if err != context.DeadlineExceeded {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestShellQuoting(t *testing.T) {
	values := []string{"a'; $(false)", "a path", "", "line\nbreak", "`false`", "back\\slash"}
	command := "set -- " + shellJoin(values) + "; printf '%s\\000' \"$@\""
	output, err := exec.Command("/bin/sh", "-c", command).Output()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00"), values) {
		t.Fatalf("remote shell arguments changed: %q", output)
	}
}

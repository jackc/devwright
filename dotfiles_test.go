package devwright

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDotfilesFetchFailureCleansUpAndStopsProvisioning(t *testing.T) {
	v := testVM(t, "configure")
	v.dotfilesRepo = "https://example.invalid/private.git"
	var clone string
	want := errors.New("authentication failed")
	v.hostGit = func(args []string, _ io.Reader, _ bool) (string, error) {
		clone = args[len(args)-1]
		return "", want
	}
	v.run = func(_ []string, _ io.Reader, _ bool) (string, error) {
		t.Fatal("provisioning attempted after failed host fetch")
		return "", nil
	}
	if err := v.configure(testState(t, v)); !errors.Is(err, want) {
		t.Fatalf("fetch failure: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(clone)); !os.IsNotExist(err) {
		t.Fatalf("host temporary directory remains: %v", err)
	}
}

func TestDotfilesBundleTransportCleanup(t *testing.T) {
	for _, fail := range []bool{false, true} {
		probe := filepath.Join(t.TempDir(), "bundle-path")
		provision := "printf '%s' \"$dotfiles_bundle\" > " + shellQuote(probe) + "\n" +
			"test \"$(cat \"$dotfiles_bundle\")\" = 'bundle contents'\n" +
			"trap ':' EXIT\n"
		if fail {
			provision += "exit 42\n"
		}
		cmd := exec.Command("/bin/bash", "-s")
		cmd.Stdin = strings.NewReader(dotfilesBundleScript([]byte("bundle contents"), provision))
		output, err := cmd.CombinedOutput()
		if (err != nil) != fail || (fail && cmd.ProcessState.ExitCode() != 42) {
			t.Fatalf("transport status: %s %v", output, err)
		}
		path, err := os.ReadFile(probe)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Dir(string(path))); !os.IsNotExist(err) {
			t.Fatalf("guest temporary bundle remains: %v", err)
		}
	}
}

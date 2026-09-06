package devsandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type runner func(args []string, input io.Reader, capture bool) (string, error)

func childEnvironment() []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "SSH_AUTH_SOCK", "GH_TOKEN", "GITHUB_TOKEN", "OPENAI_API_KEY":
			continue
		}
		env = append(env, entry)
	}
	return env
}

func commandRunner(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) runner {
	return func(args []string, input io.Reader, capture bool) (string, error) {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Env = childEnvironment()
		cmd.Stderr = stderr
		cmd.Stdout = stdout
		cmd.Stdin = input
		if input == nil && !capture {
			cmd.Stdin = stdin
		}
		var output bytes.Buffer
		if capture {
			cmd.Stdout = &output
		}
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			// Never include the command arguments or stdin in an error message.
			var status *exec.ExitError
			if errors.As(err, &status) {
				return "", fmt.Errorf("%s failed (exit %d); see output above. No local-execution fallback", filepath.Base(args[0]), status.ExitCode())
			}
			return "", fmt.Errorf("could not start %s; check that it is installed and executable", filepath.Base(args[0]))
		}
		return output.String(), nil
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

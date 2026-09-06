package verification

import (
	"bytes"
	"context"
	"os/exec"
	"syscall"
	"time"
)

// Each probe owns a process group, including descendants that outlive their parent.
func command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	return cmd
}

func stop(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func run(ctx context.Context, args ...string) (string, string, error) {
	cmd := command(ctx, args...)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	defer stop(cmd)
	if err := cmd.Start(); err != nil {
		return "", "", err
	}
	// exec's context watcher may finish when the parent exits, before its
	// descendants close the pipes. Keep group cancellation alive until Wait ends.
	cancelGroup := context.AfterFunc(ctx, func() { stop(cmd) })
	defer cancelGroup()
	err := cmd.Wait()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return out.String(), stderr.String(), err
}

func probe(args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return run(ctx, args...)
}

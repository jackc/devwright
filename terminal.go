package agentvm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Poll nonblocking input so cancellation never leaves a blocked terminal read.
// On macOS, closing a tty with an outstanding blocking read can itself block.
type terminalInput struct {
	ctx context.Context
	fd  int
	io.Writer
}

func (t terminalInput) Read(data []byte) (int, error) {
	for {
		if err := t.ctx.Err(); err != nil {
			return 0, err
		}
		fds := []unix.PollFd{{Fd: int32(t.fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 100)
		if err == unix.EINTR || (err == nil && n == 0) {
			continue
		}
		if err != nil {
			return 0, err
		}
		n, err = unix.Read(t.fd, data)
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 && err == nil {
			return 0, io.EOF
		}
		return n, err
	}
}

func readToken(ctx context.Context) (string, error) {
	console, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", errors.New("a terminal is required to enter a token securely")
	}
	defer console.Close()
	return readTokenFrom(ctx, console)
}

func readTokenFrom(ctx context.Context, console *os.File) (token string, err error) {
	fd := int(console.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", errors.New("a terminal is required to enter a token securely")
	}
	defer func() {
		if restoreErr := term.Restore(fd, state); restoreErr != nil {
			err = fmt.Errorf("restore terminal settings: %w", restoreErr)
		}
	}()
	if err := unix.SetNonblock(fd, true); err != nil {
		return "", err
	}
	defer unix.SetNonblock(fd, false)
	terminal := term.NewTerminal(terminalInput{ctx: ctx, fd: fd, Writer: console}, "")
	token, err = terminal.ReadPassword("Fine-grained GitHub token for this VM (hidden): ")
	if err != nil {
		fmt.Fprint(console, "\r\n")
		if err == io.EOF {
			err = errors.New("token entry canceled")
		}
	}
	return token, err
}

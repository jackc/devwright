package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	devwright "github.com/jackc/devwright"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := devwright.Run(ctx, os.Args[1:], version, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "devwright:", err)
		os.Exit(1)
	}
}

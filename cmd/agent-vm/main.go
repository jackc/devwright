package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	agentvm "agent-sandbox-config"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := agentvm.Run(ctx, os.Args[1:], version, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "agent-vm:", err)
		os.Exit(1)
	}
}

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	devsandbox "dev-sandbox"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := devsandbox.Run(ctx, os.Args[1:], version, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "dev-sandbox:", err)
		os.Exit(1)
	}
}

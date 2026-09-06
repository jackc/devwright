// agent-vm-verify is installed in the guest; it needs no compiler or interpreter.
package main

import (
	"agent-sandbox-config/internal/verification"
	"fmt"
	"os"
)

func main() {
	var err error
	switch {
	case len(os.Args) == 1:
		err = verification.Check(os.Stdout)
	case len(os.Args) == 4 && os.Args[1] == "sandbox-probe":
		err = verification.SandboxProbe(os.Args[2], os.Args[3], os.Stdout)
	default:
		err = fmt.Errorf("usage: agent-vm-verify [sandbox-probe CANARY SIBLING]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

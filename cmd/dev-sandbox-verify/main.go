// dev-sandbox-verify is installed in the guest; it needs no compiler or interpreter.
package main

import (
	"dev-sandbox/internal/verification"
	"fmt"
	"io"
	"os"
)

func main() {
	var err error
	switch {
	case len(os.Args) == 5 && os.Args[1] == "user-config" && (os.Args[4] == "true" || os.Args[4] == "false"):
		var data []byte
		data, err = io.ReadAll(io.LimitReader(os.Stdin, 2*1024*1024))
		if err == nil {
			err = verification.WriteUserConfig(os.Args[2], os.Args[3], os.Args[4] == "true", data)
		}
	case len(os.Args) == 4 && os.Args[1] == "native":
		err = verification.CheckNative(os.Args[2], os.Args[3], os.Stdout)
	case len(os.Args) == 4 && os.Args[1] == "setup-user":
		err = verification.SetupUser(os.Args[2], os.Args[3])
	case len(os.Args) == 3 && os.Args[1] == "native-sandbox":
		err = verification.NativeSandboxProbe(os.Args[2], os.Stdout)
	case len(os.Args) == 1:
		err = verification.Check(os.Stdout)
	case len(os.Args) == 4 && os.Args[1] == "sandbox-probe":
		err = verification.SandboxProbe(os.Args[2], os.Args[3], os.Stdout)
	default:
		err = fmt.Errorf("usage: dev-sandbox-verify [sandbox-probe CANARY SIBLING]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

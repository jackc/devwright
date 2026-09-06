package verification

import (
	"strings"
	"testing"
)

func TestPortableStartupHooks(t *testing.T) {
	content := "#!/bin/bash\ncase $- in *i*) ;; *) return ;; esac\nexport KEEP=yes\n"
	first, err := StartupHook(content, "# BEGIN DEV-SANDBOX CREDENTIALS", "# END DEV-SANDBOX CREDENTIALS", credentialsHook)
	if err != nil {
		t.Fatal(err)
	}
	second, err := StartupHook(first, "# BEGIN DEV-SANDBOX CREDENTIALS", "# END DEV-SANDBOX CREDENTIALS", credentialsHook)
	if err != nil || first != second {
		t.Fatalf("non-idempotent: %v", err)
	}
	if !strings.HasPrefix(first, "#!/bin/bash\n"+credentialsHook) || !strings.Contains(first, "export KEEP=yes") {
		t.Fatal(first)
	}
	repaired, err := StartupHook("return\n"+first, "# BEGIN DEV-SANDBOX CREDENTIALS", "# END DEV-SANDBOX CREDENTIALS", credentialsHook)
	if err != nil || !strings.HasPrefix(repaired, credentialsHook) {
		t.Fatalf("hook not repaired: %v %s", err, repaired)
	}
	for _, s := range []string{"# BEGIN DEV-SANDBOX CREDENTIALS\nkeep\n", "# END DEV-SANDBOX CREDENTIALS\n", "# BEGIN DEV-SANDBOX CREDENTIALS\n# BEGIN DEV-SANDBOX CREDENTIALS\n# END DEV-SANDBOX CREDENTIALS\n"} {
		if _, err := StartupHook(s, "# BEGIN DEV-SANDBOX CREDENTIALS", "# END DEV-SANDBOX CREDENTIALS", credentialsHook); err == nil {
			t.Fatal("accepted malformed block")
		}
	}
}

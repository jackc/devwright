package verification

import (
	"strings"
	"testing"
)

func TestPortableStartupHooks(t *testing.T) {
	content := "#!/bin/bash\ncase $- in *i*) ;; *) return ;; esac\nexport KEEP=yes\n"
	first, err := StartupHook(content, "# BEGIN DEVWRIGHT CREDENTIALS", "# END DEVWRIGHT CREDENTIALS", credentialsHook)
	if err != nil {
		t.Fatal(err)
	}
	second, err := StartupHook(first, "# BEGIN DEVWRIGHT CREDENTIALS", "# END DEVWRIGHT CREDENTIALS", credentialsHook)
	if err != nil || first != second {
		t.Fatalf("non-idempotent: %v", err)
	}
	if !strings.HasPrefix(first, "#!/bin/bash\n"+credentialsHook) || !strings.Contains(first, "export KEEP=yes") {
		t.Fatal(first)
	}
	repaired, err := StartupHook("return\n"+first, "# BEGIN DEVWRIGHT CREDENTIALS", "# END DEVWRIGHT CREDENTIALS", credentialsHook)
	if err != nil || !strings.HasPrefix(repaired, credentialsHook) {
		t.Fatalf("hook not repaired: %v %s", err, repaired)
	}
	for _, s := range []string{"# BEGIN DEVWRIGHT CREDENTIALS\nkeep\n", "# END DEVWRIGHT CREDENTIALS\n", "# BEGIN DEVWRIGHT CREDENTIALS\n# BEGIN DEVWRIGHT CREDENTIALS\n# END DEVWRIGHT CREDENTIALS\n"} {
		if _, err := StartupHook(s, "# BEGIN DEVWRIGHT CREDENTIALS", "# END DEVWRIGHT CREDENTIALS", credentialsHook); err == nil {
			t.Fatal("accepted malformed block")
		}
	}
}

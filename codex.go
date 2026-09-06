package devsandbox

import (
	"fmt"
	"os"

	"dev-sandbox/internal/codexpolicy"
	"github.com/pelletier/go-toml/v2"
)

// Read once before any VM operations; file paths are host-side inputs only.
func (o *options) loadCodexFiles() error {
	for _, entry := range []struct {
		path    string
		payload *[]byte
		policy  bool
	}{
		{o.codexRequirements, &o.requirementsPayload, true},
		{o.codexConfig, &o.configPayload, false},
	} {
		if entry.path == "" {
			continue
		}
		data, err := os.ReadFile(entry.path)
		if err != nil {
			return fmt.Errorf("read Codex file %q: %w", entry.path, err)
		}
		var value map[string]any
		if err := toml.Unmarshal(data, &value); err != nil {
			return fmt.Errorf("invalid TOML in %q: %w", entry.path, err)
		}
		if entry.policy {
			if _, err := codexpolicy.Parse(data); err != nil {
				return fmt.Errorf("invalid requirements in %q: %w", entry.path, err)
			}
		}
		*entry.payload = data
	}
	return nil
}

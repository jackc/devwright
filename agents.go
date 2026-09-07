package devwright

import (
	"fmt"
	"os"

	"github.com/jackc/devwright/internal/claudepolicy"
	"github.com/jackc/devwright/internal/codexpolicy"
	"github.com/pelletier/go-toml/v2"
)

// Read once before any VM operations; file paths are host-side inputs only.
func (o *options) loadAgentFiles() error {
	for _, entry := range []struct {
		path    string
		payload *[]byte
		check   func([]byte) error
	}{
		{o.codexRequirements, &o.requirementsPayload, checkCodexRequirements},
		{o.codexConfig, &o.configPayload, checkTOML},
		{o.claudeManagedSettings, &o.managedPayload, checkClaudeManagedSettings},
		{o.claudeConfig, &o.claudeConfigPayload, checkClaudeSettings},
	} {
		if entry.path == "" {
			continue
		}
		data, err := os.ReadFile(entry.path)
		if err != nil {
			return fmt.Errorf("read agent configuration file %q: %w", entry.path, err)
		}
		if err := entry.check(data); err != nil {
			return fmt.Errorf("invalid %q: %w", entry.path, err)
		}
		*entry.payload = data
	}
	return nil
}

func checkTOML(data []byte) error {
	var value map[string]any
	if err := toml.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("invalid TOML: %w", err)
	}
	return nil
}

func checkCodexRequirements(data []byte) error {
	if err := checkTOML(data); err != nil {
		return err
	}
	if _, err := codexpolicy.Parse(data); err != nil {
		return fmt.Errorf("invalid requirements: %w", err)
	}
	return nil
}

// Claude Code drops an invalid user settings file as a whole. Validate all
// published settings, not just the managed keys that the verifier compares.
func checkClaudeSettings(data []byte) error {
	if err := claudepolicy.Validate(data); err != nil {
		return fmt.Errorf("invalid Claude Code settings: %w", err)
	}
	return nil
}

func checkClaudeManagedSettings(data []byte) error {
	if _, err := claudepolicy.Parse(data); err != nil {
		return fmt.Errorf("invalid managed settings: %w", err)
	}
	return nil
}

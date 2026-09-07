package claudepolicy

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

// The published settings schema is bundled so validation needs neither Claude
// Code on the host nor network access. See schema/README.md for its provenance.
//
//go:embed schema/settings.json
var settingsSchema []byte

var resolvedSettingsSchema = sync.OnceValues(func() (*jsonschema.Resolved, error) {
	var schema jsonschema.Schema
	if err := json.Unmarshal(settingsSchema, &schema); err != nil {
		return nil, err
	}
	// No loader: external references must never make validation fetch a URL.
	return schema.Resolve(nil)
})

// Validate checks a user settings file against the bundled published schema.
// Parse remains the narrower reader for managed policy posture comparisons.
func Validate(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	schema, err := resolvedSettingsSchema()
	if err != nil {
		return fmt.Errorf("load embedded Claude settings schema: %w", err)
	}
	return schema.Validate(value)
}

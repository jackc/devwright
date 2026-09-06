// Package codexpolicy describes the managed requirements exposed by Codex's
// app-server. Codex itself validates the full requirements schema in the guest.
package codexpolicy

import "github.com/pelletier/go-toml/v2"

type Requirements struct {
	Default  string          `toml:"default_permissions"`
	Profiles map[string]bool `toml:"allowed_permission_profiles"`
	Features map[string]bool `toml:"features"`
}

func Parse(data []byte) (Requirements, error) {
	var r Requirements
	err := toml.Unmarshal(data, &r)
	return r, err
}

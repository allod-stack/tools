package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// registryCheckout looks up id (for example "allod/memory") in the
// repository registry (<workDir>/allod/inventory/scripts/repositories.json)
// and returns its checkout path, relative to workDir(). A missing or
// malformed registry file means no id matches; it is not an error by
// itself.
func registryCheckout(id string) (checkout string, ok bool) {
	data, err := os.ReadFile(filepath.Join(workDir(), "allod", "inventory", "scripts", "repositories.json"))
	if err != nil {
		return "", false
	}
	var registry struct {
		Repositories map[string]struct {
			Checkout string `json:"checkout"`
		} `json:"repositories"`
	}
	if json.Unmarshal(data, &registry) != nil {
		return "", false
	}
	entry, found := registry.Repositories[id]
	if !found || entry.Checkout == "" {
		return "", false
	}
	return entry.Checkout, true
}

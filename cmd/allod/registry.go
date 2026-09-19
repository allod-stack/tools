package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// registryCheckout looks up id (for example "allod/memory") in the
// repository registry (registryPath()) and returns its checkout path,
// relative to workDir(). A missing or malformed registry file means no id
// matches; it is not an error by itself.
func registryCheckout(id string) (checkout string, ok bool) {
	data, err := os.ReadFile(registryPath())
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

// registryPath returns the file registryCheckout reads: $INVENTORY's
// scripts/repositories.json when INVENTORY is set — the same variable the
// nexus host module renders from nexus.provisioning.inventoryCheckout, and
// every nexus script resolves the registry through — or
// <workDir>/allod/inventory/scripts/repositories.json otherwise, so a
// public checkout still needs no configuration.
//
// This must not call inventoryCheckout: that function falls back to
// registryCheckout("inventory") when INVENTORY is unset, and calling it
// back from here would recurse.
func registryPath() string {
	if root, ok := trimmedInventory(); ok {
		return filepath.Join(root, "scripts", "repositories.json")
	}
	return filepath.Join(workDir(), "allod", "inventory", "scripts", "repositories.json")
}

// trimmedInventory reads INVENTORY and applies the trailing-slash and root
// handling that both registryPath and inventoryCheckout need, so it is
// defined once. ok is false when INVENTORY is unset or empty.
func trimmedInventory() (path string, ok bool) {
	value := os.Getenv("INVENTORY")
	if value == "" {
		return "", false
	}
	// Trimming trailing slashes turns the root into the empty string, and
	// a caller that joined it with a further path would then run against
	// the process working directory instead of /, so the root is
	// special-cased back. Not filepath.Clean: it resolves '..' lexically,
	// so a path through a symlink ('/base/link/../inventory', link ->
	// /srv/x) would name a different directory than the one the OS
	// reaches.
	trimmed := strings.TrimRight(value, "/")
	if trimmed == "" {
		return "/", true
	}
	return trimmed, true
}

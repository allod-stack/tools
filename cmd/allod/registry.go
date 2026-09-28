package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// registryEntry is the subset of a repository's registry entry this program
// reads. PreviewPort is zero when the repository is not a previewable site;
// allod/inventory's registry validation, not this program, holds a stated one
// to 1024-65535 and to one repository each.
type registryEntry struct {
	Checkout    string `json:"checkout"`
	PreviewPort int    `json:"preview_port"`
}

// registryEntries reads the whole repository registry (registryPath()), keyed
// by id, for example "allod/memory". A missing or malformed file yields no
// entries; that is not an error by itself. A caller looking a repository up by
// its checkout rather than its id iterates this.
func registryEntries() map[string]registryEntry {
	data, err := os.ReadFile(registryPath())
	if err != nil {
		return nil
	}
	var registry struct {
		Repositories map[string]registryEntry `json:"repositories"`
	}
	if json.Unmarshal(data, &registry) != nil {
		return nil
	}
	return registry.Repositories
}

// registryCheckout returns id's checkout path, relative to workDir().
func registryCheckout(id string) (checkout string, ok bool) {
	entry, found := registryEntries()[id]
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

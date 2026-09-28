package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// PreviewPort stays undecoded: as an int, one entry with a string or a fraction
// there would fail the whole file for every lookup, site or not.
type registryEntry struct {
	Checkout    string          `json:"checkout"`
	PreviewPort json.RawMessage `json:"preview_port"`
}

// Zero and ok is an absent preview_port; the range is allod/inventory's.
func previewPortValue(text string) (port int, ok bool) {
	if text = strings.TrimSpace(text); text == "" || text == "null" {
		return 0, true
	}
	port, err := strconv.Atoi(text)
	return port, err == nil && port >= 1024 && port <= 65535
}

func previewPort(id string, entry registryEntry) int {
	port, ok := previewPortValue(string(entry.PreviewPort))
	if !ok {
		die(1, "preview_port %s for %s in %s is not a whole number from 1024 to 65535",
			strings.TrimSpace(string(entry.PreviewPort)), id, registryPath())
	}
	return port
}

func vmSpecsPath() string { return filepath.Join(filepath.Dir(registryPath()), "vm-specs.json") }

// Sorted, so a refusal that lists the machines reads the same twice.
func vmsWithRepo(id string) []string {
	data, err := os.ReadFile(vmSpecsPath())
	if err != nil {
		return nil
	}
	var specs map[string]struct {
		Repos []string `json:"repos"`
	}
	if json.Unmarshal(data, &specs) != nil {
		return nil
	}
	var found []string
	for name, spec := range specs {
		if slices.Contains(spec.Repos, id) {
			found = append(found, name)
		}
	}
	slices.Sort(found)
	return found
}

// A missing or malformed registry yields no entries, which is not an error here.
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

// The checkout path is relative to workDir().
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

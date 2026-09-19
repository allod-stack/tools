package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeRegistryFixture writes a repository registry at
// <dir>/scripts/repositories.json mapping each id in entries to its
// checkout value.
func writeRegistryFixture(t *testing.T, dir string, entries map[string]string) {
	t.Helper()
	scripts := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scripts, 0755); err != nil {
		t.Fatal(err)
	}
	body := `{"repositories": {`
	first := true
	for id, checkout := range entries {
		if !first {
			body += ", "
		}
		first = false
		body += `"` + id + `": {"checkout": "` + checkout + `"}`
	}
	body += `}}`
	if err := os.WriteFile(filepath.Join(scripts, "repositories.json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestRegistryCheckoutPrefersINVENTORYOverWorkDirRegistry pins that, once
// INVENTORY is set, the registry it names is the one read, even when a
// different registry sits at the conventional <WORK_DIR>/allod/inventory
// location and maps the same ids elsewhere.
func TestRegistryCheckoutPrefersINVENTORYOverWorkDirRegistry(t *testing.T) {
	work := t.TempDir()
	writeRegistryFixture(t, filepath.Join(work, "allod", "inventory"), map[string]string{
		"secrets": "wrong-secrets",
		"deploy":  "wrong-deploy",
	})
	t.Setenv("WORK_DIR", work)

	inventory := t.TempDir()
	writeRegistryFixture(t, inventory, map[string]string{
		"secrets": "secrets",
		"deploy":  "deploy",
	})
	t.Setenv("INVENTORY", inventory)

	if checkout, ok := registryCheckout("secrets"); !ok || checkout != "secrets" {
		t.Errorf(`registryCheckout("secrets") = %q, %v, want "secrets", true`, checkout, ok)
	}
	if got := secretsCheckoutRelative(); got != "secrets" {
		t.Errorf("secretsCheckoutRelative() = %q, want %q", got, "secrets")
	}
	if got := deployCheckoutRelative(); got != "deploy" {
		t.Errorf("deployCheckoutRelative() = %q, want %q", got, "deploy")
	}
}

// TestRegistryCheckoutReadsWorkDirRegistryWhenINVENTORYUnset pins the
// public-checkout path: with no INVENTORY pointer, the registry under
// <WORK_DIR>/allod/inventory is read.
func TestRegistryCheckoutReadsWorkDirRegistryWhenINVENTORYUnset(t *testing.T) {
	work := t.TempDir()
	writeRegistryFixture(t, filepath.Join(work, "allod", "inventory"), map[string]string{
		"secrets": "from-workdir-registry",
	})
	t.Setenv("WORK_DIR", work)
	t.Setenv("INVENTORY", "")

	if checkout, ok := registryCheckout("secrets"); !ok || checkout != "from-workdir-registry" {
		t.Errorf(`registryCheckout("secrets") = %q, %v, want %q, true`, checkout, ok, "from-workdir-registry")
	}
}

// TestRegistryCheckoutINVENTORYWithNoRegistryFileDoesNotFallBackToWorkDir
// pins that an explicit INVENTORY pointer with no registry behind it is
// that pointer's problem: registryCheckout reports not-found rather than
// silently reading the <WORK_DIR>/allod/inventory registry instead, and the
// secrets/deploy relatives fall back to their conventional names.
func TestRegistryCheckoutINVENTORYWithNoRegistryFileDoesNotFallBackToWorkDir(t *testing.T) {
	work := t.TempDir()
	writeRegistryFixture(t, filepath.Join(work, "allod", "inventory"), map[string]string{
		"secrets": "would-be-wrong",
		"deploy":  "would-be-wrong",
	})
	t.Setenv("WORK_DIR", work)

	inventory := t.TempDir() // no scripts/repositories.json here
	t.Setenv("INVENTORY", inventory)

	if checkout, ok := registryCheckout("secrets"); ok || checkout != "" {
		t.Errorf(`registryCheckout("secrets") = %q, %v, want "", false`, checkout, ok)
	}
	if got := secretsCheckoutRelative(); got != "allod/secrets" {
		t.Errorf("secretsCheckoutRelative() = %q, want %q", got, "allod/secrets")
	}
	if got := deployCheckoutRelative(); got != "allod/deploy" {
		t.Errorf("deployCheckoutRelative() = %q, want %q", got, "allod/deploy")
	}
}

// TestINVENTORYTrailingSlashIsTrimmed pins the trimming itself, on a value
// only trimming produces: filepath.Join would silently absorb an untrimmed
// trailing slash before it reached a joined path like registryPath's, so
// this asserts on inventoryCheckout's bare return instead, which is exactly
// trimmedInventory's output with no INVENTORY-unset fallback in the way.
func TestINVENTORYTrailingSlashIsTrimmed(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("INVENTORY", dir+"/")
	if got := inventoryCheckout(); got != dir {
		t.Errorf("inventoryCheckout() with one trailing slash = %q, want %q", got, dir)
	}

	t.Setenv("INVENTORY", "///")
	if got := inventoryCheckout(); got != "/" {
		t.Errorf(`inventoryCheckout() with INVENTORY="///" = %q, want "/"`, got)
	}
}

// TestInventoryCheckoutFallsBackWithoutRecursion pins that, with INVENTORY
// unset, inventoryCheckout resolves the fallback registry's 'inventory'
// entry through registryCheckout rather than calling back into
// inventoryCheckout itself — a recursion that would never return.
func TestInventoryCheckoutFallsBackWithoutRecursion(t *testing.T) {
	work := t.TempDir()
	writeRegistryFixture(t, filepath.Join(work, "allod", "inventory"), map[string]string{
		"inventory": "custom-inventory-name",
	})
	t.Setenv("WORK_DIR", work)
	t.Setenv("INVENTORY", "")

	want := filepath.Join(work, "custom-inventory-name")
	if got := inventoryCheckout(); got != want {
		t.Errorf("inventoryCheckout() = %q, want %q", got, want)
	}
}

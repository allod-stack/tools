package main

import (
	"os/user"
	"testing"
)

// Cases a real repository cannot exercise: faking a reftable or sha256 repository
// through 'git config' alone makes git itself misbehave, unrelated to this gate.
func TestChangeConfigKeyAllowedExtensions(t *testing.T) {
	cases := []struct {
		key, value string
		allowed    bool
	}{
		{"extensions.refstorage", "reftable", true},
		{"extensions.objectformat", "sha256", true},
		{"extensions.worktreeconfig", "true", false},
		{"extensions.somethingelse", "true", false},
	}
	for _, c := range cases {
		got := changeConfigKeyAllowed(c.key, c.value, "agent/example")
		if got != c.allowed {
			t.Errorf("changeConfigKeyAllowed(%q, %q, ...) = %v, want %v", c.key, c.value, got, c.allowed)
		}
	}
}

// An unresolvable group id must be an error, never "no groups": requireUntrustedNixUser
// refuses on it. A gid LookupGroupId cannot resolve exercises exactly that path.
func TestCurrentUserGroupsUnresolvableGid(t *testing.T) {
	fake := &user.User{Uid: "0", Gid: "999999999", Username: "allod-test-nonexistent"}
	if _, err := currentUserGroups(fake); err == nil {
		t.Errorf("currentUserGroups with an unresolvable gid should return an error, got nil")
	}
}

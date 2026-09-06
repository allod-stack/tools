//go:build !site

package main

// The other half of the build-tag proof. site_test.go asserts that a build
// made with -tags site has the namespace; this file asserts that a build
// without it does not — not that the commands fail, but that the word means
// nothing, which is the difference between a machine that cannot deploy and a
// machine that can deploy badly.
//
// Both files are compiled by 'go test' runs that exclude each other, so the
// pair only holds if both runs happen: 'go test ./...' and
// 'go test -tags site ./...'.

import (
	"strings"
	"testing"
)

func TestSiteNamespaceIsNotRegistered(t *testing.T) {
	if entry, ok := lookupNamespace("site"); ok {
		t.Errorf("the site namespace is registered in an untagged build: %+v", entry)
	}
}

// TestSiteIsAnUnknownNamespace pins the behaviour an operator sees: 'allod
// site deploy' on a machine that does not publish sites fails exactly the way
// a typo does, and says so in the same words.
func TestSiteIsAnUnknownNamespace(t *testing.T) {
	invocations := [][]string{
		{"site"},
		{"site", "deploy"},
		{"site", "deploy", "--dry-run"},
		{"site", "config"},
		{"site", "--help"},
	}
	for _, args := range invocations {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, errText, code := runAllod(t, args...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if want := "allod: unknown command namespace: site\n"; errText != want {
				t.Errorf("stderr = %q, want %q", errText, want)
			}
			if out != "" {
				t.Errorf("stdout = %q, want empty", out)
			}
		})
	}
}

func TestUntaggedUsageDoesNotMentionSite(t *testing.T) {
	for _, args := range [][]string{{}, {"-h"}, {"--help"}} {
		out, _, _ := runAllod(t, args...)
		if strings.Contains(strings.ToLower(out), "site") {
			t.Errorf("usage for 'allod %s' mentions a namespace this build does not have\ngot: %q",
				strings.Join(args, " "), out)
		}
	}
}

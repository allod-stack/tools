package main

// The 'site' namespace's shared plumbing: everything it takes to find a site
// repository and read site.toml, plus the command table type and dispatcher
// that select one of its subcommands. None of this carries the 'site' build
// tag.
//
// 'preview' (site_preview.go) needs none of that tag's effects — it never
// touches rclone or shared hosting — which is why it lives outside it: sites
// are edited on dev machines, and those machines carry neither a hosting
// credential nor rclone. site_preview.go's init() registers the 'site'
// namespace itself and adds the 'preview' entry to siteCommands; site.go,
// which does carry the tag, extends the same namespace with 'deploy',
// 'check', and 'config' rather than registering a second one —
// registerNamespace panics on a duplicate word, so only one file may call
// it, and site_preview.go is that file.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// siteConfigName marks a site repository root, the way .git marks a git
// repository root. siteDomainKey is the only key this version reads.
const (
	siteConfigName        = "site.toml"
	siteDomainKey         = "domain"
	siteDomainPlaceholder = "<domain>"
)

// findSiteRoot walks up from start looking for site.toml, the way git walks up
// looking for .git. It returns the directory holding the file.
func findSiteRoot(start string) (string, bool) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, siteConfigName)); err == nil && info.Mode().IsRegular() {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func resolveSiteRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		die(1, "could not determine the current directory")
	}
	root, ok := findSiteRoot(wd)
	if !ok {
		fmt.Fprintf(stderr, "allod: no %s in %s or any parent directory\n", siteConfigName, wd)
		fmt.Fprintln(stderr, "allod: while resolving: the site repository root")
		fmt.Fprintf(stderr, "allod: to fix: run this from inside a site repository, or create %s at its root containing: %s = \"example.com\"\n", siteConfigName, siteDomainKey)
		exit(1)
	}
	return root
}

type siteConfig struct {
	domain string
}

// parseSiteConfig reads the subset of TOML that site.toml needs: blank lines,
// '#' comments, [table] headers, and single-line 'key = value' pairs whose
// value is a quoted string.
//
// It is deliberately lenient about everything it does not understand. A line
// it cannot interpret, and any key other than the top-level 'domain', is
// skipped rather than rejected, so a site.toml that grows keys for a later
// version of this command still deploys with an older binary. The three hard
// errors all concern the one key that is actually read: it is absent, it is
// not a quoted string, or it is set twice.
func parseSiteConfig(text string) (siteConfig, error) {
	config, table, domainSet := siteConfig{}, "", false
	for index, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if end := strings.LastIndex(line, "]"); end > 0 {
				table = strings.TrimSpace(line[1:end])
			}
			continue
		}
		equals := strings.IndexByte(line, '=')
		if equals < 0 {
			continue
		}
		// A 'domain' under a [table] is a different key entirely, and this
		// version has no table it reads, so it is skipped like any other.
		if table != "" || strings.TrimSpace(line[:equals]) != siteDomainKey {
			continue
		}
		value, ok := parseTOMLString(strings.TrimSpace(line[equals+1:]))
		if !ok {
			return siteConfig{}, fmt.Errorf("line %d: %s must be a quoted string, as in: %s = \"example.com\"", index+1, siteDomainKey, siteDomainKey)
		}
		if domainSet {
			return siteConfig{}, fmt.Errorf("line %d: %s is set more than once", index+1, siteDomainKey)
		}
		config.domain, domainSet = value, true
	}
	if !domainSet {
		return siteConfig{}, fmt.Errorf("no %s key", siteDomainKey)
	}
	return config, nil
}

// parseTOMLString reads one TOML string value: a basic string in double quotes
// with the common backslash escapes, or a literal string in single quotes with
// none. Whatever follows the closing quote must be blank or a comment.
// Multi-line strings are not accepted; site.toml has no use for one.
func parseTOMLString(value string) (string, bool) {
	if len(value) < 2 || (value[0] != '"' && value[0] != '\'') {
		return "", false
	}
	quote := value[0]
	var out strings.Builder
	for index := 1; index < len(value); index++ {
		character := value[index]
		if quote == '"' && character == '\\' {
			index++
			if index >= len(value) {
				return "", false
			}
			switch value[index] {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case 'r':
				out.WriteByte('\r')
			case '"':
				out.WriteByte('"')
			case '\\':
				out.WriteByte('\\')
			default:
				return "", false
			}
			continue
		}
		if character == quote {
			rest := strings.TrimSpace(value[index+1:])
			if rest != "" && !strings.HasPrefix(rest, "#") {
				return "", false
			}
			return out.String(), true
		}
		out.WriteByte(character)
	}
	return "", false
}

// validDomain accepts a plain hostname and nothing else. This is a safety
// check, not a politeness one: the domain is interpolated into the rclone
// destination, so a '/' or a '..' in it would aim the sync at another site's
// docroot, and every docroot on the host belongs to the same account.
func validDomain(domain string) bool {
	if domain == "" || len(domain) > 253 || !strings.Contains(domain, ".") {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func loadSiteConfig(root string) siteConfig {
	path := filepath.Join(root, siteConfigName)
	data, err := os.ReadFile(path)
	if err != nil {
		die(1, "could not read %s", path)
	}
	config, err := parseSiteConfig(string(data))
	if err != nil {
		die(1, "invalid %s: %s", path, err)
	}
	if !validDomain(config.domain) {
		die(1, "invalid %s '%s' in %s; expected a hostname such as \"example.com\"", siteDomainKey, config.domain, path)
	}
	return config
}

// siteCommand is one command inside the 'site' namespace: the word that
// selects it from 'allod site <command>', its one-line summary for the
// Commands: list, the Usage: lines it contributes, its detailed prose, and
// the function that runs it.
type siteCommand struct {
	name    string
	summary string
	usage   []string
	detail  string
	run     func(args []string)
}

// siteCommands is the site namespace's own dispatch table, parallel to
// namespaces in main.go: a build carries exactly the entries this slice
// holds once every init() function has run. It is declared with no
// initializer, and every entry — 'preview' included — is added from an
// init() rather than a var literal: a siteCommand's run field holds a
// function (sitePreview, siteDeploy, ...) whose body calls siteUsageText,
// which reads siteCommands, so a var initializer that built a siteCommand
// value directly would be a compile-time initialization cycle. init()
// function bodies are not part of that dependency analysis, only var
// initializer expressions are, which is why population moves there instead.
// site_preview.go's init() prepends 'preview' rather than appending it, so it
// stays first in the table regardless of whether it or site.go's init() runs
// first.
var siteCommands []siteCommand

// siteMain dispatches 'allod site <command>' to whichever entry in
// siteCommands matches, so a build advertises and runs exactly the commands
// it carries. On an untagged build that is 'preview' alone: 'deploy',
// 'check', and 'config' fall through to the same "unknown site command" a
// typo would, because in that build they are exactly as absent as a typo.
func siteMain(args []string) {
	if len(args) == 0 {
		fmt.Fprint(stderr, siteUsageText())
		exit(1)
	}
	command, rest := args[0], args[1:]
	switch command {
	case "-h", "--help":
		fmt.Fprint(stdout, siteUsageText())
		return
	}
	for _, entry := range siteCommands {
		if entry.name == command {
			entry.run(rest)
			return
		}
	}
	die(1, "unknown site command: %s", command)
}

// siteUsageText renders 'allod site' usage from siteCommands, so a build
// advertises exactly the commands it carries and no others. Every command's
// '-h'/'--help' prints this same whole text, which is the existing shape:
// there was never a per-command help subset before this command table
// existed either.
func siteUsageText() string {
	var text strings.Builder
	text.WriteString("Usage:\n")
	for _, entry := range siteCommands {
		for _, line := range entry.usage {
			fmt.Fprintf(&text, "  %s\n", line)
		}
	}
	text.WriteString("\nCommands:\n")
	for _, entry := range siteCommands {
		fmt.Fprintf(&text, "  %-8s %s\n", entry.name, entry.summary)
	}
	for _, entry := range siteCommands {
		text.WriteString("\n")
		text.WriteString(entry.detail)
	}
	return text.String()
}

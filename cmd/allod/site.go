package main

// The site namespace deploys a static site to shared hosting.
//
// The whole point of the command is centralisation. One hosting account owns
// every docroot on the server, and the exclusion list that keeps a deploy from
// destroying /.well-known — where certificate renewal writes its challenge —
// is one line long and easy to omit. If every site repository carried its own
// copy of that list, one repository would eventually be missing it, and the
// failure would arrive weeks later as an expired certificate rather than as a
// broken deploy. So the list lives here, in the command, and is written to a
// fresh temporary file on every run.
//
// The same reasoning explains why there is no way to name the target: see
// siteDeploy.

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// siteConfigName marks a site repository root, the way .git marks a git
// repository root. siteDomainKey is the only key this version reads.
const (
	siteConfigName = "site.toml"
	siteDomainKey  = "domain"
)

// deployFilterRules is the exclusion list, in rclone --filter-from syntax. It
// belongs to this command and is deliberately not configurable per site.
//
//   - /.well-known/** holds ACME challenges. Deleting it breaks certificate
//     renewal silently, weeks after the deploy that caused it.
//   - /.htaccess is written by the hosting control panel, not by the build.
//   - /stats/** and /cgi-bin/** are created and owned by the host.
//
// Excluding a path also protects it at the destination: rclone sync only
// deletes what the filter admits, so these paths survive every deploy.
var deployFilterRules = []string{
	"- /.well-known/**",
	"- /.htaccess",
	"- /stats/**",
	"- /cgi-bin/**",
}

// siteVerifyTimeout bounds the post-deploy HTTPS check.
const siteVerifyTimeout = 15 * time.Second

// Exit codes beyond the generic 1: a failed nix build or rclone run exits with
// the status of the child process, and a deploy that syncs but then fails its
// HTTPS check exits with siteVerifyExit so automation can tell "the deploy did
// not happen" from "the deploy happened and the site is not answering".
const siteVerifyExit = 7

// Test seams. Every effect this command has outside the process goes through
// one of these three, so the tests can drive the whole command without a nix
// daemon, a configured rclone remote, or a network.
var (
	siteBuild  = nixBuild
	siteSync   = rcloneSync
	siteVerify = probeSite
)

func siteMain(args []string) {
	if len(args) == 0 {
		fmt.Fprint(stderr, siteUsageText)
		exit(1)
	}
	command, args := args[0], args[1:]
	switch command {
	case "deploy":
		siteDeploy(args)
	case "-h", "--help":
		fmt.Fprint(stdout, siteUsageText)
	default:
		die(1, "unknown site command: %s", command)
	}
}

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

func deployFilterText() string {
	return strings.Join(deployFilterRules, "\n") + "\n"
}

// writeDeployFilter materialises the exclusion list for one run. The caller
// removes it; die() and exit() unwind through defers, so a failure part way
// through the deploy still cleans it up.
func writeDeployFilter() string {
	file, err := os.CreateTemp("", "allod-site-filter-")
	if err != nil {
		die(1, "could not create temporary filter file")
	}
	name := file.Name()
	if _, err := file.WriteString(deployFilterText()); err != nil || file.Close() != nil {
		os.Remove(name)
		die(1, "could not write temporary filter file")
	}
	return name
}

// nixBuild builds the site and returns its store path. Stdout is captured
// because --print-out-paths writes the answer there; stderr is passed through
// so a failing build reports itself in full.
func nixBuild(root string) (string, int) {
	var out bytes.Buffer
	cmd := exec.Command("nix", "build", "--no-link", "--print-out-paths")
	cmd.Dir = root
	cmd.Stdout = &out
	cmd.Stderr = stderr
	status := commandExitCode(cmd.Run())
	return strings.TrimRight(out.String(), "\n"), status
}

func rcloneSync(args []string) int {
	return runCommand("", nil, stdout, stderr, "rclone", args...)
}

// probeSite reports the status of https://<domain>/ itself. Redirects are not
// followed on purpose: a docroot that answers 301 has not been deployed to the
// place the check is asking about, and following the hop would report the 200
// of wherever it lands.
func probeSite(url string) (int, error) {
	client := &http.Client{
		Timeout: siteVerifyTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Get(url)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	return response.StatusCode, nil
}

func siteDeploy(args []string) {
	dryRun := false
	for len(args) > 0 {
		switch args[0] {
		case "--dry-run":
			dryRun, args = true, args[1:]
		case "-h", "--help":
			fmt.Fprint(stdout, siteUsageText)
			return
		default:
			// There is deliberately no option or argument that names a
			// domain, a docroot, or a remote. The destination comes from
			// site.toml and from nothing else, because one hosting account
			// owns every docroot on the server: anything that could redirect
			// this sync could also delete a sibling site.
			if strings.HasPrefix(args[0], "-") {
				die(1, "unknown option for site deploy: %s", args[0])
			}
			die(1, "unexpected argument for site deploy: %s", args[0])
		}
	}

	root := resolveSiteRoot()
	config := loadSiteConfig(root)
	if _, err := exec.LookPath("nix"); err != nil {
		die(1, "'nix' not found on PATH")
	}
	if _, err := exec.LookPath("rclone"); err != nil {
		die(1, "'rclone' not found on PATH")
	}

	storePath, status := siteBuild(root)
	if status != 0 {
		die(status, "nix build failed in %s", root)
	}
	if storePath == "" {
		die(1, "nix build printed no output path")
	}
	if strings.Contains(storePath, "\n") {
		die(1, "nix build printed more than one output path; the site must build to exactly one")
	}
	// rclone reads 'name:path' as a remote, so a path holding a colon would
	// silently become a different destination.
	if !filepath.IsAbs(storePath) || strings.Contains(storePath, ":") {
		die(1, "nix build printed an unusable output path: %s", storePath)
	}

	docroot := "shared:domains/" + config.domain + "/public_html"
	filter := writeDeployFilter()
	defer os.Remove(filter)

	fmt.Fprintf(stdout, "Domain: %s\nSource: %s\nTarget: %s\n", config.domain, storePath, docroot)

	syncArgs := []string{
		"sync", storePath, docroot,
		"--filter-from", filter,
		"--backup-dir", "shared:deploy-trash/" + config.domain,
		"--verbose",
	}
	if dryRun {
		syncArgs = append(syncArgs, "--dry-run")
	}
	if status := siteSync(syncArgs); status != 0 {
		// A dry run writes nothing, so reporting it as a possibly partial
		// update sends the reader looking for damage that cannot exist.
		if dryRun {
			die(status, "rclone dry run failed; %s was not modified", docroot)
		}
		die(status, "rclone sync failed; %s may be partially updated", docroot)
	}
	if dryRun {
		fmt.Fprintf(stdout, "Dry run: %s was not modified\n", docroot)
		return
	}

	url := "https://" + config.domain + "/"
	code, err := siteVerify(url)
	if err != nil {
		die(siteVerifyExit, "deployed, but %s could not be reached: %s", url, err)
	}
	if code != 200 {
		die(siteVerifyExit, "deployed, but %s returned %d, not 200", url, code)
	}
	fmt.Fprintf(stdout, "Verified: %s returned %d\n", url, code)
}

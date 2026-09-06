//go:build site

package main

// The site namespace deploys a static site to shared hosting.
//
// The whole point of the command is centralisation. One hosting account owns
// every docroot on the server, and the exclusion list that keeps a deploy from
// destroying host-owned paths is easy to omit. If every site repository
// carried its own copy of that list, one repository would eventually be
// missing it, and the failure could arrive weeks later as an expired
// certificate rather than as a broken deploy. So hosting layouts live here as
// named profiles selected once by the deployment, not in each site repository.
//
// The same reasoning explains why there is no way to name the target: see
// siteDeploy.
//
// The whole namespace is behind the 'site' build tag. A machine that publishes
// no site has no rclone remote and no business carrying a command that syncs
// to one, so it does not carry it: the init() below is the only thing that
// puts 'site' in the dispatch table, and an untagged build never compiles this
// file. There, 'allod site' is an unknown namespace — which is true, and is
// what an absent capability should look like. A command that is present and
// permanently broken says something false about the machine.

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
	siteConfigName        = "site.toml"
	siteDomainKey         = "domain"
	siteDomainPlaceholder = "<domain>"
)

// siteRemoteName is the rclone remote every deploy targets. It is configured
// once per machine — by 'allod site config', or by hand — and nothing in a
// site repository can name a different one.
const siteRemoteName = "shared"

// The hosting profile is deployment configuration. A Nix wrapper can set the
// environment variable once for every invocation on that machine; leaving it
// unset keeps the original DirectAdmin behavior.
const (
	siteHostingProfileEnv     = "ALLOD_SITE_HOSTING_PROFILE"
	defaultSiteHostingProfile = "directadmin"
)

// siteHostingProfile holds only the two facts that differ between the shared
// hosting deployments this command serves. The rclone remote and backend are
// intentionally not abstracted: every profile still deploys through 'shared'.
type siteHostingProfile struct {
	name              string
	docrootPattern    string
	deployFilterRules []string
}

// siteHostingProfiles is the central source of truth for hosting layouts. A
// site.toml cannot add to or override it.
//
// DirectAdmin is the exact original behavior:
//   - /.well-known/** holds ACME challenges. Deleting it breaks certificate
//     renewal silently, weeks after the deploy that caused it.
//   - /.htaccess is written by the hosting control panel, not by the build.
//   - /stats/** and /cgi-bin/** are created and owned by the host.
//
// The public-html layout is the concrete second deployment: its docroots sit
// below public_html and ACME is served elsewhere, so it has nothing to exclude.
var siteHostingProfiles = []siteHostingProfile{
	{
		name:           "directadmin",
		docrootPattern: "domains/<domain>/public_html",
		deployFilterRules: []string{
			"- /.well-known/**",
			"- /.htaccess",
			"- /stats/**",
			"- /cgi-bin/**",
		},
	},
	{
		name:           "public-html",
		docrootPattern: "public_html/<domain>",
	},
}

func validSiteHostingProfileName(name string) bool {
	if name == "" || name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

// selectedSiteHostingProfile resolves deployment configuration before any
// remote check or build. An invalid value must not fall back to DirectAdmin:
// that would aim a destructive sync at a plausible but wrong directory.
func selectedSiteHostingProfile() siteHostingProfile {
	name := os.Getenv(siteHostingProfileEnv)
	if name == "" {
		name = defaultSiteHostingProfile
	}
	if !validSiteHostingProfileName(name) {
		die(1, "invalid hosting profile %q in %s; expected directadmin or public-html", name, siteHostingProfileEnv)
		return siteHostingProfile{}
	}
	for _, profile := range siteHostingProfiles {
		if profile.name == name {
			return profile
		}
	}
	die(1, "unknown hosting profile %q in %s; expected directadmin or public-html", name, siteHostingProfileEnv)
	return siteHostingProfile{}
}

func (profile siteHostingProfile) docroot(domain string) string {
	return siteRemoteName + ":" + strings.Replace(profile.docrootPattern, siteDomainPlaceholder, domain, 1)
}

// siteVerifyTimeout bounds the post-deploy HTTPS check.
const siteVerifyTimeout = 15 * time.Second

// Exit codes beyond the generic 1: a failed nix build or rclone run exits with
// the status of the child process, and a deploy that syncs but then fails its
// HTTPS check exits with siteVerifyExit so automation can tell "the deploy did
// not happen" from "the deploy happened and the site is not answering".
const siteVerifyExit = 7

// Test seams. Every effect this command has outside the process goes through
// one of these four, so the tests can drive the whole command without a nix
// daemon, a configured rclone remote, or a network. 'site config' has two
// seams of its own; see site_config.go.
var (
	siteRemotes = rcloneListRemotes
	siteBuild   = nixBuild
	siteSync    = rcloneSync
	siteVerify  = probeSite
)

// init is what makes the namespace exist, and it runs only in a build that
// asked for this file with -tags site.
func init() {
	registerNamespace(namespace{
		name:    "site",
		summary: "Deploy a static site to shared hosting (deploy, config)",
		main:    siteMain,
	})
}

func siteMain(args []string) {
	if len(args) == 0 {
		fmt.Fprint(stderr, siteUsageText)
		exit(1)
	}
	command, args := args[0], args[1:]
	switch command {
	case "deploy":
		siteDeploy(args)
	case "config":
		siteConfigure(args)
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

func deployFilterText(rules []string) string {
	if len(rules) == 0 {
		return ""
	}
	return strings.Join(rules, "\n") + "\n"
}

// writeDeployFilter materialises the exclusion list for one run. The caller
// removes it; die() and exit() unwind through defers, so a failure part way
// through the deploy still cleans it up.
func writeDeployFilter(rules []string) string {
	file, err := os.CreateTemp("", "allod-site-filter-")
	if err != nil {
		die(1, "could not create temporary filter file")
	}
	name := file.Name()
	if _, err := file.WriteString(deployFilterText(rules)); err != nil || file.Close() != nil {
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

// rcloneListRemotes returns what 'rclone listremotes' prints: one configured
// remote per line, each with a trailing colon. Stderr is passed through so an
// unreadable configuration file explains itself in rclone's own words.
func rcloneListRemotes() (string, int) {
	var out bytes.Buffer
	cmd := exec.Command("rclone", "listremotes")
	cmd.Stdout = &out
	cmd.Stderr = stderr
	// The run has to finish before the buffer is read. Returning both in one
	// statement would read an empty buffer: the operands of a return are
	// evaluated left to right, so out.String() would run before cmd.Run().
	status := commandExitCode(cmd.Run())
	return out.String(), status
}

// hasRemote reports whether listing names remote. The trailing colon is
// optional on the way in: current rclone prints 'shared:' and older versions
// printed the bare name. This answer decides whether a deploy runs at all, so
// it accepts both rather than reading a listing it half understands as "not
// configured".
func hasRemote(listing, remote string) bool {
	for _, line := range strings.Split(listing, "\n") {
		if strings.TrimSuffix(strings.TrimSpace(line), ":") == remote {
			return true
		}
	}
	return false
}

// requireSiteRemote fails before the build rather than after it. The remote is
// the one thing a deploy needs that the site repository cannot supply, and an
// unconfigured machine used to report that fact only once the whole site had
// been built — as an rclone complaint about a missing config section, minutes
// after the operator asked for a deploy and with nothing to show for the wait.
func requireSiteRemote(profile siteHostingProfile) {
	listing, status := siteRemotes()
	if status != 0 {
		die(status, "'rclone listremotes' failed; the rclone configuration on this machine is unreadable")
	}
	if hasRemote(listing, siteRemoteName) {
		return
	}
	fmt.Fprintf(stderr, "allod: no '%s' rclone remote is configured on this machine\n", siteRemoteName)
	fmt.Fprintf(stderr, "allod: while resolving: the deploy destination %s\n", profile.docroot(siteDomainPlaceholder))
	fmt.Fprintln(stderr, "allod: to fix: run 'allod site config', which creates the remote from a host, a user, and a password")
	exit(1)
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

	profile := selectedSiteHostingProfile()
	root := resolveSiteRoot()
	config := loadSiteConfig(root)
	if _, err := exec.LookPath("nix"); err != nil {
		die(1, "'nix' not found on PATH")
	}
	if _, err := exec.LookPath("rclone"); err != nil {
		die(1, "'rclone' not found on PATH")
	}
	// Before the build, not after it: building a site is minutes of work, and
	// none of it is any use without somewhere to send the result.
	requireSiteRemote(profile)

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

	docroot := profile.docroot(config.domain)

	fmt.Fprintf(stdout, "Domain: %s\nSource: %s\nTarget: %s\n", config.domain, storePath, docroot)

	syncArgs := []string{
		"sync", storePath, docroot,
	}
	if len(profile.deployFilterRules) > 0 {
		filter := writeDeployFilter(profile.deployFilterRules)
		defer os.Remove(filter)
		syncArgs = append(syncArgs, "--filter-from", filter)
	}
	syncArgs = append(syncArgs,
		"--backup-dir", siteRemoteName+":deploy-trash/"+config.domain,
		"--verbose",
	)
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

const siteUsageText = `Usage:
  allod site deploy [--dry-run]
  allod site config [--force]

Commands:
  deploy   Build the site repo and sync the result to its shared-hosting docroot
  config   Create the 'shared' rclone remote every deploy goes through

'deploy' walks up from the current directory to the site.toml that marks the
site repository root, builds that repo with 'nix build --no-link
--print-out-paths', and syncs the resulting store path through the 'shared'
rclone remote. Deploy never handles a credential, and checks that the remote
exists before it starts the build rather than discovering it afterwards.

The deployment selects one central hosting layout with
ALLOD_SITE_HOSTING_PROFILE. An unset or empty value selects 'directadmin',
whose docroot is shared:domains/<domain>/public_html and whose exclusion list
is unchanged. 'public-html' selects shared:public_html/<domain> and no
exclusions, for hosting where ACME is served elsewhere. Any other value fails
before the remote check or build. This is per-machine deployment configuration,
normally set by the wrapper that installs allod; it does not belong in a site
repository.

Each profile's exclusion filter belongs to this command rather than to the site
repo. A non-empty filter is written to a temporary file per run and passed as
--filter-from, so every site on the deployment gets the same list. Replaced and
deleted files are moved to shared:deploy-trash/<domain> rather than destroyed.

site.toml still has exactly one key and cannot select the hosting profile:

  domain = "example.com"

Unknown keys are ignored, so a file written for a later version still deploys.

'--dry-run' passes --dry-run to rclone: the build still runs and rclone reports
the changes it would make, but the docroot is untouched and the HTTPS check is
skipped. Without it, deploy checks that https://<domain>/ answers 200 and exits
7 if it does not, which distinguishes a site that did not deploy from one that
deployed and is not serving.

'config' asks for an FTP host, user, and password on the terminal and writes
the 'shared' remote to rclone's own configuration file, so nobody has to drive
'rclone config' by hand or invent the stanza from memory. The password is not
echoed as it is typed, never appears in a command line, and is stored the only
way rclone accepts a stored password: obscured, by rclone itself. An existing
'shared' remote is left alone unless --force is passed.
`

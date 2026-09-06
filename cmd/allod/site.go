//go:build site

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
	siteConfigName = "site.toml"
	siteDomainKey  = "domain"
)

// siteRemoteName is the rclone remote every deploy targets. It is configured
// once per machine — by 'allod site config', or by hand — and nothing in a
// site repository can name a different one.
const siteRemoteName = "shared"

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
// one of these four, so the tests can drive the whole command without a nix
// daemon, a configured rclone remote, or a network. 'site config' has two
// seams of its own; see site_config.go.
var (
	siteRemoteCheck = rcloneSiteRemoteCheck
	siteBuild       = nixBuild
	siteSync        = rcloneSync
	siteVerify      = probeSite
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

func rcloneSync(configPath string, args []string) int {
	return runCommand("", nil, stdout, stderr, "rclone", rcloneArgs(configPath, args...)...)
}

// siteRemoteProblem is the operator action selected from an rclone probe. The
// distinction matters because all four expected failures have different
// remedies, while rclone reports each one as a failed filesystem creation.
type siteRemoteProblem uint8

const (
	siteRemoteReady siteRemoteProblem = iota
	siteRemoteMissing
	siteRemoteConfigUnreadable
	siteRemoteCredentialRejected
	siteRemoteHostUnreachable
	siteRemoteUnknown
)

type siteRemoteCheckResult struct {
	problem siteRemoteProblem
	status  int
}

// rcloneSiteRemoteCheck opens the remote rather than merely asking rclone for
// its name. 'lsd shared:' logs in and reads the hosting account's root without
// changing it, so one successful call proves the stanza, credential, host and
// service all work. Output is captured because the CLI owns the diagnosis:
// rclone deliberately is not installed on the operator's PATH.
func rcloneSiteRemoteCheck(configPath string) siteRemoteCheckResult {
	var diagnostic bytes.Buffer
	cmd := exec.Command("rclone", rcloneArgs(configPath, "lsd", siteRemoteName+":")...)
	cmd.Stdout = io.Discard
	cmd.Stderr = &diagnostic
	status := commandExitCode(cmd.Run())
	if status == 0 {
		return siteRemoteCheckResult{problem: siteRemoteReady}
	}
	return siteRemoteCheckResult{
		problem: classifySiteRemoteProblem(diagnostic.String()),
		status:  status,
	}
}

// classifySiteRemoteProblem translates the stable substance of rclone's FTP
// errors rather than exposing its timestamped, implementation-shaped log
// lines. Authentication is checked before reachability because rclone wraps a
// rejected login in "failed to make FTP connection", just as it does a DNS or
// socket failure.
func classifySiteRemoteProblem(diagnostic string) siteRemoteProblem {
	message := strings.ToLower(diagnostic)
	containsAny := func(parts ...string) bool {
		for _, part := range parts {
			if strings.Contains(message, part) {
				return true
			}
		}
		return false
	}

	if containsAny("didn't find section in config file", "did not find section in config file") {
		return siteRemoteMissing
	}
	if containsAny(
		"failed to load config file",
		"failed to read config file",
		"error reading config file",
		"could not read config file",
		"cannot read config file",
	) {
		return siteRemoteConfigUnreadable
	}
	if containsAny(
		" 530 ",
		"530 login",
		"530 not logged in",
		"authentication failed",
		"authentication rejected",
		"login failed",
		"login incorrect",
	) {
		return siteRemoteCredentialRejected
	}
	if containsAny(
		"dial tcp",
		"temporary failure in name resolution",
		"connection refused",
		"connection reset by peer",
		"network is unreachable",
		"no route to host",
		"no such host",
		"i/o timeout",
		"operation timed out",
	) {
		return siteRemoteHostUnreachable
	}
	return siteRemoteUnknown
}

// reportSiteRemoteFailure is shared CLI wording rather than deploy wording so
// a command that checks the credential without deploying can use the same
// probe and the same diagnoses. Callers retain control of success output and
// exit flow.
func reportSiteRemoteFailure(result siteRemoteCheckResult) {
	switch result.problem {
	case siteRemoteMissing:
		fmt.Fprintf(stderr, "allod: no '%s' rclone remote is configured; run 'allod site config' to create it\n", siteRemoteName)
	case siteRemoteConfigUnreadable:
		fmt.Fprintln(stderr, "allod: the rclone configuration is unreadable; check its path and permissions")
	case siteRemoteCredentialRejected:
		fmt.Fprintf(stderr, "allod: the stored username or password for '%s' was rejected; run 'allod site config --force' to replace it\n", siteRemoteName)
	case siteRemoteHostUnreachable:
		fmt.Fprintf(stderr, "allod: the host for '%s' is unreachable; check its address, the network connection, and the hosting service\n", siteRemoteName)
	default:
		fmt.Fprintf(stderr, "allod: the '%s' hosting remote could not be checked (rclone exited %d)\n", siteRemoteName, result.status)
	}
}

// requireSiteRemote fails before the build rather than after it. The remote is
// the one thing a deploy needs that the site repository cannot supply, and a
// bad credential used to report itself only once the whole site had been
// built, minutes after the operator asked for a deploy.
func requireSiteRemote(configPath string) {
	result := siteRemoteCheck(configPath)
	if result.problem == siteRemoteReady {
		return
	}
	reportSiteRemoteFailure(result)
	if result.status == 0 {
		exit(1)
	}
	exit(result.status)
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
	var configSelection rcloneConfigSelection
	for len(args) > 0 {
		if rest, consumed := configSelection.consume(args, "deploy"); consumed {
			args = rest
			continue
		}
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
	if err := configSelection.requireReadable(); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(stderr, "allod: named rclone configuration does not exist: %q\n", configSelection.path)
			fmt.Fprintln(stderr, "allod: while resolving: the hosting credential selected by --config")
			fmt.Fprintln(stderr, "allod: to fix: materialise the credential at that path, or omit --config to use rclone's configuration")
			exit(1)
		}
		die(1, "named rclone configuration is not readable: %q: %s", configSelection.path, err)
	}
	configPath := configSelection.configPath()
	// Before the build, not after it: building a site is minutes of work, and
	// none of it is any use without somewhere to send the result.
	requireSiteRemote(configPath)

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

	docroot := siteRemoteName + ":domains/" + config.domain + "/public_html"
	filter := writeDeployFilter()
	defer os.Remove(filter)

	fmt.Fprintf(stdout, "Domain: %s\nSource: %s\nTarget: %s\n", config.domain, storePath, docroot)

	syncArgs := []string{
		"sync", storePath, docroot,
		"--filter-from", filter,
		"--backup-dir", siteRemoteName + ":deploy-trash/" + config.domain,
		"--verbose",
	}
	if dryRun {
		syncArgs = append(syncArgs, "--dry-run")
	}
	if status := siteSync(configPath, syncArgs); status != 0 {
		// A dry run writes nothing, so reporting it as a possibly partial
		// update sends the reader looking for damage that cannot exist.
		if dryRun {
			die(status, "rclone dry run failed; %s was not modified", docroot)
		}
		die(status, "rclone sync failed; deployment to %s did not complete", docroot)
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
  allod site deploy [--config <path>] [--dry-run]
  allod site config [--config <path>] [--force]

Commands:
  deploy   Build the site repo and sync the result to its shared-hosting docroot
  config   Create the 'shared' rclone remote every deploy goes through

'deploy' walks up from the current directory to the site.toml that marks the
site repository root, builds that repo with 'nix build --no-link
--print-out-paths', and syncs the resulting store path to
shared:domains/<domain>/public_html. 'shared' is an rclone remote configured
once per machine; deploy never handles a credential, and checks that the remote
can authenticate before it starts the build rather than discovering a rejected
login afterwards.

'--config <path>' selects one rclone configuration file for either command. An
explicit path takes precedence over RCLONE_CONFIG, rclone's reported path,
XDG_CONFIG_HOME, and HOME. Deploy requires the named path to be an existing,
readable regular file before it builds, and passes it to every rclone call.
Config may create a missing named file and writes it at mode 0600. Without the
flag, deploy leaves resolution entirely to rclone and config keeps asking
rclone for its configuration file before falling back to the XDG/HOME default.
The hosting config is machine-wide; it does not belong in site.toml or a site
repository.

The docroot is derived from the 'domain' key in site.toml and from nothing
else. No flag, argument, or environment variable can point a deploy at another
site, because one hosting account owns every docroot on the server and a
redirected sync would delete a sibling site.

The exclusion filter belongs to this command rather than to the site repo. It
is written to a temporary file per run and passed as --filter-from, so every
site gets the same list and no repo can quietly lose the /.well-known/** entry
that certificate renewal depends on. Replaced and deleted files are moved to
shared:deploy-trash/<domain> rather than destroyed.

site.toml today has exactly one key:

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

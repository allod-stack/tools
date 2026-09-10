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
// 'deploy', 'check', and 'config' are behind the 'site' build tag; 'preview'
// is not, and lives in site_preview.go along with the namespace registration
// itself — see that file and site_common.go. A machine that publishes no
// site has no rclone remote and no business carrying a command that syncs to
// one, so it does not carry these three: this file's init() only appends
// them to the command table site_common.go already registered, and an
// untagged build never compiles this file, so 'allod site deploy' there
// falls through to "unknown site command" — which is true, and is what an
// absent capability should look like. A command that is present and
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
	"unicode"
	"unicode/utf8"
)

// siteRemoteName is the rclone remote every deploy targets. It is configured
// once per machine — by 'allod site config', or by hand — and nothing in a
// site repository can name a different one.
const siteRemoteName = "shared"

const defaultSiteHostingProfile = "directadmin"

// siteHostingProfileName is deployment-owned build configuration. A
// site-enabled build can replace this string with:
//
//	-X main.siteHostingProfileName=public-html
//
// It deliberately has no flag or environment override: the repository being
// deployed must not be able to redirect a destructive sync.
var siteHostingProfileName = defaultSiteHostingProfile

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

func printableSingleLine(value string) bool {
	if value == "" || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

func validSiteDocrootPattern(pattern string) bool {
	if !printableSingleLine(pattern) || strings.HasPrefix(pattern, "/") ||
		strings.Count(pattern, siteDomainPlaceholder) != 1 {
		return false
	}
	for _, segment := range strings.Split(pattern, "/") {
		if segment == siteDomainPlaceholder {
			continue
		}
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, r := range segment {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
				continue
			}
			return false
		}
	}
	return true
}

func validDeployExclusionRule(rule string) bool {
	return printableSingleLine(rule) && strings.TrimSpace(rule) == rule &&
		strings.HasPrefix(rule, "- /") && len(rule) > len("- /")
}

// validateSiteHostingProfiles treats this source-owned table like input
// because one malformed row can redirect or broaden a destructive sync. The
// check runs before the selected profile reaches a remote check or build.
func validateSiteHostingProfiles(profiles []siteHostingProfile) error {
	if len(profiles) == 0 {
		return fmt.Errorf("no profiles are defined")
	}
	seen := make(map[string]bool, len(profiles))
	for _, profile := range profiles {
		if !validSiteHostingProfileName(profile.name) {
			return fmt.Errorf("invalid profile name %q", profile.name)
		}
		if seen[profile.name] {
			return fmt.Errorf("profile name %q is defined more than once", profile.name)
		}
		seen[profile.name] = true
		if !validSiteDocrootPattern(profile.docrootPattern) {
			return fmt.Errorf("profile %q has an unsafe docroot pattern %q", profile.name, profile.docrootPattern)
		}
		for _, rule := range profile.deployFilterRules {
			if !validDeployExclusionRule(rule) {
				return fmt.Errorf("profile %q has an invalid exclusion rule %q", profile.name, rule)
			}
		}
	}
	if !seen[defaultSiteHostingProfile] {
		return fmt.Errorf("default profile %q is not defined", defaultSiteHostingProfile)
	}
	return nil
}

func siteHostingProfileChoices() string {
	names := make([]string, 0, len(siteHostingProfiles))
	for _, profile := range siteHostingProfiles {
		names = append(names, profile.name)
	}
	return strings.Join(names, " or ")
}

// selectedSiteHostingProfile resolves immutable deployment configuration
// before any remote check or build. An invalid build value or table must not
// fall back to DirectAdmin: that would aim a destructive sync at a plausible
// but wrong directory.
func selectedSiteHostingProfile() siteHostingProfile {
	if err := validateSiteHostingProfiles(siteHostingProfiles); err != nil {
		die(1, "invalid built-in hosting profile table: %s", err)
		return siteHostingProfile{}
	}
	name := siteHostingProfileName
	if !validSiteHostingProfileName(name) {
		die(1, "invalid build-time hosting profile %q; expected %s", name, siteHostingProfileChoices())
		return siteHostingProfile{}
	}
	for _, profile := range siteHostingProfiles {
		if profile.name == name {
			return profile
		}
	}
	die(1, "unknown build-time hosting profile %q; expected %s", name, siteHostingProfileChoices())
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
// daemon, a configured rclone remote, or a network. 'site config' has its own
// command and terminal seams; see site_config.go.
var (
	siteRemoteCheck = rcloneSiteRemoteCheck
	siteBuild       = nixBuild
	siteSync        = rcloneSync
	siteVerify      = probeSite
)

// init extends the 'site' namespace site_common.go already registered,
// adding the three commands this build's tag opts it into. It appends to
// siteCommands rather than replacing it, and never calls registerNamespace:
// that would panic on the duplicate word, and site_common.go's init() is the
// only one allowed to call it.
func init() {
	siteCommands = append(siteCommands,
		siteCommand{
			name:    "deploy",
			summary: "Build the site repo and sync the result to its shared-hosting docroot",
			usage:   []string{"allod site deploy [--config <path>] [--dry-run]"},
			detail:  siteDeployDetail,
			run:     siteDeploy,
		},
		siteCommand{
			name:    "check",
			summary: "Verify the stored hosting credential without deploying",
			usage:   []string{"allod site check [--config <path>]"},
			detail:  siteCheckDetail,
			run:     siteCheck,
		},
		siteCommand{
			name:    "config",
			summary: "Create, inspect, update, or replace the 'shared' rclone remote",
			usage: []string{
				"allod site config [--config <path>] [--force]",
				"allod site config show [--config <path>]",
				"allod site config update {host|user|password} [--config <path>]",
				"allod site config replace [--config <path>]",
			},
			detail: siteConfigDetail,
			run:    siteConfigure,
		},
	)
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
// probe and the same diagnoses. When a caller selected a config path, every
// config remedy names that path instead of redirecting the operator to
// rclone's default. %q keeps even an unusual path on one safe output line.
func reportSiteRemoteFailure(result siteRemoteCheckResult, configPath string) {
	switch result.problem {
	case siteRemoteMissing:
		if configPath == "" {
			fmt.Fprintf(stderr, "allod: no '%s' rclone remote is configured; run 'allod site config' to create it\n", siteRemoteName)
		} else {
			fmt.Fprintf(stderr, "allod: no '%s' rclone remote is configured in %q; create it in that selected configuration\n", siteRemoteName, configPath)
		}
	case siteRemoteConfigUnreadable:
		if configPath == "" {
			fmt.Fprintln(stderr, "allod: the rclone configuration is unreadable; check its path and permissions")
		} else {
			fmt.Fprintf(stderr, "allod: the selected rclone configuration %q is unreadable; check that path and its permissions\n", configPath)
		}
	case siteRemoteCredentialRejected:
		if selectedConfigIsSymlink(configPath) {
			if configPath == "" {
				fmt.Fprintf(stderr, "allod: the stored username or password for '%s' was rejected; update the source credential behind rclone's read-only configuration symlink, then run 'allod site check' again\n", siteRemoteName)
			} else {
				fmt.Fprintf(stderr, "allod: the stored username or password for '%s' in %q was rejected; update the source credential behind that read-only symlink, then run 'allod site check' again with the same --config path\n", siteRemoteName, configPath)
			}
		} else if configPath == "" {
			fmt.Fprintf(stderr, "allod: the stored username or password for '%s' was rejected; run 'allod site config update user' or 'allod site config update password'\n", siteRemoteName)
		} else {
			fmt.Fprintf(stderr, "allod: the stored username or password for '%s' in %q was rejected; run 'allod site config update user' or 'allod site config update password' with that same --config path\n", siteRemoteName, configPath)
		}
	case siteRemoteHostUnreachable:
		fmt.Fprintf(stderr, "allod: the host for '%s' is unreachable; check its address, the network connection, and the hosting service\n", siteRemoteName)
	default:
		if configPath == "" {
			fmt.Fprintf(stderr, "allod: the '%s' hosting remote could not be checked (rclone exited %d)\n", siteRemoteName, result.status)
		} else {
			fmt.Fprintf(stderr, "allod: the '%s' hosting remote using %q could not be checked (rclone exited %d)\n", siteRemoteName, configPath, result.status)
		}
	}
}

// selectedConfigIsSymlink resolves only enough of rclone's implicit precedence
// to choose truthful recovery wording. The path itself stays out of output:
// RCLONE_CONFIG may name an identity-bearing activation credential.
func selectedConfigIsSymlink(configPath string) bool {
	if configPath == "" {
		configPath = os.Getenv("RCLONE_CONFIG")
		if configPath == "" {
			var err error
			configPath, err = defaultRcloneConfigPath()
			if err != nil {
				return false
			}
		}
	}
	info, err := os.Lstat(configPath)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

// siteRemoteFailureExitCode preserves a useful child status except for 7,
// which allod reserves to mean that a deploy completed and only its HTTPS
// verification failed. A pre-build check can never truthfully return that.
func siteRemoteFailureExitCode(status int) int {
	if status == 0 || status == siteVerifyExit {
		return 1
	}
	return status
}

// requireReadableSiteConfig applies the named-path contract shared by commands
// that hand a credential to rclone. The default selection remains rclone's to
// resolve; an explicit file must exist, be regular, and be readable before a
// check or a build starts.
func requireReadableSiteConfig(selection rcloneConfigSelection) {
	if err := selection.requireReadable(); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(stderr, "allod: named rclone configuration does not exist: %q\n", selection.path)
			fmt.Fprintln(stderr, "allod: while resolving: the hosting credential selected by --config")
			fmt.Fprintln(stderr, "allod: to fix: materialise the credential at that path, or omit --config to use rclone's configuration")
			exit(1)
		}
		die(1, "named rclone configuration is not readable: %q: %s", selection.path, err)
	}
}

// siteCheck is the quick, read-only half of the deploy preflight. It neither
// resolves a site repository nor selects a hosting layout: opening 'shared:'
// is enough to prove that the stored host and credential work, and nothing is
// built, synced, verified over HTTPS, or published.
func siteCheck(args []string) {
	var configSelection rcloneConfigSelection
	for len(args) > 0 {
		if rest, consumed := configSelection.consume(args, "check"); consumed {
			args = rest
			continue
		}
		switch args[0] {
		case "-h", "--help":
			fmt.Fprint(stdout, siteUsageText())
			return
		default:
			if strings.HasPrefix(args[0], "-") {
				die(1, "unknown option for site check: %s", args[0])
			}
			die(1, "unexpected argument for site check: %s", args[0])
		}
	}

	if _, err := exec.LookPath("rclone"); err != nil {
		die(1, "'rclone' not found on PATH")
	}
	requireReadableSiteConfig(configSelection)
	requireSiteRemote(configSelection.configPath())
	fmt.Fprintf(stdout, "Remote '%s' is ready.\n", siteRemoteName)
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
	reportSiteRemoteFailure(result, configPath)
	exit(siteRemoteFailureExitCode(result.status))
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
			fmt.Fprint(stdout, siteUsageText())
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
	requireReadableSiteConfig(configSelection)
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

// siteDeployDetail, siteCheckDetail, and siteConfigDetail are the three
// prose blocks this file contributes to 'allod site' usage, in the same
// words the single siteUsageText constant carried before the command table
// split them out. siteUsageText() in site_common.go joins them with the
// other commands' blocks in siteCommands order.
const siteDeployDetail = `'deploy' walks up from the current directory to the site.toml that marks the
site repository root, builds that repo with 'nix build --no-link
--print-out-paths', and syncs the resulting store path through the 'shared'
rclone remote. Deploy never handles a credential, and checks that the remote
can authenticate before it starts the build rather than discovering a rejected
login afterwards.

'--config <path>' selects one rclone configuration path for any command. An
explicit path takes precedence over RCLONE_CONFIG, rclone's reported path,
XDG_CONFIG_HOME, and HOME. A path beginning with '-' uses --config=<path> so it
cannot be mistaken for another option. Deploy, check, and config show accept a
readable regular file or a symlink to one. Create and replace may create a
missing path. Update requires an existing 'shared' stanza. All three mutable
actions atomically replace a regular file at mode 0600, but refuse to replace a
symlink or any other file type. Deploy passes the same path
to the preflight and sync; rclone opens it separately for those calls, so
activation or rotation during the build can make sync read newer contents than
the preflight checked. Without the flag, deploy and check leave resolution to
rclone, while config asks rclone for its configuration file before falling
back to the XDG/HOME default. The hosting config is machine-wide; it does not
belong in site.toml or a site repository.

Each site-enabled binary has one deployment-owned hosting layout compiled in.
Without a linker override it uses 'directadmin', whose docroot is
shared:domains/<domain>/public_html and whose exclusion list is unchanged.
A package can build with '-X main.siteHostingProfileName=public-html' to select
shared:public_html/<domain> and no exclusions, for hosting where ACME is served
elsewhere. An unknown or malformed build-time value fails before the remote
check or build. No flag, environment variable, or site.toml key can change the
installed binary's layout; rollback requires rebuilding the package with the
default value.

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
`

const siteCheckDetail = `'check' opens the 'shared' remote without reading a site repo, building, syncing,
or publishing. A rejected FTP login can identify only the username or password
as the cause, so its failure points to both single-field update commands.
`

const siteConfigDetail = `'config' with no action creates the remote and refuses to replace one that is
already present. 'config show' safely prints its config path, type, host, and
user, but never its plaintext-equivalent obscured password. 'config update'
asks only for the selected field and changes only that value. 'config replace'
is the discoverable full replacement, asking for host, user, and password;
'config --force' remains a compatibility spelling for that same operation.

Passwords are not echoed as they are typed, never appear in a command line,
and are stored the only way rclone accepts a stored password: obscured, by
rclone itself. Every successful create, update, or replacement names 'allod
site check' as the way to verify the stored values with the server.
`

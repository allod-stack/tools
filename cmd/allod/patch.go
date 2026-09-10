package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

func patchMain(args []string) {
	if len(args) == 0 {
		fmt.Fprint(stderr, patchUsageText)
		exit(1)
	}
	command, args := args[0], args[1:]
	switch command {
	case "fetch":
		patchFetch(args)
	case "apply":
		patchApply(args)
	case "receive":
		patchReceive(args)
	case "-h", "--help":
		fmt.Fprint(stdout, patchUsageText)
	default:
		die(1, "unknown patch command: %s", command)
	}
}

var patchTempPath = regexp.MustCompile(`^/tmp/allod-patch\.[a-zA-Z0-9]{10}$`)

func validPatchTempPath(path string) bool {
	return patchTempPath.MatchString(path)
}

const remoteGenerateScript = `set -eu
read -r b64_repo
read -r b64_base
read -r b64_base_set
repo=$(printf '%s' "$b64_repo" | base64 -d)
base=$(printf '%s' "$b64_base" | base64 -d)
base_set=$(printf '%s' "$b64_base_set" | base64 -d)
case "$repo" in *"
"*) printf 'allod: source repo path contains newline\n' >&2; exit 1 ;; esac
case "$base" in *"
"*) printf 'allod: base ref contains newline\n' >&2; exit 1 ;; esac
case "$base_set" in true|false) ;; *) printf 'allod: invalid base mode\n' >&2; exit 1 ;; esac
if ! git -C "$repo" rev-parse --git-dir >/dev/null 2>&1; then
  printf 'allod: not a git repository: %s\n' "$repo" >&2; exit 1
fi
dirty=$(git -C "$repo" status --porcelain)
if [ -n "$dirty" ]; then
  printf 'allod: source worktree is dirty; commit or stash changes before export\n' >&2
  printf '%s\n' "$dirty" >&2
  exit 10
fi
if ! git -C "$repo" rev-parse --verify 'HEAD^{commit}' >/dev/null 2>&1; then
  printf 'allod: source HEAD does not resolve to a commit\n' >&2; exit 11
fi

resolve_default_base() {
  branch=$(git -C "$repo" branch --show-current 2>/dev/null || true)
  upstream=$(git -C "$repo" rev-parse --abbrev-ref --symbolic-full-name '@{u}' 2>/dev/null || true)
  if [ -n "$upstream" ] && git -C "$repo" rev-parse --verify "$upstream^{commit}" >/dev/null 2>&1; then
    printf '%s\n' "$upstream"
    return 0
  fi
  if [ -n "$branch" ] && git -C "$repo" rev-parse --verify "refs/remotes/origin/${branch}^{commit}" >/dev/null 2>&1; then
    printf 'refs/remotes/origin/%s\n' "$branch"
    return 0
  fi
  default_branch=$(git -C "$repo" symbolic-ref refs/remotes/origin/HEAD 2>/dev/null | sed 's|refs/remotes/origin/||' || true)
  if [ -n "$default_branch" ] && git -C "$repo" rev-parse --verify "refs/remotes/origin/${default_branch}^{commit}" >/dev/null 2>&1; then
    printf 'refs/remotes/origin/%s\n' "$default_branch"
    return 0
  fi
  return 1
}

root_export=false
base_commit=
if [ "$base_set" = true ]; then
  base_ref="$base"
  base_commit=$(git -C "$repo" rev-parse --verify "$base_ref^{commit}" 2>/dev/null) || {
    printf 'allod: cannot resolve base ref: %s\n' "$base_ref" >&2; exit 11
  }
else
  if base_ref=$(resolve_default_base); then
    base_commit=$(git -C "$repo" rev-parse --verify "$base_ref^{commit}" 2>/dev/null) || {
      printf 'allod: cannot resolve base ref: %s\n' "$base_ref" >&2; exit 11
    }
  else
    root_export=true
    base_ref="<root>"
    printf 'allod: no source base ref found; exporting full history from root\n' >&2
  fi
fi

if [ "$root_export" = true ]; then
  merges=$(git -C "$repo" rev-list --merges HEAD)
else
  if ! git -C "$repo" merge-base --is-ancestor "$base_commit" HEAD; then
    printf 'allod: base %s is not an ancestor of HEAD\n' "$base_ref" >&2; exit 11
  fi
  ahead=$(git -C "$repo" rev-list "$base_commit..HEAD")
  if [ -z "$ahead" ]; then
    printf 'allod: HEAD is not ahead of base; nothing to export\n' >&2; exit 11
  fi
  merges=$(git -C "$repo" rev-list --merges "$base_commit..HEAD")
fi
if [ -n "$merges" ]; then
  printf 'allod: non-linear history is unsupported; merge commits found in export range\n' >&2; exit 11
fi
head_commit=$(git -C "$repo" rev-parse HEAD)
# git diff --check flags trailing spaces, a space before a tab in indentation,
# and blank lines at end of file, as the source repo's whitespace rules define
# them. Refusing here keeps the defect on the sending side, where the agent can
# amend, instead of applying it and then failing the receive.
if [ "$root_export" = true ]; then
  check_from=$(git -C "$repo" hash-object -t tree /dev/null)
else
  check_from="$base_commit"
fi
if ! whitespace=$(git -C "$repo" diff --check "$check_from" HEAD); then
  printf 'allod: whitespace check failed on the export range (git diff --check); fix and amend before export\n' >&2
  printf '%s\n' "$whitespace" >&2
  exit 17
fi
repo_remote=$(git -C "$repo" remote get-url origin 2>/dev/null || printf '')
tmpdir=$(mktemp -d /tmp/allod-patch.XXXXXXXXXX)
_trap_cleanup() { rm -rf -- "$tmpdir"; }
trap _trap_cleanup EXIT
if [ "$root_export" = true ]; then
  git -C "$repo" format-patch --root HEAD -o "$tmpdir" >&2
else
  git -C "$repo" format-patch "$base_commit..HEAD" -o "$tmpdir" >&2
fi
patch_count=0
: > "$tmpdir/.patches.jsonl"
for f in "$tmpdir"/*.patch; do
  [ -f "$f" ] || continue
  fname=$(basename -- "$f")
  sha=$(sha256sum -b -- "$f" | awk '{print $1}')
  jq -n --arg fn "$fname" --arg sha "$sha" '{filename: $fn, sha256: $sha}' >> "$tmpdir/.patches.jsonl"
  patch_count=$((patch_count + 1))
done
if [ "$patch_count" -eq 0 ]; then
  printf 'allod: git format-patch produced zero patches\n' >&2; exit 11
fi
jq -n \
  --arg repo_remote "$repo_remote" \
  --arg base_commit "$base_commit" \
  --arg head_commit "$head_commit" \
  --argjson root_export "$root_export" \
  --argjson patch_count "$patch_count" \
  --slurpfile patches "$tmpdir/.patches.jsonl" \
  '{repo_remote: $repo_remote, base_commit: (if $root_export then null else $base_commit end), head_commit: $head_commit, root_export: $root_export, patch_count: $patch_count, patches: $patches}' \
  > "$tmpdir/manifest.json"
rm -f "$tmpdir/.patches.jsonl"
trap - EXIT
printf '%s\n' "$tmpdir"
`

const remoteTransferScript = `set -eu
read -r b64_tmpdir
tmpdir=$(printf '%s' "$b64_tmpdir" | base64 -d)
case "$tmpdir" in *"
"*) printf 'allod: temp dir path contains newline\n' >&2; exit 1 ;; esac
if ! printf '%s' "$tmpdir" | grep -qxE '/tmp/allod-patch\.[a-zA-Z0-9]{10}'; then
  printf 'allod: invalid remote temp dir path\n' >&2; exit 1
fi
tar -cz -C "$tmpdir" .
`

const remoteCleanupScript = `set -eu
read -r b64_tmpdir
tmpdir=$(printf '%s' "$b64_tmpdir" | base64 -d)
case "$tmpdir" in *"
"*) printf 'allod: temp dir path contains newline\n' >&2; exit 1 ;; esac
if ! printf '%s' "$tmpdir" | grep -qxE '/tmp/allod-patch\.[a-zA-Z0-9]{10}'; then
  printf 'allod: invalid remote temp dir path\n' >&2; exit 1
fi
rm -rf -- "$tmpdir"
`

func encoded(value string) string {
	return base64.StdEncoding.EncodeToString([]byte(value))
}

func sshCapture(host, script, input string) (string, int) {
	var out bytes.Buffer
	cmd := exec.Command("ssh", "--", host, script)
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = &out
	cmd.Stderr = stderr
	status := commandExitCode(cmd.Run())
	return strings.TrimRight(out.String(), "\n"), status
}

func transferArtifact(host, script, input, destination string) int {
	sshCommand := exec.Command("ssh", "--", host, script)
	sshCommand.Stdin = strings.NewReader(input)
	sshCommand.Stderr = stderr
	pipe, err := sshCommand.StdoutPipe()
	if err != nil {
		return 1
	}
	tarCommand := exec.Command("tar", "-xz", "-C", destination)
	tarCommand.Stdin = pipe
	tarCommand.Stdout = stdout
	tarCommand.Stderr = stderr
	if err := sshCommand.Start(); err != nil {
		return 1
	}
	if err := tarCommand.Start(); err != nil {
		_ = sshCommand.Process.Kill()
		_ = sshCommand.Wait()
		return 1
	}
	sshStatus := commandExitCode(sshCommand.Wait())
	tarStatus := commandExitCode(tarCommand.Wait())
	if tarStatus != 0 {
		return tarStatus
	}
	return sshStatus
}

func writableDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir() && info.Mode().Perm()&0222 != 0
}

func patchFetch(args []string) {
	target, base, output := "", "", ""
	baseSet, outputSet := false, false
	for len(args) > 0 {
		switch args[0] {
		case "--base":
			requireValue(args, args[0])
			if args[1] == "" {
				die(1, "--base cannot be empty")
			}
			base, baseSet, args = args[1], true, args[2:]
		case "--output":
			requireValue(args, args[0])
			output, outputSet, args = args[1], true, args[2:]
		case "-h", "--help":
			fmt.Fprint(stdout, patchUsageText)
			return
		default:
			if strings.HasPrefix(args[0], "-") {
				die(1, "unknown option for patch fetch: %s", args[0])
			}
			if target != "" {
				die(1, "patch fetch accepts at most one target")
			}
			target, args = args[0], args[1:]
		}
	}
	if target == "" {
		die(1, "patch fetch requires <ssh-host>:<source-repo>")
	}
	colon := strings.IndexByte(target, ':')
	if colon < 0 {
		die(1, "invalid target; expected <ssh-host>:<source-repo>")
	}
	host, sourceRepo := target[:colon], target[colon+1:]
	if host == "" {
		die(1, "empty SSH host in target")
	}
	if sourceRepo == "" {
		die(1, "empty source repo path in target")
	}
	if !filepath.IsAbs(sourceRepo) {
		die(1, "source repo must be an absolute path: %s", sourceRepo)
	}
	if strings.Contains(sourceRepo, "\n") {
		die(1, "source repo path must not contain newlines")
	}
	if strings.Contains(base, "\n") {
		die(1, "--base value must not contain newlines")
	}

	outputParent, finalOutput := "/tmp", ""
	needsRename := false
	if outputSet {
		parentInput := filepath.Dir(output)
		parent, err := filepath.Abs(parentInput)
		if err != nil {
			die(1, "output parent directory does not exist: %s", parentInput)
		}
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			parent = resolved
		}
		if info, err := os.Stat(parent); err != nil || !info.IsDir() {
			die(1, "output parent directory does not exist: %s", parentInput)
		}
		if !writableDirectory(parent) {
			die(1, "output parent is not a writable directory: %s", parent)
		}
		outputParent = parent
		finalOutput = filepath.Join(parent, filepath.Base(output))
		if _, err := os.Lstat(finalOutput); err == nil || !os.IsNotExist(err) {
			die(1, "output path already exists: %s", finalOutput)
		}
		needsRename = true
	}
	baseMode := "false"
	if baseSet {
		baseMode = "true"
	}
	generateInput := encoded(sourceRepo) + "\n" + encoded(base) + "\n" + encoded(baseMode) + "\n"
	remoteDir, status := sshCapture(host, remoteGenerateScript, generateInput)
	if status != 0 {
		if status == 10 || status == 11 || status == 17 {
			exit(status)
		}
		exit(1)
	}
	if remoteDir == "" {
		die(1, "remote generate produced no output")
	}
	if strings.Contains(remoteDir, "\n") {
		die(1, "remote generate produced multi-line output")
	}
	if !validPatchTempPath(remoteDir) {
		die(1, "invalid remote temp dir path: %s", remoteDir)
	}
	var staging string
	var err error
	if needsRename {
		staging, err = makeTempDir(outputParent, "allod-patch-staging.", 10)
	} else {
		staging, err = makeTempDir("/tmp", "allod-patch.", 10)
		finalOutput = staging
	}
	if err != nil {
		die(1, "could not create local staging directory")
	}
	remoteInput := encoded(remoteDir) + "\n"
	if transferArtifact(host, remoteTransferScript, remoteInput, staging) != 0 {
		_ = os.RemoveAll(staging)
		fmt.Fprintf(stderr, "allod: remote temp dir (requires manual cleanup): %s\n", remoteDir)
		die(1, "artifact transfer or extraction failed")
	}
	manifestPath := filepath.Join(staging, "manifest.json")
	manifestInfo, manifestErr := os.Lstat(manifestPath)
	if manifestErr != nil || manifestInfo.Mode()&os.ModeSymlink != 0 || !manifestInfo.Mode().IsRegular() {
		_ = os.RemoveAll(staging)
		fmt.Fprintf(stderr, "allod: remote temp dir (requires manual cleanup): %s\n", remoteDir)
		die(1, "manifest.json is missing, a symlink, or not a regular file")
	}
	if needsRename {
		if err := os.Rename(staging, finalOutput); err != nil {
			_ = os.RemoveAll(staging)
			fmt.Fprintf(stderr, "allod: remote temp dir (requires manual cleanup): %s\n", remoteDir)
			die(1, "failed to move staging dir to output path")
		}
	}
	if _, cleanupStatus := sshCapture(host, remoteCleanupScript, remoteInput); cleanupStatus != 0 {
		fmt.Fprintln(stderr, "allod: error: remote cleanup failed")
		fmt.Fprintf(stderr, "allod: local artifact: %s\n", finalOutput)
		fmt.Fprintf(stderr, "allod: remote temp dir (requires manual cleanup): %s\n", remoteDir)
		exit(1)
	}
	manifest, err := readManifest(filepath.Join(finalOutput, "manifest.json"))
	if err != nil {
		die(1, "could not read transferred manifest")
	}
	baseShort := "root"
	if !manifest.RootExport && manifest.BaseCommit != nil {
		baseShort = shortHash(*manifest.BaseCommit)
	}
	fmt.Fprintf(stdout, "allod: fetched %d patch(es) (%s..%s)\n", manifest.PatchCount, baseShort, shortHash(manifest.HeadCommit))
	fmt.Fprintf(stdout, "allod: artifact dir: %s\n", finalOutput)
}

type patchEntry struct {
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
}

type patchManifest struct {
	RepoRemote string       `json:"repo_remote"`
	BaseCommit *string      `json:"base_commit"`
	HeadCommit string       `json:"head_commit"`
	RootExport bool         `json:"root_export"`
	PatchCount int          `json:"patch_count"`
	Patches    []patchEntry `json:"patches"`
}

func shortHash(value string) string {
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

func readManifest(path string) (patchManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return patchManifest{}, err
	}
	var manifest patchManifest
	err = json.Unmarshal(data, &manifest)
	return manifest, err
}

var fullHex = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
var digestHex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validateManifest(data []byte) (patchManifest, string) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return patchManifest{}, "not a JSON object"
	}
	required := []string{"repo_remote", "base_commit", "head_commit", "patch_count", "patches"}
	for _, field := range required {
		if _, ok := fields[field]; !ok {
			return patchManifest{}, "missing required field"
		}
	}
	var manifest patchManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return patchManifest{}, manifestTypeError(fields)
	}
	if _, ok := fields["root_export"]; !ok {
		manifest.RootExport = false
	} else {
		var value bool
		if json.Unmarshal(fields["root_export"], &value) != nil {
			return patchManifest{}, "root_export: expected boolean"
		}
	}
	var repoRemote string
	if json.Unmarshal(fields["repo_remote"], &repoRemote) != nil {
		return patchManifest{}, "repo_remote: expected string"
	}
	if manifest.RootExport {
		if string(fields["base_commit"]) != "null" {
			return patchManifest{}, "base_commit: expected null for root export"
		}
	} else {
		var value string
		if json.Unmarshal(fields["base_commit"], &value) != nil {
			return patchManifest{}, "base_commit: expected string"
		}
		manifest.BaseCommit = &value
	}
	var head string
	if json.Unmarshal(fields["head_commit"], &head) != nil {
		return patchManifest{}, "head_commit: expected string"
	}
	var count int
	if json.Unmarshal(fields["patch_count"], &count) != nil {
		return patchManifest{}, "patch_count: expected number"
	}
	var patches []patchEntry
	if json.Unmarshal(fields["patches"], &patches) != nil {
		return patchManifest{}, "patches: expected array"
	}
	manifest.RepoRemote, manifest.HeadCommit, manifest.PatchCount, manifest.Patches = repoRemote, head, count, patches
	if !manifest.RootExport && (manifest.BaseCommit == nil || !fullHex.MatchString(*manifest.BaseCommit)) {
		return patchManifest{}, "base_commit: not a full lowercase hex commit ID"
	}
	if !fullHex.MatchString(manifest.HeadCommit) {
		return patchManifest{}, "head_commit: not a full lowercase hex commit ID"
	}
	if manifest.PatchCount != len(manifest.Patches) {
		return patchManifest{}, "patch_count does not match patches array length"
	}
	seen := map[string]bool{}
	for _, patch := range manifest.Patches {
		if patch.Filename == "" || !strings.HasSuffix(patch.Filename, ".patch") || strings.Contains(patch.Filename, "/") || strings.Contains(patch.Filename, "..") || hasControl(patch.Filename) {
			return patchManifest{}, "patches filename is invalid"
		}
		if !digestHex.MatchString(patch.SHA256) {
			return patchManifest{}, "patches sha256 is not a valid hex digest"
		}
		if seen[patch.Filename] {
			return patchManifest{}, "duplicate filenames"
		}
		seen[patch.Filename] = true
	}
	return manifest, "ok"
}

func manifestTypeError(fields map[string]json.RawMessage) string {
	checks := []struct {
		name, message string
		destination   any
	}{
		{"repo_remote", "repo_remote: expected string", new(string)},
		{"head_commit", "head_commit: expected string", new(string)},
		{"patch_count", "patch_count: expected number", new(int)},
		{"patches", "patches: expected array", new([]patchEntry)},
	}
	for _, check := range checks {
		if raw, ok := fields[check.name]; ok && json.Unmarshal(raw, check.destination) != nil {
			return check.message
		}
	}
	return "manifest validation failed"
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 32 {
			return true
		}
	}
	return false
}

var schemeRemote = regexp.MustCompile(`^(https?|ssh|git)://(?:[^/@]+@)?([^/:?#]+)(?::[0-9]+)?/(.+)$`)
var scpRemote = regexp.MustCompile(`^[^/@:]+@([^/:]+):(.+)$`)

func remoteIdentity(remote string) (string, bool) {
	if remote == "" || strings.Contains(remote, "\n") {
		return "", false
	}
	var host, path string
	if matches := schemeRemote.FindStringSubmatch(remote); matches != nil {
		host, path = matches[2], matches[3]
	} else if matches := scpRemote.FindStringSubmatch(remote); matches != nil {
		host, path = matches[1], matches[2]
		if strings.HasPrefix(path, "/") {
			return "", false
		}
	} else {
		return "", false
	}
	if host == "" || path == "" || strings.ContainsAny(path, "?#") {
		return "", false
	}
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	if path == "" || !strings.Contains(path, "/") {
		return "", false
	}
	return strings.ToLower(host) + "/" + path, true
}

func remotesMatch(left, right string) bool {
	if left == right {
		return true
	}
	leftID, leftOK := remoteIdentity(left)
	rightID, rightOK := remoteIdentity(right)
	return leftOK && rightOK && leftID == rightID
}

func patchApply(args []string) {
	artifact, repoPath := "", ""
	push := false
	for len(args) > 0 {
		switch args[0] {
		case "--repo":
			requireValue(args, args[0])
			repoPath, args = args[1], args[2:]
		case "--push":
			push, args = true, args[1:]
		case "-h", "--help":
			fmt.Fprint(stdout, patchUsageText)
			return
		default:
			if strings.HasPrefix(args[0], "-") {
				die(1, "unknown option for patch apply: %s", args[0])
			}
			if artifact != "" {
				die(1, "patch apply accepts at most one artifact directory")
			}
			artifact, args = args[0], args[1:]
		}
	}
	if artifact == "" {
		die(1, "patch apply requires <artifact-dir>")
	}
	abs, err := filepath.Abs(artifact)
	if err != nil {
		die(12, "artifact directory does not exist: %s", artifact)
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		die(12, "artifact directory does not exist: %s", artifact)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		die(12, "artifact directory does not exist: %s", artifact)
	}
	manifestPath := filepath.Join(abs, "manifest.json")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil {
		die(12, "manifest.json is missing or not a regular file")
	}
	if manifestInfo.Mode()&os.ModeSymlink != 0 {
		die(12, "manifest.json is a symlink")
	}
	if !manifestInfo.Mode().IsRegular() {
		die(12, "manifest.json is missing or not a regular file")
	}
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		die(12, "malformed JSON in manifest.json")
	}
	var syntax any
	if json.Unmarshal(manifestData, &syntax) != nil {
		die(12, "malformed JSON in manifest.json")
	}
	manifest, diagnostic := validateManifest(manifestData)
	if diagnostic != "ok" {
		die(12, "manifest integrity failure: %s", diagnostic)
	}
	patchFiles := make([]string, 0, len(manifest.Patches))
	for _, patch := range manifest.Patches {
		path := filepath.Join(abs, patch.Filename)
		patchInfo, err := os.Lstat(path)
		if err != nil {
			die(12, "listed patch file missing: %s", patch.Filename)
		}
		if patchInfo.Mode()&os.ModeSymlink != 0 {
			die(12, "listed patch file is a symlink: %s", patch.Filename)
		}
		if !patchInfo.Mode().IsRegular() {
			die(12, "listed patch file is not a regular file: %s", patch.Filename)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			die(12, "listed patch file is not a regular file: %s", patch.Filename)
		}
		digest := sha256.Sum256(data)
		actual := hex.EncodeToString(digest[:])
		if actual != patch.SHA256 {
			die(12, "checksum mismatch for %s: expected %s, got %s", patch.Filename, patch.SHA256, actual)
		}
		patchFiles = append(patchFiles, path)
	}
	entries, _ := os.ReadDir(abs)
	actualCount := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".patch") {
			actualCount++
		}
	}
	if actualCount != manifest.PatchCount {
		die(12, "unlisted .patch files in artifact directory")
	}
	repo := resolvePatchDestination(repoPath)
	if dirty, _ := gitOutput(repo, "status", "--porcelain"); dirty != "" {
		fmt.Fprintln(stderr, "allod: destination worktree is dirty; commit or stash changes before apply")
		exit(16)
	}
	destHasHead := gitQuiet(repo, "rev-parse", "--verify", "HEAD^{commit}")
	if manifest.RootExport && destHasHead {
		fmt.Fprintln(stderr, "allod: root export can only be applied to an empty destination history")
		exit(14)
	}
	destRemote, _ := gitOutput(repo, "remote", "get-url", "origin")
	allowMissing := manifest.RepoRemote == "" && manifest.RootExport && !destHasHead
	if !allowMissing && !remotesMatch(destRemote, manifest.RepoRemote) {
		fmt.Fprintln(stderr, "allod: repo identity mismatch")
		fmt.Fprintf(stderr, "allod: manifest remote: %s\n", manifest.RepoRemote)
		fmt.Fprintf(stderr, "allod: destination remote: %s\n", destRemote)
		exit(13)
	}
	if !manifest.RootExport {
		base := *manifest.BaseCommit
		if !gitQuiet(repo, "rev-parse", "--verify", base+"^{commit}") {
			fmt.Fprintf(stderr, "allod: base commit %s not found in destination; run git fetch first\n", base)
			exit(14)
		}
		if !gitQuiet(repo, "merge-base", "--is-ancestor", base, "HEAD") {
			fmt.Fprintf(stderr, "allod: base commit %s is not an ancestor of destination HEAD; check out the branch containing the base commit\n", base)
			exit(14)
		}
	}
	preHead := "<unborn>"
	if destHasHead {
		preHead, _ = gitOutput(repo, "rev-parse", "HEAD")
	}
	amArgs := append([]string{"am", "--3way"}, patchFiles...)
	if status := gitInherit(repo, amArgs...); status != 0 {
		gitDir, _ := gitOutput(repo, "rev-parse", "--path-format=absolute", "--git-dir")
		if info, err := os.Stat(filepath.Join(gitDir, "rebase-apply")); err == nil && info.IsDir() {
			_ = runCommand(repo, nil, io.Discard, io.Discard, "git", "am", "--abort")
		}
		current, ok := gitOutput(repo, "rev-parse", "HEAD")
		if !ok {
			current = "<unborn>"
		}
		if current != preHead {
			fmt.Fprintf(stderr, "allod: WARNING: HEAD changed after git am abort; expected %s, got %s\n", preHead, current)
			fmt.Fprintf(stderr, "allod: repo requires manual repair: %s\n", repo)
		}
		if dirty, _ := gitOutput(repo, "status", "--porcelain"); dirty != "" {
			fmt.Fprintln(stderr, "allod: WARNING: worktree not clean after git am abort")
			fmt.Fprintf(stderr, "allod: repo requires manual repair: %s\n", repo)
		}
		if isDirectory(filepath.Join(gitDir, "rebase-apply")) || isDirectory(filepath.Join(gitDir, "rebase-merge")) {
			fmt.Fprintln(stderr, "allod: WARNING: rebase state directory still present after abort")
			fmt.Fprintf(stderr, "allod: repo requires manual repair: %s\n", repo)
		}
		exit(15)
	}
	// fetch refuses to export a range that fails git diff --check, so this
	// fires only for an artifact that did not come through that gate. It is a
	// report, not a failure: the commits stay applied and the human decides.
	appliedRange := ""
	var whitespace bytes.Buffer
	var diffStatus int
	if preHead == "<unborn>" {
		emptyTree, _ := gitOutput(repo, "hash-object", "-t", "tree", "/dev/null")
		diffStatus = runCommand(repo, nil, &whitespace, stderr, "git", "diff", "--check", emptyTree, "HEAD")
	} else {
		appliedRange = preHead + "..HEAD"
		diffStatus = runCommand(repo, nil, &whitespace, stderr, "git", "diff", "--check", appliedRange)
	}
	fmt.Fprintf(stdout, "allod: applied %d patch(es)\n", manifest.PatchCount)
	if preHead == "<unborn>" {
		if status := gitInherit(repo, "log", "--oneline", "HEAD"); status != 0 {
			exit(status)
		}
	} else if status := gitInherit(repo, "log", "--oneline", appliedRange); status != 0 {
		exit(status)
	}
	if status := gitInherit(repo, "show", "--stat", "--oneline", "HEAD"); status != 0 {
		exit(status)
	}
	if diffStatus != 0 {
		fmt.Fprintln(stderr, "allod: WARNING: whitespace check (git diff --check) flagged the applied commits; they stay applied:")
		fmt.Fprint(stderr, whitespace.String())
	}
	if push {
		if status := gitInherit(repo, "push"); status != 0 {
			fmt.Fprintln(stderr, "allod: git push failed")
			fmt.Fprintf(stderr, "allod: repo: %s\n", repo)
			fmt.Fprintf(stderr, "allod: pre-apply HEAD: %s\n", preHead)
			current, _ := gitOutput(repo, "rev-parse", "HEAD")
			fmt.Fprintf(stderr, "allod: current HEAD: %s\n", current)
			fmt.Fprintf(stderr, "allod: to push manually: git -C \"%s\" push\n", repo)
			if preHead == "<unborn>" {
				fmt.Fprintln(stderr, "allod: to undo: reclone or manually remove the newly created history")
			} else {
				fmt.Fprintf(stderr, "allod: to undo: git -C \"%s\" reset --hard %s\n", repo, preHead)
			}
			exit(1)
		}
	} else {
		fmt.Fprintln(stdout, "allod: run git push to publish applied patches")
	}
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func captureExit(fn func()) (code int) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if e, ok := recovered.(cliExit); ok {
				code = e.code
				return
			}
			panic(recovered)
		}
	}()
	fn()
	return 0
}

func patchReceive(args []string) {
	target, destination, base := "", "", ""
	baseSet, push := false, false
	for len(args) > 0 {
		switch args[0] {
		case "--base":
			requireValue(args, args[0])
			if args[1] == "" {
				die(1, "--base cannot be empty")
			}
			base, baseSet, args = args[1], true, args[2:]
		case "--push":
			push, args = true, args[1:]
		case "-h", "--help":
			fmt.Fprint(stdout, patchUsageText)
			return
		default:
			if strings.HasPrefix(args[0], "-") {
				die(1, "unknown option for patch receive: %s", args[0])
			}
			if target == "" {
				target = args[0]
			} else if destination == "" {
				destination = args[0]
			} else {
				die(1, "patch receive accepts at most two positional arguments")
			}
			args = args[1:]
		}
	}
	if target == "" {
		die(1, "patch receive requires <ssh-host>:<source-repo>")
	}
	if destination == "" {
		die(1, "patch receive requires <destination-repo>")
	}
	destination = resolvePatchDestination(destination)
	parent, err := makeTempDir("/tmp", "allod-patch-receive.", 10)
	if err != nil {
		die(1, "could not create receive directory")
	}
	artifact := filepath.Join(parent, "artifact")
	fetchArgs := []string{target, "--output", artifact}
	if baseSet {
		fetchArgs = append(fetchArgs, "--base", base)
	}
	fetchStatus := captureExit(func() { patchFetch(fetchArgs) })
	if fetchStatus != 0 {
		if isDirectory(artifact) {
			fmt.Fprintf(stderr, "allod: artifact dir preserved: %s\n", artifact)
		} else {
			_ = os.RemoveAll(parent)
		}
		exit(fetchStatus)
	}
	applyArgs := []string{artifact, "--repo", destination}
	if push {
		applyArgs = append(applyArgs, "--push")
	}
	applyStatus := captureExit(func() { patchApply(applyArgs) })
	fmt.Fprintf(stdout, "allod: artifact dir: %s\n", artifact)
	if applyStatus != 0 {
		exit(applyStatus)
	}
}

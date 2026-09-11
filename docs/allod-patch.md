# allod patch

Transfer committed changes between environments as `git format-patch` artifacts, so a human can review and push without granting one environment both private and public capabilities.

## Commands

### fetch

Fetch patches from a remote source repo via SSH.

```
allod patch fetch <ssh-host>:<source-repo> [--base <ref>] [--output <dir>]
```

- `--base <ref>` - Base ref for patch range (default: source branch upstream, same-named origin branch, or origin default branch; if none exists, export from root)
- `--output <dir>` - Local directory for artifacts (default: auto-generated in `/tmp`)

SSHes into the source VM, validates the worktree is clean, checks the export range for whitespace errors, generates `git format-patch` artifacts, transfers them via tar, and cleans up the remote temp dir.

The whitespace check is `git diff --check` over the export range, run on the source repository before anything is exported. It flags trailing spaces, a space before a tab in indentation, and a blank line at end of file, as the source repo's `core.whitespace` and `.gitattributes` settings define them. While it fails, `fetch` refuses with exit 17 and prints git's line-level output, so the fix is made and amended on the source side before the patches cross the boundary.

### apply

Apply fetched patches to a local destination repo.

```
allod patch apply <artifact-dir> [--repo <destination-repo>]
```

- `--repo <path>` - Destination repo (default: current directory)

Validates the manifest and checksums, verifies the destination repo matches the source's origin URL, and applies patches with `git am --3way`. Common equivalent remote URL forms such as `https://github.com/org/repo.git`, `git@github.com:org/repo.git`, and `ssh://git@github.com/org/repo.git` are normalized before comparison. Root exports can only be applied to an empty destination history; if a root-export source has no `origin`, the remote identity check is skipped only for that empty-destination bootstrap case.

After `git am`, the same `git diff --check` runs over the applied range. Because `fetch` already refuses a range that fails it, this only fires for an artifact that did not come through `fetch`. It reports the offending lines on stderr and does not fail: the applied commits stay applied, and the human pushes.

### receive

Fetch and apply patches in one step.

```
allod patch receive <ssh-host>:<source-repo> <destination-repo> [--base <ref>]
```

Runs `fetch` then `apply`. The artifact directory is preserved after both success and failure for inspection.

## Repo arguments

`<source-repo>` and `<destination-repo>` (and `apply --repo`'s value) each accept either a filesystem path or a repository-registry id such as `allod/memory`. A repo argument that is absolute or begins with `~`, `.`, or `..` is a path. Anything else is looked up in the repository registry (`inventory/scripts/repositories.json`) first and, when no entry matches, treated as a relative path. This lets a relay command such as `allod patch receive <host>:allod/memory allod/memory` resolve both ends through the registry, so neither the agent composing the command nor the human running it needs to know where the other side's checkout lives.

An id that resolves to the wrong clone is not a silent misapply: `apply`'s origin-URL identity check (exit 13, below) still runs against whatever directory the id names, so a stale or wrong registry entry fails there rather than applying into an unrelated repository.

## Manifest format

```json
{
  "repo_remote": "ssh://git@forge.example:2222/org/repo.git",
  "base_commit": "abc123...",
  "head_commit": "def456...",
  "patch_count": 2,
  "patches": [
    {"filename": "0001-some-change.patch", "sha256": "..."},
    {"filename": "0002-another-change.patch", "sha256": "..."}
  ]
}
```

- `base_commit` and `head_commit` are full lowercase hex Git object IDs (40 or 64 characters).
- Each `sha256` is the lowercase hex digest from `sha256sum -b`.
- Filenames are basenames ending in `.patch` with no path separators or traversal.

## Exit codes

```
0   success
1   usage error / SSH failure / general error
10  source worktree dirty
11  source range not exportable (not ancestor, not ahead, merge commits, empty)
12  manifest/checksum integrity failure
13  repo identity mismatch (origin URL after normalization for recognized URL forms; scheme, user, port, trailing slash, and `.git` suffix are ignored)
14  base commit missing or not ancestor of destination HEAD
15  git am failed (patches aborted)
16  destination worktree dirty
17  source range fails git diff --check (whitespace errors)
```

## Security model

Every SSH invocation uses static remote command text. Dynamic values (source repo path, base ref, temp dir path) are base64-encoded and passed through stdin, never interpolated into shell commands. The remote temp dir path is validated against a fixed `/tmp/allod-patch.XXXXXXXXXX` pattern before any tar or cleanup operations.

## Examples

Fetch patches from a dev VM, naming the source repo by its registry id:

```sh
allod patch fetch devvm:allod/memory
```

Apply fetched patches, naming the destination by its registry id:

```sh
allod patch apply /tmp/allod-patch.abcdefghij --repo allod/memory
```

One-step fetch and apply, both ends by registry id:

```sh
allod patch receive devvm:allod/memory allod/memory
```

A path still works on either side:

```sh
allod patch receive devvm:~/work/allod/memory ~/work/allod/memory
```

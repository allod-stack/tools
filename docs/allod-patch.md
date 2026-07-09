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

SSHes into the source VM, validates the worktree is clean, generates `git format-patch` artifacts, transfers them via tar, and cleans up the remote temp dir.

### apply

Apply fetched patches to a local destination repo.

```
allod patch apply <artifact-dir> [--repo <destination-repo>] [--push]
```

- `--repo <path>` - Destination repo (default: current directory)
- `--push` - Push after successful apply

Validates the manifest and checksums, verifies the destination repo matches the source's origin URL, and applies patches with `git am --3way`. Common equivalent remote URL forms such as `https://github.com/org/repo.git`, `git@github.com:org/repo.git`, and `ssh://git@github.com/org/repo.git` are normalized before comparison. Root exports can only be applied to an empty destination history.

### receive

Fetch and apply patches in one step.

```
allod patch receive <ssh-host>:<source-repo> <destination-repo> [--base <ref>] [--push]
```

Runs `fetch` then `apply`. The artifact directory is preserved after both success and failure for inspection.

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
```

## Security model

Every SSH invocation uses static remote command text. Dynamic values (source repo path, base ref, temp dir path) are base64-encoded and passed through stdin, never interpolated into shell commands. The remote temp dir path is validated against a fixed `/tmp/allod-patch.XXXXXXXXXX` pattern before any tar or cleanup operations.

## Examples

Fetch patches from a dev VM:

```sh
allod patch fetch devvm:/home/user/work/myrepo
```

Apply fetched patches:

```sh
allod patch apply /tmp/allod-patch.abcdefghij --repo ~/work/myrepo --push
```

One-step fetch and apply:

```sh
allod patch receive devvm:/home/user/work/myrepo ~/work/myrepo --push
```

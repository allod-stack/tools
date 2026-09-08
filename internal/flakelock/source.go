package flakelock

import "strings"

// A Source is what a lock node's flake.nix declaration says about where the
// input comes from, reduced to what is needed to read its branch head without
// Nix and to pin a revision of it.
type Source struct {
	Kind SourceKind
	// URL is the repository to ask over the git protocol and Ref the branch,
	// tag, full ref, or "HEAD" for the default branch. Both are set for
	// SourceBranch only.
	URL string
	Ref string
	// Rev is the revision the lock holds, or "" when it holds none.
	Rev string
	// Prefix followed by a revision is a flake reference that pins that
	// revision while leaving the node's declaration intact. Set for
	// SourceBranch only.
	Prefix string
}

// SourceKind says whether the tool can move a node itself.
type SourceKind int

const (
	// SourceOpaque is a declaration the tool cannot read a head for: a
	// registry reference, a tarball, a path, a node the path does not reach,
	// or a git or GitHub reference carrying an attribute (`dir`, `host`,
	// `submodules`) that an override would have to reproduce. Nix resolves it.
	SourceOpaque SourceKind = iota
	// SourceFixed is a declaration that names a revision itself; nothing can
	// move it, so there is nothing to read or update.
	SourceFixed
	// SourceBranch is a branch, tag, or default branch of a git repository
	// that `git ls-remote` can read.
	SourceBranch
)

// Source classifies the node a slash-joined input path from root resolves to,
// following `follows` edges as Rev does.
func (l *Lock) Source(path string) Source {
	nodeName, ok := l.follow("root", strings.Split(path, "/"))
	if !ok {
		return Source{}
	}
	n := l.nodes[nodeName]
	original := n.original
	opaque := Source{Rev: n.rev}
	if _, fixed := original["rev"]; fixed {
		return Source{Kind: SourceFixed, Rev: n.rev}
	}
	ref, ok := optionalString(original, "ref")
	if !ok {
		return opaque
	}
	if ref == "" {
		ref = "HEAD"
	}
	switch original["type"] {
	case "github":
		owner, ownerOK := original["owner"].(string)
		repo, repoOK := original["repo"].(string)
		if !ownerOK || !repoOK || owner == "" || repo == "" || !onlyKeys(original, "type", "owner", "repo", "ref") {
			return opaque
		}
		return Source{
			Kind:   SourceBranch,
			URL:    "https://github.com/" + owner + "/" + repo,
			Ref:    ref,
			Rev:    n.rev,
			Prefix: "github:" + owner + "/" + repo + "/",
		}
	case "git":
		url, urlOK := original["url"].(string)
		if !urlOK || url == "" || !onlyKeys(original, "type", "url", "ref") {
			return opaque
		}
		sep := "?"
		if strings.Contains(url, "?") {
			sep = "&"
		}
		return Source{
			Kind:   SourceBranch,
			URL:    url,
			Ref:    ref,
			Rev:    n.rev,
			Prefix: "git+" + url + sep + "rev=",
		}
	}
	return opaque
}

// optionalString reads a string attribute that may be absent; a present
// attribute of another type is a shape the tool does not know.
func optionalString(attrs map[string]any, key string) (string, bool) {
	value, present := attrs[key]
	if !present {
		return "", true
	}
	s, ok := value.(string)
	return s, ok
}

// onlyKeys reports whether attrs has no key outside allowed.
func onlyKeys(attrs map[string]any, allowed ...string) bool {
	for key := range attrs {
		known := false
		for _, name := range allowed {
			if key == name {
				known = true
				break
			}
		}
		if !known {
			return false
		}
	}
	return true
}

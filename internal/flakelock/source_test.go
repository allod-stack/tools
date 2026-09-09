package flakelock

import (
	"strings"
	"testing"
)

func rev(c byte) string { return strings.Repeat(string(c), 40) }

// Source reads the node's flake.nix declaration: a GitHub or git branch is
// readable, a declaration naming a revision is fixed, and every other shape —
// registry references, extra attributes an override would have to carry, a
// node with no declaration at all — is left to nix.
func TestSourceClassifiesDeclarations(t *testing.T) {
	lock := mustParse(t, `{"nodes":{
	  "root": {"inputs": {"gh": "gh", "ghdef": "ghdef", "fixed": "fixed", "sub": "sub", "git": "git", "gitq": "gitq", "gitmod": "gitmod", "reg": "reg", "bare": "bare", "alias": ["gh"]}},
	  "gh": {"original": {"type": "github", "owner": "acme", "repo": "demo", "ref": "main"}, "locked": {"rev": "`+rev('a')+`"}},
	  "ghdef": {"original": {"type": "github", "owner": "acme", "repo": "demo"}, "locked": {"rev": "`+rev('b')+`"}},
	  "fixed": {"original": {"type": "github", "owner": "acme", "repo": "demo", "rev": "`+rev('c')+`"}, "locked": {"rev": "`+rev('c')+`"}},
	  "sub": {"original": {"type": "github", "owner": "acme", "repo": "demo", "dir": "sub"}, "locked": {"rev": "`+rev('d')+`"}},
	  "git": {"original": {"type": "git", "url": "ssh://git@forge.example:2222/acme/demo.git", "ref": "refs/heads/master"}, "locked": {"rev": "`+rev('e')+`"}},
	  "gitq": {"original": {"type": "git", "url": "https://forge.example/acme/demo?x=1"}, "locked": {"rev": "`+rev('f')+`"}},
	  "gitmod": {"original": {"type": "git", "url": "https://forge.example/acme/demo", "submodules": true}, "locked": {"rev": "`+rev('1')+`"}},
	  "reg": {"original": {"type": "indirect", "id": "nixpkgs"}, "locked": {"rev": "`+rev('2')+`"}},
	  "bare": {"locked": {"rev": "`+rev('3')+`"}}
	}}`)
	gh := Source{Kind: SourceBranch, URL: "https://github.com/acme/demo", Ref: "main", Rev: rev('a'), Prefix: "github:acme/demo/"}
	cases := map[string]Source{
		"gh":      gh,
		"alias":   gh,
		"ghdef":   {Kind: SourceBranch, URL: "https://github.com/acme/demo", Ref: "HEAD", Rev: rev('b'), Prefix: "github:acme/demo/"},
		"fixed":   {Kind: SourceFixed, Rev: rev('c')},
		"sub":     {Rev: rev('d')},
		"git":     {Kind: SourceBranch, URL: "ssh://git@forge.example:2222/acme/demo.git", Ref: "refs/heads/master", Rev: rev('e'), Prefix: "git+ssh://git@forge.example:2222/acme/demo.git?rev="},
		"gitq":    {Kind: SourceBranch, URL: "https://forge.example/acme/demo?x=1", Ref: "HEAD", Rev: rev('f'), Prefix: "git+https://forge.example/acme/demo?x=1&rev="},
		"gitmod":  {Rev: rev('1')},
		"reg":     {Rev: rev('2')},
		"bare":    {Rev: rev('3')},
		"missing": {},
	}
	for path, want := range cases {
		if got := lock.Source(path); got != want {
			t.Errorf("Source(%q) = %+v, want %+v", path, got, want)
		}
	}
}

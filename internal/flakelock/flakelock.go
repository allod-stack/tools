// Package flakelock reads the parts of a flake.lock that flake-update-cascade
// needs: which reachable direct pins carry a requested input name, and which
// revision a pin path resolves to.
//
// The walks tolerate the shapes a lock can take: a missing node, a missing
// `inputs`, an edge that is a `follows` array rather than a node name. They are
// pure, so a malformed lock is reported by Parse and never by a walk.
package flakelock

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Lock is a parsed flake.lock.
type Lock struct {
	nodes map[string]node
}

type node struct {
	inputs   map[string]edge
	rev      string
	original map[string]any
}

// An edge is either a pin, naming another node, or a follows, an absolute path
// of input names from root. Anything else in the file is neither and resolves
// to nothing.
type edge struct {
	pin     string
	follows []string
	kind    edgeKind
}

type edgeKind int

const (
	edgeOther edgeKind = iota
	edgePin
	edgeFollows
)

// Parse decodes a lock file's bytes.
func Parse(data []byte) (*Lock, error) {
	var raw struct {
		Nodes map[string]struct {
			Inputs   map[string]json.RawMessage `json:"inputs"`
			Locked   map[string]json.RawMessage `json:"locked"`
			Original map[string]any             `json:"original"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	lock := &Lock{nodes: make(map[string]node, len(raw.Nodes))}
	for name, rawNode := range raw.Nodes {
		n := node{inputs: make(map[string]edge, len(rawNode.Inputs)), original: rawNode.Original}
		for input, rawEdge := range rawNode.Inputs {
			n.inputs[input] = parseEdge(rawEdge)
		}
		if rawRev, ok := rawNode.Locked["rev"]; ok {
			// jq's `.locked.rev // empty` yields the string, or nothing for
			// null; a non-string rev never occurs in a lock Nix wrote.
			_ = json.Unmarshal(rawRev, &n.rev)
		}
		lock.nodes[name] = n
	}
	return lock, nil
}

func parseEdge(raw json.RawMessage) edge {
	var pin string
	if err := json.Unmarshal(raw, &pin); err == nil {
		return edge{pin: pin, kind: edgePin}
	}
	var follows []string
	if err := json.Unmarshal(raw, &follows); err == nil && follows != nil {
		return edge{follows: follows, kind: edgeFollows}
	}
	return edge{}
}

// UpdatePaths returns every reachable, directly pinned input path whose final
// component is one of names, as slash-joined paths from root, sorted and
// deduplicated. Only pin edges are followed: a follows edge is neither matched
// nor traversed, which is what makes a root-level redirect such as
// vm/nixpkgs resolve to its canonical target instead of the alias.
//
// The cycle guard is the path of ancestors, not a global visited set, so a
// node reachable two ways is walked twice and its paths deduplicated after.
func (l *Lock) UpdatePaths(names []string) []string {
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	found := make(map[string]bool)
	var walk func(nodeName string, path []string, ancestors []string)
	walk = func(nodeName string, path []string, ancestors []string) {
		for _, ancestor := range ancestors {
			if ancestor == nodeName {
				return
			}
		}
		inputs := l.nodes[nodeName].inputs
		for input, e := range inputs {
			if e.kind != edgePin {
				continue
			}
			next := append(append([]string(nil), path...), input)
			if wanted[input] {
				found[strings.Join(next, "/")] = true
			}
			walk(e.pin, next, append(append([]string(nil), ancestors...), nodeName))
		}
	}
	walk("root", nil, nil)
	paths := make([]string, 0, len(found))
	for path := range found {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// Rev resolves a slash-joined input path from root and returns the locked
// revision of the node it reaches, or "" when the path does not resolve or
// the node has no rev.
//
// A follows edge is resolved as an absolute path from root before the walk
// continues, so a path discovered from the pre-update lock still resolves
// against a post-update lock in which the update collapsed a pin into a
// redirect.
func (l *Lock) Rev(path string) string {
	nodeName, ok := l.follow("root", strings.Split(path, "/"))
	if !ok {
		return ""
	}
	return l.nodes[nodeName].rev
}

func (l *Lock) follow(nodeName string, parts []string) (string, bool) {
	if len(parts) == 0 {
		return nodeName, true
	}
	e, ok := l.nodes[nodeName].inputs[parts[0]]
	if !ok {
		return "", false
	}
	var next string
	switch e.kind {
	case edgePin:
		next = e.pin
	case edgeFollows:
		target, ok := l.follow("root", e.follows)
		if !ok {
			return "", false
		}
		next = target
	default:
		return "", false
	}
	return l.follow(next, parts[1:])
}

// String is a debugging aid.
func (l *Lock) String() string {
	return fmt.Sprintf("flakelock.Lock{%d nodes}", len(l.nodes))
}

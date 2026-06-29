// Package links models SUBMODULE-LINKS.yaml: declared links between submodules
// plus task-level dependency edges, and enforces an acyclic graph. A link lets a
// honeybee on either submodule reference and depend on the other's PLAN.md;
// stepwise cross-repo chains are sequenced by dependency tags. Every tag write
// runs a cycle check so a wait cycle is rejected. Deterministic; no LLM.
package links

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// Links is the parsed SUBMODULE-LINKS.yaml: bidirectional submodule links and
// directed task dependencies (Edge: from depends on to).
type Links struct {
	Submodules []string `yaml:"submodules,omitempty"` // linked submodule names
	Deps       []Edge   `yaml:"deps,omitempty"`       // directed dependency edges
}

// Edge means From depends on To (To must complete before From).
type Edge struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

// Parse reads SUBMODULE-LINKS.yaml text. Empty input is an empty Links.
func Parse(b []byte) (*Links, error) {
	l := &Links{}
	if len(b) == 0 {
		return l, nil
	}
	if err := yaml.Unmarshal(b, l); err != nil {
		return nil, fmt.Errorf("links: parse: %w", err)
	}
	return l, nil
}

// Load reads a SUBMODULE-LINKS.yaml file; missing file is empty Links.
func Load(path string) (*Links, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Links{}, nil
		}
		return nil, err
	}
	return Parse(b)
}

// Save writes the links file deterministically (sorted).
func (l *Links) Save(path string) error {
	sort.Strings(l.Submodules)
	sort.Slice(l.Deps, func(i, j int) bool {
		if l.Deps[i].From != l.Deps[j].From {
			return l.Deps[i].From < l.Deps[j].From
		}
		return l.Deps[i].To < l.Deps[j].To
	})
	b, err := yaml.Marshal(l)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// LinkSubmodules records an undirected link between a and b (idempotent).
func (l *Links) LinkSubmodules(a, b string) {
	add := func(s string) {
		for _, x := range l.Submodules {
			if x == s {
				return
			}
		}
		l.Submodules = append(l.Submodules, s)
	}
	add(a)
	add(b)
}

// AddDep adds a from->to dependency, rejecting it if it creates a cycle.
func (l *Links) AddDep(from, to string) error {
	if from == to {
		return fmt.Errorf("links: self-dependency %q", from)
	}
	for _, e := range l.Deps {
		if e.From == from && e.To == to {
			return nil
		}
	}
	l.Deps = append(l.Deps, Edge{From: from, To: to})
	if c := cycle(l.Deps); c != nil {
		l.Deps = l.Deps[:len(l.Deps)-1]
		return fmt.Errorf("links: dependency %s->%s creates cycle: %v", from, to, c)
	}
	return nil
}

// HasCycle reports whether the dependency graph contains a wait cycle.
func (l *Links) HasCycle() bool { return cycle(l.Deps) != nil }

// cycle returns a node sequence forming a cycle, or nil if acyclic (DFS).
func cycle(edges []Edge) []string {
	adj := map[string][]string{}
	for _, e := range edges {
		adj[e.From] = append(adj[e.From], e.To)
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var path, found []string
	var dfs func(string) bool
	dfs = func(n string) bool {
		color[n] = gray
		path = append(path, n)
		for _, m := range adj[n] {
			if color[m] == gray {
				found = append(append([]string{}, path...), m)
				return true
			}
			if color[m] == white && dfs(m) {
				return true
			}
		}
		path = path[:len(path)-1]
		color[n] = black
		return false
	}
	nodes := make([]string, 0, len(adj))
	for n := range adj {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	for _, n := range nodes {
		if color[n] == white && dfs(n) {
			return found
		}
	}
	return nil
}

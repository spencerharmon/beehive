package selectt

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/spencerharmon/beehive/internal/links"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
)

// Graph is the combined, deterministic cross-submodule dependency graph that the
// selector owns (the plan layer stays links-free). It folds, across every
// submodule:
//
//   - PLAN.md dependency tags, as edges between qualified node ids
//     "<submodule>:<taskid>" (a bare dep id is local and qualified with its own
//     submodule);
//   - any task edges declared in SUBMODULE-LINKS.yaml (links.Deps); and
//   - the submodule link adjacency (links.Submodules) that authorizes one
//     submodule to depend on another.
//
// From this it answers two selection questions: is a candidate task on a wait
// cycle (exclude it), and are its cross-submodule prerequisites satisfied (linked
// and DONE)?
type Graph struct {
	Edges  []links.Edge               // From depends on To; node ids are qualified
	Status map[string]plan.Status     // qualified task id -> status from PLAN.md
	Linked map[string]map[string]bool // submodule -> set of linked submodule names
	cyclic map[string]bool            // qualified ids lying on a cycle
}

// qualifyID returns the graph node id for a dep id d referenced from submodule
// sm. An id already carrying a "<submodule>:" prefix is cross-submodule and used
// verbatim; a bare id is local and qualified with sm.
func qualifyID(sm, d string) string {
	if strings.Contains(d, ":") {
		return d
	}
	return sm + ":" + d
}

// splitID splits a qualified node id into its submodule and task parts. ok is
// false for an unqualified (local) id.
func splitID(id string) (submodule, task string, ok bool) {
	i := strings.Index(id, ":")
	if i < 0 {
		return "", id, false
	}
	return id[:i], id[i+1:], true
}

// LoadEdges builds the combined graph by reading every submodule's
// SUBMODULE-LINKS.yaml and PLAN.md. Missing, dormant, or unparsed-absent plans
// contribute nothing; a present-but-malformed PLAN.md surfaces its parse error.
func LoadEdges(rp *repo.Repo) (*Graph, error) {
	subs, err := rp.Submodules()
	if err != nil {
		return nil, err
	}
	g := &Graph{
		Status: map[string]plan.Status{},
		Linked: map[string]map[string]bool{},
	}
	for _, sm := range subs {
		l, err := links.Load(filepath.Join(sm.Path, repo.LinksFile))
		if err != nil {
			return nil, err
		}
		for _, name := range l.Submodules {
			if name == sm.Name {
				continue
			}
			if g.Linked[sm.Name] == nil {
				g.Linked[sm.Name] = map[string]bool{}
			}
			g.Linked[sm.Name][name] = true
		}
		for _, e := range l.Deps {
			g.Edges = append(g.Edges, links.Edge{
				From: qualifyID(sm.Name, e.From),
				To:   qualifyID(sm.Name, e.To),
			})
		}

		b, err := os.ReadFile(sm.PlanPath())
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		pl, err := plan.Parse(string(b))
		if err != nil {
			return nil, err
		}
		for _, t := range pl.Tasks {
			node := sm.Name + ":" + t.ID
			g.Status[node] = t.Status
			for _, d := range t.Deps {
				g.Edges = append(g.Edges, links.Edge{
					From: node,
					To:   qualifyID(sm.Name, d),
				})
			}
		}
	}
	g.cyclic = links.CyclicNodes(g.Edges)
	return g, nil
}

// Validate returns a node sequence forming a wait cycle in the combined graph, or
// nil when the graph is acyclic. Used by `beehive lint` and the pre-commit guard
// to reject a PLAN.md dep-tag commit that forms a cross-submodule cycle.
func (g *Graph) Validate() []string { return links.Cycle(g.Edges) }

// InCycle reports whether the qualified node id lies on a wait cycle.
func (g *Graph) InCycle(node string) bool { return g.cyclic[node] }

// crossDepSatisfied reports whether a dependency d of a task in submodule sm is
// satisfied as far as cross-submodule resolution is concerned. Local deps (no
// "<submodule>:" prefix) are the plan layer's responsibility and always pass
// here. A cross-submodule dep is satisfied only when sm declares a link to the
// target submodule (authorization) and the target task is DONE.
func (g *Graph) crossDepSatisfied(sm, d string) bool {
	tsm, _, ok := splitID(d)
	if !ok {
		return true
	}
	if !g.Linked[sm][tsm] {
		return false
	}
	return g.Status[d] == plan.StatusDone
}

// CrossDepSatisfied is the exported form of crossDepSatisfied: it reports whether
// a cross-submodule dependency d of a task in submodule sm is authorized (sm is
// linked to the target submodule) AND the target task is DONE. A bare local dep
// (no "<submodule>:" prefix) always returns true here — local readiness is the
// plan layer's (plan.Blocked / plan.Selectable) responsibility. Used by the
// runner to recognize a work pass that deliberately yielded by filing a blocking
// cross-submodule dependency on itself.
func (g *Graph) CrossDepSatisfied(sm, d string) bool { return g.crossDepSatisfied(sm, d) }

// LinkedTo reports whether submodule a is authorized to depend on submodule b
// (SUBMODULE-LINKS.yaml records the link). Exposed so `beehive task block` can
// reject a cross-submodule dep the link graph does not authorize before it is
// ever written into a PLAN.md.
func (g *Graph) LinkedTo(a, b string) bool { return g.Linked[a][b] }

// TaskStatus returns the recorded status of a qualified node id
// ("<submodule>:<taskid>"), and whether that task exists in the loaded graph.
// Exposed so `beehive task block` can confirm the dependency task it is about to
// point at actually exists (never link to a dangling id).
func (g *Graph) TaskStatus(node string) (plan.Status, bool) {
	s, ok := g.Status[node]
	return s, ok
}

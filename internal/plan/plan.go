// Package plan parses and writes submodule PLAN.md task lists and implements the
// task-type state machine, heartbeat/TTL math, dependency gating, and cycle
// detection used by selection, claiming, and the turn loop. PLAN.md is the only
// truth; nothing here invents state.
package plan

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Status is a task lifecycle state.
type Status string

const (
	TODO        Status = "TODO"
	InProgress  Status = "IN-PROGRESS"
	NeedsReview Status = "NEEDS-REVIEW"
	NeedsArb    Status = "NEEDS-ARBITRATION"
	NeedsHuman  Status = "NEEDS-HUMAN"
	Done        Status = "DONE"
)

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	switch s {
	case TODO, InProgress, NeedsReview, NeedsArb, NeedsHuman, Done:
		return true
	}
	return false
}

// roiStamp matches the reconcile marker recording the last reconciled ROI commit.
var roiStamp = regexp.MustCompile(`Beehive-ROI:\s*([0-9a-f]+)`)

// taskLine matches one task: "- ID | STATUS | text | key:val | key:val".
// ID and STATUS are mandatory; trailing pipe fields are key:value metadata.
var taskLine = regexp.MustCompile(`^-\s+(\S+)\s*\|\s*([A-Z-]+)\s*(?:\|(.*))?$`)

// Task is one PLAN.md item. Type is derived from Status, never stored.
type Task struct {
	ID       string
	Status   Status
	Text     string
	Deps     []string  // task ids (same plan or "submodule:id")
	Attempts int       // rejection counter
	Weight   int       // selection weight, default 1
	TS       time.Time // IN-PROGRESS heartbeat; zero when terminal
}

// Plan is a parsed PLAN.md.
type Plan struct {
	ROIStamp string
	Tasks    []Task
	Header   []string // verbatim non-task header lines (incl. stamp)
}

// Parse reads PLAN.md text into a Plan, preserving header lines for round-trip.
func Parse(text string) (*Plan, error) {
	p := &Plan{}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		m := taskLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			if mm := roiStamp.FindStringSubmatch(line); mm != nil {
				p.ROIStamp = mm[1]
			}
			p.Header = append(p.Header, line)
			continue
		}
		t := Task{ID: m[1], Status: Status(m[2]), Weight: 1}
		if !t.Status.Valid() {
			return nil, fmt.Errorf("plan: bad status %q on task %q", m[2], t.ID)
		}
		for _, f := range strings.Split(m[3], "|") {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			k, v, ok := strings.Cut(f, ":")
			if !ok {
				if t.Text == "" {
					t.Text = f
				}
				continue
			}
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			switch k {
			case "deps":
				for _, d := range strings.Split(v, ",") {
					if d = strings.TrimSpace(d); d != "" {
						t.Deps = append(t.Deps, d)
					}
				}
			case "attempts":
				n, err := strconv.Atoi(v)
				if err != nil {
					return nil, fmt.Errorf("plan: task %q attempts %q: %w", t.ID, v, err)
				}
				t.Attempts = n
			case "weight":
				n, err := strconv.Atoi(v)
				if err != nil {
					return nil, fmt.Errorf("plan: task %q weight %q: %w", t.ID, v, err)
				}
				t.Weight = n
			case "ts":
				ts, err := time.Parse(time.RFC3339, v)
				if err != nil {
					return nil, fmt.Errorf("plan: task %q ts %q: %w", t.ID, v, err)
				}
				t.TS = ts
			default:
				if t.Text == "" {
					t.Text = f
				}
			}
		}
		p.Tasks = append(p.Tasks, t)
	}
	return p, nil
}

// String renders the plan back to PLAN.md text round-trippably.
func (p *Plan) String() string {
	var b strings.Builder
	for _, h := range p.Header {
		b.WriteString(h)
		b.WriteByte('\n')
	}
	for _, t := range p.Tasks {
		b.WriteString(t.line())
		b.WriteByte('\n')
	}
	return b.String()
}

func (t Task) line() string {
	parts := []string{"- " + t.ID, string(t.Status), t.Text}
	if len(t.Deps) > 0 {
		parts = append(parts, "deps:"+strings.Join(t.Deps, ","))
	}
	if t.Attempts > 0 {
		parts = append(parts, "attempts:"+strconv.Itoa(t.Attempts))
	}
	if t.Weight > 1 {
		parts = append(parts, "weight:"+strconv.Itoa(t.Weight))
	}
	if !t.TS.IsZero() {
		parts = append(parts, "ts:"+t.TS.UTC().Format(time.RFC3339))
	}
	return strings.Join(parts, " | ")
}

// Find returns a pointer to the task with id, or nil.
func (p *Plan) Find(id string) *Task {
	for i := range p.Tasks {
		if p.Tasks[i].ID == id {
			return &p.Tasks[i]
		}
	}
	return nil
}

// Stale reports whether an IN-PROGRESS heartbeat is older than ttl (GC candidate).
func (t Task) Stale(now time.Time, ttl time.Duration) bool {
	return t.Status == InProgress && !t.TS.IsZero() && now.Sub(t.TS) > ttl
}

// depsDone reports whether every dependency of t resolves DONE within p.
// Cross-submodule deps ("sm:id") cannot be resolved here and gate as unmet.
func (p *Plan) depsDone(t Task) bool {
	for _, d := range t.Deps {
		if strings.Contains(d, ":") {
			return false
		}
		dep := p.Find(d)
		if dep == nil || dep.Status != Done {
			return false
		}
	}
	return true
}

// HasCycle reports whether the same-plan dependency graph contains a cycle.
func (p *Plan) HasCycle() bool {
	return len(p.cyclic()) > 0
}

// cyclic returns the set of task ids participating in any dependency cycle.
func (p *Plan) cyclic() map[string]bool {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	bad := map[string]bool{}
	var visit func(id string) bool
	visit = func(id string) bool {
		color[id] = gray
		inCycle := false
		t := p.Find(id)
		if t != nil {
			for _, d := range t.Deps {
				if strings.Contains(d, ":") {
					continue
				}
				switch color[d] {
				case gray:
					bad[d] = true
					inCycle = true
				case white:
					if p.Find(d) != nil && visit(d) {
						bad[d] = true
						inCycle = true
					}
				}
			}
		}
		color[id] = black
		if inCycle {
			bad[id] = true
		}
		return inCycle
	}
	ids := make([]string, len(p.Tasks))
	for i, t := range p.Tasks {
		ids[i] = t.ID
	}
	sort.Strings(ids)
	for _, id := range ids {
		if color[id] == white {
			visit(id)
		}
	}
	return bad
}

// Candidates returns selectable tasks of the highest-priority available type
// (GC > arbitration > review > main), excluding NEEDS-HUMAN, tasks with unmet
// deps, and tasks tangled in a dependency cycle. now/ttl drive GC detection.
func (p *Plan) Candidates(now time.Time, ttl time.Duration) []Task {
	cyc := p.cyclic()
	var gc, arb, rev, main []Task
	for _, t := range p.Tasks {
		if cyc[t.ID] || t.Status == NeedsHuman {
			continue
		}
		switch {
		case t.Stale(now, ttl):
			gc = append(gc, t)
		case t.Status == NeedsArb:
			arb = append(arb, t)
		case t.Status == NeedsReview:
			rev = append(rev, t)
		case t.Status == TODO && p.depsDone(t):
			main = append(main, t)
		}
	}
	switch {
	case len(gc) > 0:
		return gc
	case len(arb) > 0:
		return arb
	case len(rev) > 0:
		return rev
	default:
		return main
	}
}

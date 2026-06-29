// Package plan parses and rewrites PLAN.md: the per-submodule task list, its
// status state machine, ROI reconcile stamp, heartbeat timestamps, attempt
// counters, dependency tags, and TTL math. Deterministic; no LLM.
//
// PLAN.md format (line-oriented, stable round-trip):
//
//	<!-- Beehive-ROI: <sha> -->
//	# Plan
//
//	## <id> [<STATUS>] <!-- attempts=N deps=a,b heartbeat=<RFC3339> -->
//	free-form body lines...
//
// The ROI stamp is the first comment; tasks are H2 headers carrying a metadata
// comment. Body lines between headers belong to the preceding task.
package plan

import (
	"bufio"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Status is a task state. The machine is:
//
//	TODO -> IN-PROGRESS -> NEEDS-REVIEW -> {DONE | NEEDS-ARBITRATION}
//	NEEDS-ARBITRATION -> {TODO | DONE}
//	rejections > limit -> NEEDS-HUMAN (terminal, frontend-only)
type Status string

const (
	StatusTODO       Status = "TODO"
	StatusInProgress Status = "IN-PROGRESS"
	StatusReview     Status = "NEEDS-REVIEW"
	StatusArb        Status = "NEEDS-ARBITRATION"
	StatusDone       Status = "DONE"
	StatusHuman      Status = "NEEDS-HUMAN"
)

var allStatuses = map[Status]bool{
	StatusTODO: true, StatusInProgress: true, StatusReview: true,
	StatusArb: true, StatusDone: true, StatusHuman: true,
}

// Task is one PLAN.md item.
type Task struct {
	ID        string
	Title     string
	Status    Status
	Attempts  int
	Deps      []string
	Weight    int       // selection weight, default 1
	Heartbeat time.Time // zero when not IN-PROGRESS
	Body      []string  // body lines verbatim, without trailing blank
}

// Plan is a parsed PLAN.md.
type Plan struct {
	ROI    string // Beehive-ROI stamp sha, "" if none
	Header []string
	Tasks  []*Task
}

var (
	stampRe  = regexp.MustCompile(`<!--\s*Beehive-ROI:\s*([0-9a-f]*)\s*-->`)
	headerRe = regexp.MustCompile(`^##\s+(\S+)\s+\[([A-Z-]+)\](?:\s+<!--\s*(.*?)\s*-->)?\s*$`)
)

// Parse reads PLAN.md text.
func Parse(s string) (*Plan, error) {
	p := &Plan{}
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var cur *Task
	for sc.Scan() {
		line := sc.Text()
		if cur == nil {
			if m := stampRe.FindStringSubmatch(line); m != nil {
				p.ROI = m[1]
				p.Header = append(p.Header, line)
				continue
			}
		}
		if m := headerRe.FindStringSubmatch(line); m != nil {
			t, err := parseHeader(m)
			if err != nil {
				return nil, err
			}
			cur = t
			p.Tasks = append(p.Tasks, t)
			continue
		}
		if cur == nil {
			p.Header = append(p.Header, line)
		} else {
			cur.Body = append(cur.Body, line)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	for _, t := range p.Tasks {
		t.Body = trimTrailingBlank(t.Body)
	}
	p.Header = trimTrailingBlank(p.Header)
	return p, nil
}

func parseHeader(m []string) (*Task, error) {
	st := Status(m[2])
	if !allStatuses[st] {
		return nil, fmt.Errorf("plan: unknown status %q for task %s", m[2], m[1])
	}
	t := &Task{ID: m[1], Status: st}
	for _, kv := range strings.Fields(m[3]) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch k {
		case "attempts":
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("plan: bad attempts %q for %s", v, t.ID)
			}
			t.Attempts = n
		case "deps":
			if v != "" {
				t.Deps = strings.Split(v, ",")
			}
		case "weight":
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("plan: bad weight %q for %s", v, t.ID)
			}
			t.Weight = n
		case "heartbeat":
			ts, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return nil, fmt.Errorf("plan: bad heartbeat %q for %s", v, t.ID)
			}
			t.Heartbeat = ts
		}
	}
	return t, nil
}

func trimTrailingBlank(ls []string) []string {
	for len(ls) > 0 && strings.TrimSpace(ls[len(ls)-1]) == "" {
		ls = ls[:len(ls)-1]
	}
	return ls
}

// String serializes a plan deterministically; Parse(p.String()) round-trips.
func (p *Plan) String() string {
	var b strings.Builder
	if len(p.Header) > 0 {
		b.WriteString(strings.Join(p.Header, "\n"))
		b.WriteString("\n")
	}
	for _, t := range p.Tasks {
		b.WriteString("\n")
		b.WriteString(t.header())
		b.WriteString("\n")
		if len(t.Body) > 0 {
			b.WriteString(strings.Join(t.Body, "\n"))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (t *Task) header() string {
	meta := fmt.Sprintf("attempts=%d deps=%s", t.Attempts, strings.Join(t.Deps, ","))
	if t.Weight > 1 {
		meta += fmt.Sprintf(" weight=%d", t.Weight)
	}
	if !t.Heartbeat.IsZero() {
		meta += " heartbeat=" + t.Heartbeat.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("## %s [%s] <!-- %s -->", t.ID, t.Status, meta)
}

// Stamp sets the Beehive-ROI sha, inserting the comment if absent.
func (p *Plan) Stamp(sha string) {
	line := "<!-- Beehive-ROI: " + sha + " -->"
	for i, h := range p.Header {
		if stampRe.MatchString(h) {
			p.Header[i] = line
			p.ROI = sha
			return
		}
	}
	p.Header = append([]string{line}, p.Header...)
	p.ROI = sha
}

// Task returns the task with id, or nil.
func (p *Plan) Task(id string) *Task {
	for _, t := range p.Tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

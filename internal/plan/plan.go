// Package plan parses and rewrites PLAN.md: the per-submodule task list, its
// status state machine, ROI reconcile stamp, heartbeat timestamps, attempt
// counters, dependency tags, and TTL math. Deterministic; no LLM.
//
// PLAN.md format (line-oriented, stable round-trip):
//
//	<!-- Beehive-ROI: <sha> -->
//	# Plan
//
//	## <id> [<STATUS>] <!-- attempts=N deps=a,b session=<id> heartbeat=<RFC3339> -->
//	free-form body lines...
//	Human-needed: concrete blocker/reason (only when status is NEEDS-HUMAN)
//
// The ROI stamp is the first comment; tasks are H2 headers carrying a metadata
// comment. Body lines between headers belong to the preceding task. A task is
// "active" (being worked right now) when it carries a session id and a heartbeat
// fresh within the TTL — independent of its status. There is no IN-PROGRESS
// status: every status can be actively worked. (Legacy `[IN-PROGRESS]` headers
// parse as TODO for backward compatibility.)
package plan

import (
	"bufio"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const humanReasonPrefix = "Human-needed:"

// Status is a task state. The machine is:
//
//	TODO -> NEEDS-REVIEW -> {DONE | NEEDS-ARBITRATION}
//	NEEDS-ARBITRATION -> {TODO | DONE}
//	rejections > limit -> NEEDS-HUMAN (terminal)
//	explicit human request -> NEEDS-HUMAN (terminal, with Human-needed reason)
//
// "In progress" is NOT a status: a task being worked keeps its phase status
// (TODO while implementing, NEEDS-REVIEW while under review, ...) and is marked
// active by a session id + fresh heartbeat instead.
type Status string

const (
	StatusTODO   Status = "TODO"
	StatusReview Status = "NEEDS-REVIEW"
	StatusArb    Status = "NEEDS-ARBITRATION"
	StatusDone   Status = "DONE"
	StatusHuman  Status = "NEEDS-HUMAN"
	// StatusInProgress is retained only to normalize legacy PLAN.md headers on
	// parse (-> TODO). It is never produced or selected.
	StatusInProgress Status = "IN-PROGRESS"
)

var allStatuses = map[Status]bool{
	StatusTODO: true, StatusReview: true,
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
	Session   string    // owner's unique claim token; "" when unclaimed
	Heartbeat time.Time // last claim stamp; zero when unclaimed
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
	// Legacy normalization: IN-PROGRESS is no longer a status; an in-progress task
	// is now a TODO carrying a session+heartbeat. Map it so old PLAN.md files load.
	if st == StatusInProgress {
		st = StatusTODO
	}
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
		case "session":
			t.Session = v
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
	if t.Session != "" {
		meta += " session=" + t.Session
	}
	if !t.Heartbeat.IsZero() {
		meta += " heartbeat=" + t.Heartbeat.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("## %s [%s] <!-- %s -->", t.ID, t.Status, meta)
}

// HumanReason returns the current reason a task is blocked for operator input,
// recorded as a body field so humans can read/edit it directly in PLAN.md.
func (t *Task) HumanReason() string {
	for _, line := range t.Body {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), humanReasonPrefix); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func (t *Task) setHumanReason(reason string) {
	reason = oneLine(reason)
	field := humanReasonPrefix + " " + reason
	for i, line := range t.Body {
		if _, ok := strings.CutPrefix(strings.TrimSpace(line), humanReasonPrefix); ok {
			t.Body[i] = field
			return
		}
	}
	if len(t.Body) > 0 && strings.TrimSpace(t.Body[len(t.Body)-1]) != "" {
		t.Body = append(t.Body, "")
	}
	t.Body = append(t.Body, field)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

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

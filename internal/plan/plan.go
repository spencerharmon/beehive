// Package plan parses and rewrites PLAN.md: the per-submodule task list, its
// status state machine, ROI reconcile stamp, heartbeat timestamps, attempt
// counters, dependency tags, and TTL math. Deterministic; no LLM.
//
// PLAN.md format (line-oriented, stable round-trip):
//
//	<!-- Beehive-ROI: <sha> -->
//	# Plan
//
//	## <id> [<STATUS>] <!-- attempts=N deps=a,b session=<id> heartbeat=<RFC3339> not_before=<RFC3339> category=<cat> -->
//	free-form body lines...
//	Human-needed: concrete blocker/reason (only when status is NEEDS-HUMAN),
//	  optionally continued by immediately-following non-blank lines (e.g.
//	  bullets naming the blocker/needed input) up to the next blank line
//
// The ROI stamp is the first comment; tasks are H2 headers carrying a metadata
// comment. Body lines between headers belong to the preceding task. A task is
// "active" (being worked right now) when it carries a session id and a heartbeat
// fresh within the TTL — independent of its status. There is no IN-PROGRESS
// status: every status can be actively worked. (Legacy `[IN-PROGRESS]` headers
// parse as TODO for backward compatibility.) The optional `not_before=<RFC3339>`
// stamp is a wall-clock gate: a TODO task is held out of selection (same as an
// unmet dep) until now reaches it, then it is normally selectable — a general
// delay primitive (backoff, TTL wait, spaced re-check/retry) a task or the runner
// may set/refresh on itself.
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

// checkPrefix / verifyAfterMergePrefix are the body-field labels carrying a task's
// definition-of-done commands (docs/dod-verification-spec.md). Commands are
// multi-line and cannot live in the header comment (parsed with strings.Fields),
// so they are body fields spanning the label line plus following non-blank lines
// — the same span rule as Human-needed:. `Check:` is the DoD command whose exit 0
// is "satisfied"; `Verify-After-Merge:` is the DoD command whose effect only
// exists after the change is merged (its presence marks the task merge-gated).
const checkPrefix = "Check:"
const verifyAfterMergePrefix = "Verify-After-Merge:"

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

// Category is the machine-readable class of a NEEDS-HUMAN escalation. The set is
// EXHAUSTIVE: a honeybee-initiated escalation (RequestHuman / `beehive task
// human`) must carry exactly one of these, and nothing that fails to fit one is a
// legitimate NEEDS-HUMAN. It exists so the operator UI can lead with the single
// relevant ask and show only that category's resolution affordance instead of a
// generic blob, and so a honeybee cannot farm ordinary in-authority work (an
// in-cluster restart, a cache clear, a reversible internal choice) out to a human
// by mislabeling it. Serialized as `category=<value>` in the task header comment;
// absent on a runner-forced escalation (Reject/Strand/RecoverLostWork retry
// overflow) or a legacy pre-category task, which the UI renders as unclassified.
type Category string

const (
	// CatSecret: a credential/secret only the operator can supply (PAT, password,
	// private key, token, API key). The ask is a store key to populate.
	CatSecret Category = "secret"
	// CatExternalPermission: an action on infrastructure the beehive does NOT
	// control — host-root on a node, a physical/hardware/vendor action, a
	// registrar/cloud/DNS change outside the repo, any out-of-GitOps, out-of-
	// cluster op. In-cluster kubectl against workloads this swarm deploys is NOT
	// this — that is the swarm's own job.
	CatExternalPermission Category = "external-permission"
	// CatContradiction: the ROI is internally self-contradictory, the ROI and
	// PLAN conflict, or two linked-submodule ROIs oppose, and the honeybee cannot
	// tell which side is authoritative. The ask is which intent wins.
	CatContradiction Category = "contradiction"
	// CatArchitecture: a high-level design decision with a lasting, hard-to-
	// reverse, user-visible consequence (wire format, on-disk schema, public API,
	// a fork where picking wrong forces a later breaking change). The ask is
	// which option, with the user-visible consequence of each.
	CatArchitecture Category = "architecture"
)

var allCategories = map[Category]bool{
	CatSecret: true, CatExternalPermission: true,
	CatContradiction: true, CatArchitecture: true,
}

// Valid reports whether c is one of the four legitimate escalation categories.
// The empty category is NOT valid: a honeybee-initiated escalation must classify
// itself. (Runner-forced overflow escalations set NEEDS-HUMAN directly, outside
// the category-gated completion checks, and are allowed to carry no category.)
func (c Category) Valid() bool { return allCategories[c] }

// Categories returns the four legitimate categories in canonical order (for CLI
// help, validation messages, and UI enumeration).
func Categories() []Category {
	return []Category{CatSecret, CatExternalPermission, CatContradiction, CatArchitecture}
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
	// NotBefore, when non-zero, is a wall-clock gate: a TODO task is NOT ready
	// for selection until now >= NotBefore (same effect as an unmet dep), then it
	// becomes normally selectable. A general delay primitive (backoff, TTL wait,
	// spaced re-check/retry) a task or the runner may set/refresh on itself. Zero
	// means no gate. Serialized as `not_before=<RFC3339>` in the header comment.
	NotBefore time.Time
	// ReviewCommit, when non-empty, is the submodule commit sha a completed Work
	// pass handed to review — the exact commit its NEEDS-REVIEW gitlink bump
	// points at, recorded DURABLY on the task by the runner (Claimer.
	// RecordReviewCommit) the moment the work lands NEEDS-REVIEW. It survives the
	// disposable bee-<taskid> branch being reclaimed or reused, so the runner can
	// still recognize "this task's work was already merged into tracked main" (an
	// interrupted review that landed the merge but not the DONE bookkeeping) after
	// the branch is gone — without it, the vanished branch is misread as lost work
	// and the task loops. Tested for ancestry-of-main, never trusted as a status.
	// Serialized as `review=<sha>` in the header comment.
	ReviewCommit string
	// Commits is the agent-authored, gate-verified list of submodule commit shas
	// this session produced (the session-attribution tag the handoff protocol
	// requires on EVERY terminal flip — Work/Review/Arbitrate). It is distinct
	// from ReviewCommit (which the RUNNER records post-hoc for the review-
	// reachability/finalize machinery): Commits is what the AGENT declares and the
	// runner's handoff gate verifies exists in the submodule before accepting the
	// flip, so a flip can never reference a phantom/bad-object commit. CommitsSet
	// distinguishes "declared none" (CommitsSet && len==0) from "tag absent"
	// (!CommitsSet), which the gate treats as an unmet requirement. Serialized as
	// `commits=<sha>[,<sha>...]` or `commits=none` in the header comment.
	Commits    []string
	CommitsSet bool
	// HumanCategory, when set, is the machine-readable class of a NEEDS-HUMAN
	// escalation (see Category). Set by RequestHuman on a honeybee-initiated
	// escalation (always one of the four valid values) and cleared by Resolve.
	// Empty on a runner-forced overflow escalation or a legacy task. Serialized as
	// `category=<value>` in the header comment.
	HumanCategory Category
	// CheckNone records an explicit, justified declaration that this task has NO
	// machine-checkable definition of done (mirrors CommitsSet's `commits=none`
	// forcing function). It makes the ABSENCE of a check a visible, reviewable
	// decision rather than a silent gap: a task may enter DONE only if its `Check:`
	// body command passes OR CheckNone is set. Mutually exclusive with a `Check:`
	// body field (a task carrying both is a parse defect). The justification is the
	// adjacent body prose (review-enforced). Serialized as `check=none` in the
	// header comment. See docs/dod-verification-spec.md.
	CheckNone bool
	Body      []string // body lines verbatim, without trailing blank
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
	// DoD schema validation: `check=none` (justified absence) and a `Check:` body
	// command are mutually exclusive — a task carrying both contradicts itself about
	// whether it has a machine-checkable definition of done.
	for _, t := range p.Tasks {
		if t.CheckNone && t.Check() != "" {
			return nil, fmt.Errorf("plan: task %s declares both `check=none` and a `Check:` body command — pick one", t.ID)
		}
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
		case "not_before":
			ts, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return nil, fmt.Errorf("plan: bad not_before %q for %s", v, t.ID)
			}
			t.NotBefore = ts
		case "review":
			t.ReviewCommit = v
		case "commits":
			t.CommitsSet = true
			if v != "" && v != "none" {
				t.Commits = strings.Split(v, ",")
			}
		case "check":
			// The header token only ever carries the justified-absence flag; a
			// real check COMMAND lives in the `Check:` body field (it has spaces the
			// Fields-split comment cannot hold). Any value other than `none` is a
			// malformed plan.
			if v != "none" {
				return nil, fmt.Errorf("plan: bad check=%q for %s (the only valid header value is `check=none`; a real check command goes in the `Check:` body field)", v, t.ID)
			}
			t.CheckNone = true
		case "category":
			// Stored verbatim; validity is enforced at the write/completion
			// boundary (RequestHuman, the CLI, the runner completion checks), not
			// on parse, so a legacy/unknown value still round-trips rather than
			// failing the whole plan to load.
			t.HumanCategory = Category(v)
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

// Card renders this task's PLAN.md card — its H2 header line plus its body —
// exactly as Plan.String() emits it. The runner injects the card verbatim in the
// precomputed honeybee brief so the agent gets its task definition without
// re-reading and re-parsing PLAN.md. It reuses the canonical header serialization,
// so there is one source of truth for the card format.
func (t *Task) Card() string {
	var b strings.Builder
	b.WriteString(t.header())
	b.WriteByte('\n')
	if len(t.Body) > 0 {
		b.WriteString(strings.Join(t.Body, "\n"))
		b.WriteByte('\n')
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
	if !t.NotBefore.IsZero() {
		meta += " not_before=" + t.NotBefore.UTC().Format(time.RFC3339)
	}
	if t.ReviewCommit != "" {
		meta += " review=" + t.ReviewCommit
	}
	if t.CommitsSet {
		if len(t.Commits) == 0 {
			meta += " commits=none"
		} else {
			meta += " commits=" + strings.Join(t.Commits, ",")
		}
	}
	if t.HumanCategory != "" {
		meta += " category=" + string(t.HumanCategory)
	}
	if t.CheckNone {
		meta += " check=none"
	}
	return fmt.Sprintf("## %s [%s] <!-- %s -->", t.ID, t.Status, meta)
}

// humanReasonSpan locates the Human-needed field's line range [start, end) in
// t.Body: the line carrying the "Human-needed:" prefix plus any immediately
// following non-blank lines — the structured bullets HONEYBEE.md's escalation
// guidance asks agents to write (a one-line summary plus bullets naming the
// concrete blocker/needed input) — stopping at the first blank line or the end
// of the body. Returns start == -1 when no such field exists. HumanReason,
// setHumanReason, and clearHumanReason all share this so the three stay in
// agreement about the field's extent (plan-view-detail-polish).
func (t *Task) humanReasonSpan() (start, end int) {
	for i, line := range t.Body {
		if _, ok := strings.CutPrefix(strings.TrimSpace(line), humanReasonPrefix); ok {
			j := i + 1
			for j < len(t.Body) && strings.TrimSpace(t.Body[j]) != "" {
				j++
			}
			return i, j
		}
	}
	return -1, -1
}

// HumanReason returns the current reason a task is blocked for operator input,
// recorded as a body field so humans can read/edit it directly in PLAN.md. A
// structured reason (see humanReasonSpan) spans the "Human-needed:" line and
// every immediately-following non-blank line, joined back with newlines, so a
// view can render the whole thing as markdown (e.g. a summary plus bullets)
// instead of only ever its first line.
func (t *Task) HumanReason() string {
	start, end := t.humanReasonSpan()
	if start == -1 {
		return ""
	}
	first := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t.Body[start]), humanReasonPrefix))
	if end == start+1 {
		return first
	}
	lines := append([]string{first}, t.Body[start+1:end]...)
	return strings.Join(lines, "\n")
}

func (t *Task) setHumanReason(reason string) {
	reason = oneLine(reason)
	field := humanReasonPrefix + " " + reason
	if start, end := t.humanReasonSpan(); start != -1 {
		rest := append([]string{}, t.Body[end:]...)
		t.Body = append(t.Body[:start:start], field)
		t.Body = append(t.Body, rest...)
		return
	}
	if len(t.Body) > 0 && strings.TrimSpace(t.Body[len(t.Body)-1]) != "" {
		t.Body = append(t.Body, "")
	}
	t.Body = append(t.Body, field)
}

// clearHumanReason drops the Human-needed body field — including any
// structured continuation lines (see humanReasonSpan) — and a trailing blank
// line it may have introduced, when a NEEDS-HUMAN task is resolved, so a
// reopened task does not carry a stale blocker reason. A no-op when no such
// field exists.
func (t *Task) clearHumanReason() {
	start, end := t.humanReasonSpan()
	if start == -1 {
		return
	}
	t.Body = append(t.Body[:start:start], t.Body[end:]...)
	t.Body = trimTrailingBlank(t.Body)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// bodyFieldLabels are the recognized structured body-field prefixes. A field's
// multi-line span (bodyFieldSpan) stops at the next one so adjacent fields do not
// bleed into each other (e.g. `Check:` must not absorb a following
// `Verify-After-Merge:` line).
var bodyFieldLabels = []string{
	checkPrefix, verifyAfterMergePrefix, humanReasonPrefix,
	"Files:", "Doc:", "Accept:", "Review:", "Design:", "Human:",
}

// startsBodyField reports whether a body line (already trimmed) opens a recognized
// structured field.
func startsBodyField(trimmed string) bool {
	for _, p := range bodyFieldLabels {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return false
}

// bodyFieldSpan locates a labeled body field's line range [start, end) in t.Body:
// the line carrying `prefix` plus every immediately-following non-blank line that
// does NOT open another recognized field (startsBodyField), stopping at the first
// blank line, the next field label, or the end of the body. Returns start == -1
// when no such field exists. Shared by the DoD command accessors (Check,
// VerifyAfterMerge); a command may span several continuation lines but never
// swallow a sibling field.
func (t *Task) bodyFieldSpan(prefix string) (start, end int) {
	for i, line := range t.Body {
		if _, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
			j := i + 1
			for j < len(t.Body) {
				trimmed := strings.TrimSpace(t.Body[j])
				if trimmed == "" || startsBodyField(trimmed) {
					break
				}
				j++
			}
			return i, j
		}
	}
	return -1, -1
}

// bodyField returns the verbatim content of a labeled body field: the text after
// `prefix` on its line, joined with any continuation lines by newlines, trimmed.
// "" when the field is absent.
func (t *Task) bodyField(prefix string) string {
	start, end := t.bodyFieldSpan(prefix)
	if start == -1 {
		return ""
	}
	first := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t.Body[start]), prefix))
	if end == start+1 {
		return first
	}
	lines := t.Body[start+1 : end]
	if first == "" {
		return strings.Join(lines, "\n")
	}
	return strings.Join(append([]string{first}, lines...), "\n")
}

// Check returns the task's DoD command (the `Check:` body field), or "" if none.
// Its exit 0 is the machine definition of done, enforced by the runner's handoff
// gate on entering DONE. See docs/dod-verification-spec.md.
func (t *Task) Check() string { return t.bodyField(checkPrefix) }

// VerifyAfterMerge returns the task's post-merge DoD command (the
// `Verify-After-Merge:` body field), or "" if none. Its presence marks the task's
// effect merge-gated: the live-effect DoD is carried by a runner-spawned successor
// check task rather than verified in the work session.
func (t *Task) VerifyAfterMerge() string { return t.bodyField(verifyAfterMergePrefix) }

// CheckDeclared reports whether the task has made an explicit definition-of-done
// decision: either a real `Check:` command or a justified `check=none`. A task
// that reaches a state requiring the decision without one is a defect (lint-
// flagged / gate-refused per surface).
func (t *Task) CheckDeclared() bool { return t.CheckNone || t.Check() != "" }

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

// DanglingDeps returns, per task, the LOCAL dependency ids (same-plan, unqualified
// — no ":") that name no task in this plan. A dangling dep is never satisfiable:
// it makes the dependent task permanently "blocked" (Blocked treats an absent dep
// as unmet) and, worse, lets a work pass fake a legitimate dep-yield against a
// task that will never exist — the exact defect that wedged
// flux:phantom-library-bluegreen-repin-gitea-images on the nonexistent
// jellyfin:jellyfin-image-build. Cross-submodule deps (qualified, "<sm>:<id>")
// are resolved against the link graph, not here (see the selection graph /
// `beehive plan lint`). The map is keyed by task id; only tasks WITH a dangling
// local dep appear. See docs/dod-verification-spec.md.
func (p *Plan) DanglingDeps() map[string][]string {
	out := map[string][]string{}
	for _, t := range p.Tasks {
		for _, d := range t.Deps {
			if strings.Contains(d, ":") {
				continue // cross-submodule: resolved against the link graph elsewhere
			}
			if p.Task(d) == nil {
				out[t.ID] = append(out[t.ID], d)
			}
		}
	}
	return out
}

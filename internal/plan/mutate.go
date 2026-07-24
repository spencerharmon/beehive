package plan

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// idRe validates a task id / a dep's task part: the token that appears after
// `## ` in a header and after a `<submodule>:` prefix in a dep. It must be a
// single word free of whitespace and of the two dependency-list metacharacters
// (`,` separates deps, `:` qualifies a cross-submodule dep), so a value that
// round-trips through the header comment unambiguously. Kept deliberately narrow
// (the ids the swarm actually mints are lowercase-kebab like
// `zuul-build-publish-image-base-job`) while still allowing digits, dots, and
// underscores that show up in practice.
var idRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidID reports whether id is a well-formed task id (see idRe).
func ValidID(id string) bool { return idRe.MatchString(id) }

// depRe validates a dependency reference: either a bare local id or a qualified
// cross-submodule `<submodule>:<taskid>` (each part a ValidID token). The plan
// layer only checks SHAPE; whether the referenced task/link actually exists and
// is DONE is the selector's cross-submodule graph responsibility.
var depRe = regexp.MustCompile(`^[A-Za-z0-9._-]+(:[A-Za-z0-9._-]+)?$`)

// ValidDep reports whether d is a well-formed dependency reference (bare local
// id, or qualified `<submodule>:<taskid>`).
func ValidDep(d string) bool { return depRe.MatchString(d) }

// NewTask builds a fresh TODO task for insertion into a plan. id must be a valid
// task id; every dep must be a valid dependency reference; weight defaults to 1
// when < 1. body is the task card's body lines (the description a honeybee reads
// as its `## Your task`), stored verbatim. Attempts start at 0 and the task
// carries no claim.
func NewTask(id string, deps []string, weight int, body []string) (*Task, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("plan: invalid task id %q (allowed: %s)", id, idRe.String())
	}
	clean := make([]string, 0, len(deps))
	for _, d := range deps {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if !ValidDep(d) {
			return nil, fmt.Errorf("plan: invalid dep %q on task %s (allowed: <taskid> or <submodule>:<taskid>)", d, id)
		}
		clean = append(clean, d)
	}
	if weight < 1 {
		weight = 1
	}
	return &Task{
		ID:     id,
		Status: StatusTODO,
		Deps:   clean,
		Weight: weight,
		Body:   trimTrailingBlank(append([]string(nil), body...)),
	}, nil
}

// AddTask appends t to the plan. It errors on a duplicate id so a honeybee filing
// a newly-discovered dependency task can never silently collide with (and thereby
// mask or corrupt) an existing task of the same id.
func (p *Plan) AddTask(t *Task) error {
	if t == nil {
		return fmt.Errorf("plan: AddTask nil task")
	}
	if !ValidID(t.ID) {
		return fmt.Errorf("plan: invalid task id %q", t.ID)
	}
	if p.Find(t.ID) != nil {
		return fmt.Errorf("plan: task %q already exists", t.ID)
	}
	p.Tasks = append(p.Tasks, t)
	return nil
}

// AddDep adds a dependency reference to the task, idempotently. It returns
// (added, err): added is false when the dep was already present (a no-op) and
// err is non-nil for a malformed dep. Used by `beehive task block` when a work
// pass discovers it depends on a task it just filed.
func (t *Task) AddDep(dep string) (bool, error) {
	dep = strings.TrimSpace(dep)
	if !ValidDep(dep) {
		return false, fmt.Errorf("plan: invalid dep %q (allowed: <taskid> or <submodule>:<taskid>)", dep)
	}
	for _, d := range t.Deps {
		if d == dep {
			return false, nil
		}
	}
	t.Deps = append(t.Deps, dep)
	return true, nil
}

// SetCheck appends a `Check:` body field carrying the task's definition-of-done
// command (its exit 0 is the machine DoD the runner's handoff gate enforces on
// entering DONE). Errors if the task already declared `check=none` (mutually
// exclusive) or already carries a Check. Used by `beehive task add --check`.
func (t *Task) SetCheck(cmd string) error {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return fmt.Errorf("plan: empty check command")
	}
	if t.CheckNone {
		return fmt.Errorf("plan: task %s cannot carry both a Check and check=none", t.ID)
	}
	if s, _ := t.bodyFieldSpan(checkPrefix); s != -1 {
		return fmt.Errorf("plan: task %s already has a Check", t.ID)
	}
	t.Body = append(t.Body, checkPrefix+" "+cmd)
	return nil
}

// SetVerifyAfterMerge appends a `Verify-After-Merge:` body field carrying the
// task's post-merge DoD command. Its presence marks the task's effect merge-gated
// (verified by a runner-spawned successor check task, not in the work session).
// Used by `beehive task add --verify-after-merge`.
func (t *Task) SetVerifyAfterMerge(cmd string) error {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return fmt.Errorf("plan: empty verify-after-merge command")
	}
	if s, _ := t.bodyFieldSpan(verifyAfterMergePrefix); s != -1 {
		return fmt.Errorf("plan: task %s already has a Verify-After-Merge", t.ID)
	}
	t.Body = append(t.Body, verifyAfterMergePrefix+" "+cmd)
	return nil
}

// Defer records a convergence-wait self-defer: it sets not_before to until (the
// wall-clock the task becomes selectable again) and increments the defer counter
// that MaxDefers bounds. It is the mutation behind `beehive task defer` and the
// runner's own re-check scheduling: "did the work, the world has not converged,
// re-check after `until`." Errors on a zero/past `until` (a defer must move the
// gate into the future). The task stays TODO; the caller releases its claim.
func (t *Task) Defer(until, now time.Time) error {
	if until.IsZero() || !until.After(now) {
		return fmt.Errorf("plan: defer until %s is not in the future (now %s)", until.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	if t.Status != StatusTODO {
		return fmt.Errorf("plan: only a TODO task may be deferred; %s is %s", t.ID, t.Status)
	}
	t.NotBefore = until
	t.Defers++
	return nil
}

// Blocked reports whether a TODO task is currently held OUT of selection by an
// unmet LOCAL dependency — a dep naming a same-plan task that is absent or not
// DONE. Cross-submodule deps (containing ":") are the selector's graph
// responsibility and are NOT judged here (Selectable defers them the same way);
// the caller combines this with the cross-submodule check. It is the inverse of
// the local half of Selectable, exposed so the runner can recognize a work pass
// that deliberately yielded by filing a blocking dependency on itself.
func (p *Plan) Blocked(t *Task) bool {
	for _, d := range t.Deps {
		if strings.Contains(d, ":") {
			continue
		}
		dep := p.Task(d)
		if dep == nil || dep.Status != StatusDone {
			return true
		}
	}
	return false
}

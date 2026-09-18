package web

// This file implements chat-dances: a registry of named, invocable maintenance
// dances surfaced on the beehive frontend. Each dance offers a deterministic,
// read-only DRY-RUN (a plan of exactly what it would change) and a separate APPLY
// that performs precisely that change. Destructive dances refuse to apply without
// an explicit confirmation, and an unknown dance name is a hard error — the four
// acceptance guarantees for this feature.
//
// The registry is the single lookup/dispatch point: plan() stamps a dance's
// identity onto its dry-run and apply() enforces the invocation contract
// (unknown -> error, report-only -> no apply, destructive -> confirm-gated)
// before any mutation runs. Dances close over *Server so their plans are live
// scans and their applies reuse the server's guarded git write path.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/artifacts"
	"github.com/spencerharmon/beehive/internal/editor"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
)

// ---- JSON API (browser-free clients, e.g. beemacs) ----
//
// These mirror the HTML /dances/{name}/plan and /dances/{name}/apply routes
// exactly — same registry, same invocation guards (unknown -> 404, report-only
// apply -> 400, destructive apply without confirm -> 200 with a
// distinguishable "confirmation required" fact) — so the JSON and HTML
// surfaces can never drift; only the response encoding differs (writeJSON
// instead of s.render).

// danceDiffJSON is the JSON encoding of one proposed file rewrite.
type danceDiffJSON struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// dancePlanJSON is the JSON encoding of a dancePlan's dry-run result.
type dancePlanJSON struct {
	Empty bool            `json:"empty"`
	Diffs []danceDiffJSON `json:"diffs"`
}

func newDancePlanJSON(p dancePlan) dancePlanJSON {
	diffs := make([]danceDiffJSON, 0, len(p.Diffs))
	for _, d := range p.Diffs {
		if d == nil {
			continue
		}
		diffs = append(diffs, danceDiffJSON{Path: d.Path, Before: d.Before, After: d.After})
	}
	return dancePlanJSON{Empty: p.Empty(), Diffs: diffs}
}

// apiDancePlanHandler is the JSON mirror of dancePlanHandler: runs a dance's
// deterministic dry-run and returns its identity/flags plus the plan. An
// unknown dance is a 404. It mutates nothing.
func (s *Server) apiDancePlan(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	reg := s.dances()
	sk, p, err := reg.plan(r.Context(), name)
	if err != nil {
		if errors.Is(err, errUnknownDance) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":        sk.Name,
		"title":       sk.Title,
		"destructive": sk.Destructive,
		"reportOnly":  sk.ReportOnly,
		"plan":        newDancePlanJSON(p),
	})
}

// apiDanceApply is the JSON mirror of danceApplyHandler: applies a dance after
// the registry's invocation guards, accepting the same confirm form/query
// param. An unknown dance is a 404, a report-only dance is a 400, and a
// destructive dance invoked WITHOUT confirm performs NO mutation and instead
// reports {"confirmRequired": true} alongside a fresh plan at 200 (the JSON
// analogue of the HTML confirm-gate re-render).
func (s *Server) apiDanceApply(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	reg := s.dances()
	sk, res, err := reg.apply(r.Context(), name, isTrue(r.FormValue("confirm")))
	if err != nil {
		switch {
		case errors.Is(err, errUnknownDance):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.Is(err, errReportOnly):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		case errors.Is(err, errConfirmRequired):
			payload := map[string]interface{}{
				"confirmRequired": true,
				"error":           err.Error(),
			}
			if _, p, perr := reg.plan(r.Context(), name); perr == nil {
				payload["plan"] = newDancePlanJSON(p)
			}
			writeJSON(w, http.StatusOK, payload)
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}
	payload := map[string]interface{}{
		"name":   sk.Name,
		"result": res,
	}
	if _, p, perr := reg.plan(r.Context(), name); perr == nil {
		payload["plan"] = newDancePlanJSON(p)
	}
	writeJSON(w, http.StatusOK, payload)
}

// The invocation-guard errors. They are sentinels (wrapped, matched with
// errors.Is) so the HTTP layer maps each to a distinct status without string
// matching, and so a caller can tell "no such dance" apart from "you must
// confirm" — the difference between a 404 and a refusal-to-mutate.
var (
	errUnknownDance    = errors.New("unknown dance")
	errReportOnly      = errors.New("dance is report-only and has no apply action")
	errConfirmRequired = errors.New("dance is destructive and requires explicit confirmation")
)

// danceAction is one concrete mutation a dance's apply would perform, surfaced in
// the dry-run so an operator sees exactly what will change before approving.
type danceAction struct {
	Op     string // "remove" | "reclaim" | "write"
	Target string // the path / branch / id acted on
	Detail string // human explanation (no action implied by itself)
}

// danceDiff is a proposed whole-file rewrite of ONE target's file (e.g. a single
// submodule's INFRASTRUCTURE.md), rendered as a unified diff so a file-editing
// dance previews its change like the chat editor. A plan carries one per file it
// would touch, each scoped to exactly that target.
type danceDiff struct {
	Path   string
	Before string
	After  string
}

// changed reports whether the proposed rewrite actually differs from the current
// file — a no-op normalization proposes nothing.
func (d *danceDiff) changed() bool { return d != nil && d.Before != d.After }

// dancePlan is the deterministic dry-run of a dance: a read-only description of
// what applying WOULD do, computed without mutating anything. The identity/flag
// fields are stamped by the registry, not the plan closure.
type dancePlan struct {
	Dance       string
	Title       string
	Summary     string
	Destructive bool
	ReportOnly  bool

	Report  []string      // informational findings (report-only dances, or "nothing to do")
	Actions []danceAction // the concrete mutations apply would perform
	Diffs   []*danceDiff  // proposed file rewrites, one per target file the dance would edit
}

// Empty reports whether the plan would change nothing: no actions and no real
// diff. The panel uses it to show "already clean" and suppress the apply control.
func (p dancePlan) Empty() bool {
	if len(p.Actions) > 0 {
		return false
	}
	for _, d := range p.Diffs {
		if d.changed() {
			return false
		}
	}
	return true
}

// danceResult is the outcome of applying a dance: the concrete changes made.
type danceResult struct {
	Dance string
	Done  []string
}

// dance is one named, invocable maintenance action. plan computes the read-only
// dry-run; apply performs it. A destructive dance's apply is gated on an explicit
// confirm by the REGISTRY (apply below), never by the closure, so every dance's
// mutation path is protected uniformly.
type dance struct {
	Name        string
	Title       string
	Summary     string
	Destructive bool
	ReportOnly  bool
	plan        func(ctx context.Context) (dancePlan, error)
	apply       func(ctx context.Context) (danceResult, error)
}

// danceRegistry is the ordered set of dances with name lookup. It is rebuilt per
// request (cheap: just closures) so every plan is a live scan.
type danceRegistry struct {
	order  []string
	byName map[string]*dance
}

// list returns the dances in registration order (deterministic for the index).
func (r *danceRegistry) list() []*dance {
	out := make([]*dance, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.byName[n])
	}
	return out
}

// lookup resolves a dance by name, returning errUnknownDance (wrapped with the
// name) when absent — the acceptance's "unknown dance errors".
func (r *danceRegistry) lookup(name string) (*dance, error) {
	sk, ok := r.byName[name]
	if !ok {
		return nil, fmt.Errorf("%q: %w", name, errUnknownDance)
	}
	return sk, nil
}

// plan runs a dance's dry-run and stamps the dance's identity/flags onto the
// result, so plan closures compute only Report/Actions/Diff. It mutates nothing.
func (r *danceRegistry) plan(ctx context.Context, name string) (*dance, dancePlan, error) {
	sk, err := r.lookup(name)
	if err != nil {
		return nil, dancePlan{}, err
	}
	p, err := sk.plan(ctx)
	if err != nil {
		return sk, dancePlan{}, err
	}
	p.Dance, p.Title, p.Summary = sk.Name, sk.Title, sk.Summary
	p.Destructive, p.ReportOnly = sk.Destructive, sk.ReportOnly
	return sk, p, nil
}

// apply runs a dance's mutation AFTER enforcing the invocation contract: an
// unknown dance errors, a report-only dance has nothing to apply, and a
// destructive dance refuses without an explicit confirm. Each guard returns its
// sentinel error with NO side effect, so "no destructive action without
// approval" holds structurally — the closure never runs on a guard failure.
func (r *danceRegistry) apply(ctx context.Context, name string, confirm bool) (*dance, danceResult, error) {
	sk, err := r.lookup(name)
	if err != nil {
		return nil, danceResult{}, err
	}
	if sk.ReportOnly || sk.apply == nil {
		return sk, danceResult{}, errReportOnly
	}
	if sk.Destructive && !confirm {
		return sk, danceResult{}, errConfirmRequired
	}
	res, err := sk.apply(ctx)
	if err != nil {
		return sk, danceResult{}, err
	}
	res.Dance = sk.Name
	return sk, res, nil
}

// dances builds the maintenance-dance registry over this server. The set and
// order are deterministic; each dance closes over s for its live scan/apply.
func (s *Server) dances() *danceRegistry {
	reg := &danceRegistry{byName: map[string]*dance{}}
	add := func(sk *dance) {
		reg.order = append(reg.order, sk.Name)
		reg.byName[sk.Name] = sk
	}
	add(s.danceCleanupStale())
	add(s.danceGC())
	add(s.danceResources())
	add(s.danceInfraConventions())
	add(s.danceRepairPlan())
	add(s.dancePruneEmptySessions())
	return reg
}

// danceRepairPlan surfaces the plan-repair operation (dances/repair-plan.md) as a
// deterministic dry-run + confirm-gated apply. It targets exactly one corruption
// class: a task header carrying an EMPTY-valued session=/heartbeat=/not_before=
// stamp — the signature a pass killed mid-write (e.g. OOM) leaves behind, which
// makes time.Parse("") reject the whole document (`plan: bad heartbeat ""`) and
// blocks selection/reconcile/every view for that submodule. It only ever acts on
// a PLAN.md that genuinely fails to parse, drops the empty stamps via
// plan.RepairCorruptStamps (canonical form omits an empty session and a zero
// heartbeat/not_before, so a dead claim is simply released), and REFUSES to guess
// at malformed structural counters or discard non-empty real-but-corrupt values —
// those surface as report items for manual repair. Destructive: it rewrites
// PLAN.md, so apply is confirm-gated and re-verifies the repaired file parses
// before writing or publishing.
func (s *Server) danceRepairPlan() *dance {
	return &dance{
		Name:        "repair-plan",
		Title:       "Repair a corrupt PLAN.md",
		Summary:     "Surgically drop the empty-valued session=/heartbeat=/not_before= stamp a crashed pass leaves behind (the `plan: bad heartbeat \"\"` corruption) so the submodule's PLAN.md parses again. Refuses to guess at malformed structural counters or discard non-empty real values.",
		Destructive: true,
		plan: func(ctx context.Context) (dancePlan, error) {
			subs, err := s.repo.Submodules()
			if err != nil {
				return dancePlan{}, err
			}
			var p dancePlan
			for _, sm := range subs {
				before, err := readFileOrEmpty(sm.PlanPath())
				if err != nil {
					return dancePlan{}, err
				}
				if before == "" {
					continue
				}
				// Only act on a genuinely unparseable file — a healthy PLAN.md is
				// never rewritten by this dance.
				if _, perr := plan.Parse(before); perr == nil {
					continue
				}
				display := filepath.ToSlash(filepath.Join("submodules", sm.Name, "PLAN.md"))
				after, changed, unfixable := plan.RepairCorruptStamps(before)
				for _, u := range unfixable {
					p.Report = append(p.Report, display+": "+u)
				}
				if len(changed) == 0 {
					if _, perr := plan.Parse(before); perr != nil {
						p.Report = append(p.Report, fmt.Sprintf("%s: unparseable and not an auto-fixable empty-stamp corruption (%v) — manual repair needed", display, perr))
					}
					continue
				}
				// Never propose a repair that does not actually restore parseability.
				if _, perr := plan.Parse(after); perr != nil {
					p.Report = append(p.Report, fmt.Sprintf("%s: empty stamps dropped but file still unparseable (%v) — manual repair needed", display, perr))
					continue
				}
				p.Diffs = append(p.Diffs, &danceDiff{Path: display, Before: before, After: after})
				for _, c := range changed {
					p.Actions = append(p.Actions, danceAction{
						Op:     "write",
						Target: fmt.Sprintf("%s:%d", display, c.Line),
						Detail: fmt.Sprintf("drop empty %s stamp on task %s (releases a dead claim)", strings.Join(c.Dropped, "+"), c.ID),
					})
				}
			}
			if len(p.Actions) == 0 && len(p.Report) == 0 {
				p.Report = append(p.Report, "every submodule's PLAN.md parses; nothing to repair")
			}
			return p, nil
		},
		apply: func(ctx context.Context) (danceResult, error) {
			subs, err := s.repo.Submodules()
			if err != nil {
				return danceResult{}, err
			}
			var res danceResult
			wrote := false
			for _, sm := range subs {
				path := sm.PlanPath()
				before, err := readFileOrEmpty(path)
				if err != nil {
					return danceResult{}, err
				}
				if before == "" {
					continue
				}
				if _, perr := plan.Parse(before); perr == nil {
					continue
				}
				after, changed, _ := plan.RepairCorruptStamps(before)
				if len(changed) == 0 {
					continue
				}
				// Refuse to write a file the repair did not actually make parseable.
				if _, perr := plan.Parse(after); perr != nil {
					return danceResult{}, fmt.Errorf("submodules/%s/PLAN.md: repair did not restore parseability, not writing: %w", sm.Name, perr)
				}
				if err := os.WriteFile(path, []byte(after), 0o644); err != nil {
					return danceResult{}, err
				}
				for _, c := range changed {
					res.Done = append(res.Done, fmt.Sprintf("submodules/%s/PLAN.md:%d dropped empty %s stamp on task %s", sm.Name, c.Line, strings.Join(c.Dropped, "+"), c.ID))
				}
				wrote = true
			}
			if !wrote {
				return danceResult{Done: []string{"no PLAN.md needed empty-stamp repair"}}, nil
			}
			if err := s.publishMain(ctx, "frontend: repair corrupt PLAN.md empty stamps"); err != nil {
				return danceResult{}, err
			}
			return res, nil
		},
	}
}

// isEmptyTranscript reports whether a session .md is a recorded transcript that
// captured ZERO turns. Two shapes qualify, both left by a pass that spawned but
// did no work (a task stuck in a re-selection loop accretes thousands — observed
// live: one flux task, ~6200 empty transcripts):
//
//   - header + metadata + `## user` with a whitespace-only body; and
//   - header + metadata only, with NO `## ` turn heading at all (the pass died
//     before even the first turn marker was written).
//
// A live/abandoned streaming STUB (still pointing at its session branch) is NOT
// empty — its content may yet land, and while it streams main carries only the
// stub — so stubs are excluded. A non-stub transcript, by the streaming model, is
// already finalized (the sweep merged its branch), so an empty one is final and
// its content unrecoverable. Only a recognized `# session ` transcript is ever
// treated as prunable, so an unrelated .md is never touched.
func isEmptyTranscript(content string) bool {
	if strings.TrimSpace(content) == "" {
		return true
	}
	if _, isStub := repo.ParseSessionStub(content); isStub {
		return false
	}
	if !strings.HasPrefix(strings.TrimLeft(content, " \t\r\n\ufeff"), "# session ") {
		return false // not a recognized session transcript: never ours to touch
	}
	// The turns live under `## user`/`## assistant` headings. No `## ` heading at
	// all => the pass recorded nothing (header + metadata only) => empty.
	idx := strings.Index(content, "\n## ")
	if idx < 0 {
		return true
	}
	// A `## ` heading exists: everything after that first (`## user`) heading line
	// must be blank for the transcript to be a zero-turn one.
	rest := content[idx+1:]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	} else {
		rest = ""
	}
	return strings.TrimSpace(rest) == ""
}

// isWarningOnlyTranscript reports whether a session .md recorded NO agent work,
// only a runner-emitted notice — the shape a work pass leaves when it ends with
// its task still TODO and the completion check fails: header + metadata + a lone
// `## ⚠️ warning` (`task <id> left TODO but the completion check failed — left
// for review`) and no `## user`/`## assistant` turn at all. Its only information
// (that the task's check failed) is already durable in the task's PLAN status and
// attempts= tag, so no agent work is lost by pruning it — it is the same no-work
// clutter class as an empty transcript. A transcript that ALSO carries a real
// agent turn (`## assistant`, or a `## user` task-context block) is NOT
// warning-only and is kept: the criterion is a `## ` heading present but NONE of
// them a `## user`/`## assistant` agent turn. Stubs and non-transcripts excluded.
func isWarningOnlyTranscript(content string) bool {
	if _, isStub := repo.ParseSessionStub(content); isStub {
		return false
	}
	if !strings.HasPrefix(strings.TrimLeft(content, " \t\r\n\ufeff"), "# session ") {
		return false // not a recognized session transcript: never ours to touch
	}
	sawHeading, sawNotice := false, false
	for _, ln := range strings.Split(content, "\n") {
		if !strings.HasPrefix(ln, "## ") {
			continue
		}
		sawHeading = true
		h := strings.TrimSpace(strings.TrimPrefix(ln, "## "))
		// An agent turn disqualifies it — real work was recorded.
		if h == "user" || h == "assistant" || strings.HasPrefix(h, "user ") || strings.HasPrefix(h, "assistant ") {
			return false
		}
		if strings.Contains(h, "warning") {
			sawNotice = true
		}
	}
	// At least one heading, none an agent turn, and a runner warning present.
	return sawHeading && sawNotice
}

// emptyTranscriptsIn returns, for one submodule's sessions dir, the absolute
// paths of every zero-turn transcript and a per-task tally (task id -> count)
// keyed by stripping the `-<epoch>-<n>.md` suffix off each filename. Read-only.
func emptyTranscriptsIn(dir string) (files []string, byTask map[string]int, err error) {
	ents, derr := os.ReadDir(dir)
	if derr != nil {
		if os.IsNotExist(derr) {
			return nil, nil, nil
		}
		return nil, nil, derr
	}
	byTask = map[string]int{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil, nil, rerr
		}
		if !isEmptyTranscript(string(b)) {
			continue
		}
		files = append(files, p)
		byTask[emptyTranscriptTaskID(e.Name())]++
	}
	sort.Strings(files)
	return files, byTask, nil
}

// warningOnlyTranscriptsIn returns, for one submodule's sessions dir, the
// absolute paths of every warning-only (no-agent-work) transcript (see
// isWarningOnlyTranscript) and a per-task tally, keyed like emptyTranscriptsIn.
// Read-only.
func warningOnlyTranscriptsIn(dir string) (files []string, byTask map[string]int, err error) {
	ents, derr := os.ReadDir(dir)
	if derr != nil {
		if os.IsNotExist(derr) {
			return nil, map[string]int{}, nil
		}
		return nil, nil, derr
	}
	byTask = map[string]int{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil, nil, rerr
		}
		if !isWarningOnlyTranscript(string(b)) {
			continue
		}
		files = append(files, p)
		byTask[emptyTranscriptTaskID(e.Name())]++
	}
	sort.Strings(files)
	return files, byTask, nil
}

// emptyTranscriptTaskID strips the `-<10-digit-epoch>-<n>.md` session suffix off
// a transcript filename to recover the task id it belongs to, so the report can
// name the looping task driving the empties (the root cause a prune won't fix).
func emptyTranscriptTaskID(name string) string {
	id := strings.TrimSuffix(name, ".md")
	// trailing "-<digits>" (the intra-second counter)
	if i := strings.LastIndexByte(id, '-'); i >= 0 && allDigits(id[i+1:]) {
		id = id[:i]
	}
	// trailing "-<digits>" (the epoch)
	if i := strings.LastIndexByte(id, '-'); i >= 0 && allDigits(id[i+1:]) {
		id = id[:i]
	}
	return id
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// topLoopers renders the up-to-3 highest-count task ids from a tally as report
// lines, so an operator sees WHICH task is generating the empties.
func topLoopers(prefix string, byTask map[string]int) []string {
	type kv struct {
		id string
		n  int
	}
	kvs := make([]kv, 0, len(byTask))
	for id, n := range byTask {
		kvs = append(kvs, kv{id, n})
	}
	sort.Slice(kvs, func(i, j int) bool {
		if kvs[i].n != kvs[j].n {
			return kvs[i].n > kvs[j].n
		}
		return kvs[i].id < kvs[j].id
	})
	var out []string
	for i, e := range kvs {
		if i >= 3 {
			break
		}
		out = append(out, fmt.Sprintf("%s loop: %d × %s", prefix, e.n, e.id))
	}
	return out
}

// orphanedStubMinAge guards orphaned-stub pruning: a stub younger than this is
// kept even if its branch isn't visible, so a just-started session (whose branch
// may not have landed in the primary repo's refs yet) is never nuked. Observed
// orphans are hours-to-months old, so an hour is amply conservative.
const orphanedStubMinAge = time.Hour

// liveSessionBranches returns the set of session-branch names that still exist
// (local heads or remote-tracking). A session's transcript streams to its branch
// `<sm>-<epoch>-<n>-session`; while that branch lives the session may be in
// flight and its stub's content may yet land, so its stub must NOT be pruned.
// Remote-tracking names are recorded both raw (`origin/<b>`) and stripped (`<b>`)
// so a plain stub branch matches regardless of which ref carries it.
func (s *Server) liveSessionBranches(ctx context.Context) (map[string]bool, error) {
	out, err := s.git.Run(ctx, "for-each-ref", "--format=%(refname:short)", "refs/heads/", "refs/remotes/")
	if err != nil {
		return nil, err
	}
	live := map[string]bool{}
	for _, ln := range strings.Split(out, "\n") {
		b := strings.TrimSpace(ln)
		if b == "" {
			continue
		}
		live[b] = true
		if i := strings.IndexByte(b, '/'); i >= 0 {
			live[b[i+1:]] = true // strip the remote prefix (origin/<b> -> <b>)
		}
	}
	return live, nil
}

// orphanedStubsIn returns the session-stub files in dir whose referenced branch
// no longer exists (absent from live) AND that are older than minAge. Such a stub
// is dead: the session ended without the final transcript ever replacing the stub
// and the branch it streamed to is gone, so the content is unrecoverable and the
// stub is pure clutter. A live-branch stub (still streaming) and a too-new stub
// are both kept. byTask tallies the task ids so the report names what generated
// them, exactly as the empty-transcript path does.
func orphanedStubsIn(dir string, live map[string]bool, minAge time.Duration) (files []string, byTask map[string]int, err error) {
	ents, rerr := os.ReadDir(dir)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return nil, map[string]int{}, nil
		}
		return nil, nil, rerr
	}
	byTask = map[string]int{}
	cutoff := time.Now().Add(-minAge)
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil, nil, rerr
		}
		branch, ok := repo.ParseSessionStub(string(b))
		if !ok {
			continue // not a stub
		}
		if live[branch] {
			continue // still streaming: keep
		}
		info, ierr := e.Info()
		if ierr != nil {
			return nil, nil, ierr
		}
		if info.ModTime().After(cutoff) {
			continue // too new: a just-started session, keep
		}
		files = append(files, p)
		byTask[emptyTranscriptTaskID(e.Name())]++
	}
	sort.Strings(files)
	return files, byTask, nil
}

// dancePruneEmptySessions deletes DEAD session files across every submodule's
// sessions/ dir, in three no-recoverable-content classes: (1) zero-turn
// transcripts (see isEmptyTranscript); (2) warning-only transcripts that recorded
// no agent work, only a runner "completion check failed" notice (see
// isWarningOnlyTranscript); and (3) orphaned stubs whose session branch is gone
// (see orphanedStubsIn). Live streaming stubs and any transcript with a real
// agent turn are never touched. The report NAMES the looping/failing task(s)
// generating them, because pruning treats the symptom: until those tasks are
// fixed, new dead files keep accruing. Destructive: it removes tracked files and
// publishes, so apply is confirm-gated, recomputes the set under the git lock,
// and commits+pushes atomically via publishMainLocked.
func (s *Server) dancePruneEmptySessions() *dance {
	return &dance{
		Name:        "prune-empty-sessions",
		Title:       "Prune empty session transcripts",
		Summary:     "Delete DEAD session files across every submodule's sessions/: zero-turn transcripts (a header with an empty body), warning-only transcripts (a pass that recorded no agent work, only a runner 'completion check failed' notice), and orphaned stubs (a streaming stub whose session branch is gone). No recoverable agent work is lost. Live streaming stubs and any transcript with a real agent turn are never touched. The report names the looping/failing task(s) generating them — pruning is symptom-only until those tasks are fixed.",
		Destructive: true,
		plan: func(ctx context.Context) (dancePlan, error) {
			subs, err := s.repo.Submodules()
			if err != nil {
				return dancePlan{}, err
			}
			live, err := s.liveSessionBranches(ctx)
			if err != nil {
				return dancePlan{}, err
			}
			var p dancePlan
			totalEmpty, totalWarn, totalOrphan := 0, 0, 0
			for _, sm := range subs {
				files, byTask, err := emptyTranscriptsIn(sm.SessionsDir())
				if err != nil {
					return dancePlan{}, err
				}
				wfiles, wbyTask, err := warningOnlyTranscriptsIn(sm.SessionsDir())
				if err != nil {
					return dancePlan{}, err
				}
				ofiles, obyTask, err := orphanedStubsIn(sm.SessionsDir(), live, orphanedStubMinAge)
				if err != nil {
					return dancePlan{}, err
				}
				sessRel := filepath.ToSlash(filepath.Join("submodules", sm.Name, "sessions"))
				if len(files) > 0 {
					totalEmpty += len(files)
					p.Actions = append(p.Actions, danceAction{
						Op:     "remove",
						Target: sessRel,
						Detail: fmt.Sprintf("%d zero-turn (no-content) transcript(s)", len(files)),
					})
					p.Report = append(p.Report, topLoopers("submodules/"+sm.Name+":", byTask)...)
				}
				if len(wfiles) > 0 {
					totalWarn += len(wfiles)
					p.Actions = append(p.Actions, danceAction{
						Op:     "remove",
						Target: sessRel,
						Detail: fmt.Sprintf("%d warning-only (no-work) transcript(s)", len(wfiles)),
					})
					p.Report = append(p.Report, topLoopers("submodules/"+sm.Name+" check-fail:", wbyTask)...)
				}
				if len(ofiles) > 0 {
					totalOrphan += len(ofiles)
					p.Actions = append(p.Actions, danceAction{
						Op:     "remove",
						Target: sessRel,
						Detail: fmt.Sprintf("%d orphaned stub(s) (session branch gone)", len(ofiles)),
					})
					p.Report = append(p.Report, topLoopers("submodules/"+sm.Name+" orphan-stub:", obyTask)...)
				}
			}
			if totalEmpty+totalWarn+totalOrphan == 0 {
				p.Report = append(p.Report, "no dead session files (empty, warning-only, or orphaned stub)")
			} else {
				p.Report = append(p.Report, fmt.Sprintf("%d empty + %d warning-only transcript(s) + %d orphaned stub(s) total — pruning is symptom-only; fix the task(s) above so new ones stop accruing", totalEmpty, totalWarn, totalOrphan))
			}
			return p, nil
		},
		apply: func(ctx context.Context) (danceResult, error) {
			// Under the primary-checkout lock: recompute the dead set live, remove each
			// file, then commit+push atomically (publishMainLocked, since we hold gitMu)
			// so the deletion converges and never races a concurrent publish.
			s.gitMu.Lock()
			defer s.gitMu.Unlock()
			subs, err := s.repo.Submodules()
			if err != nil {
				return danceResult{}, err
			}
			live, err := s.liveSessionBranches(ctx)
			if err != nil {
				return danceResult{}, err
			}
			var res danceResult
			removedEmpty, removedWarn, removedOrphan := 0, 0, 0
			for _, sm := range subs {
				files, _, err := emptyTranscriptsIn(sm.SessionsDir())
				if err != nil {
					return danceResult{}, err
				}
				wfiles, _, err := warningOnlyTranscriptsIn(sm.SessionsDir())
				if err != nil {
					return danceResult{}, err
				}
				ofiles, _, err := orphanedStubsIn(sm.SessionsDir(), live, orphanedStubMinAge)
				if err != nil {
					return danceResult{}, err
				}
				all := append(append(append([]string{}, files...), wfiles...), ofiles...)
				for _, f := range all {
					if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
						return danceResult{}, err
					}
				}
				if len(files) > 0 {
					removedEmpty += len(files)
					res.Done = append(res.Done, fmt.Sprintf("removed %d empty transcript(s) from submodules/%s/sessions", len(files), sm.Name))
				}
				if len(wfiles) > 0 {
					removedWarn += len(wfiles)
					res.Done = append(res.Done, fmt.Sprintf("removed %d warning-only transcript(s) from submodules/%s/sessions", len(wfiles), sm.Name))
				}
				if len(ofiles) > 0 {
					removedOrphan += len(ofiles)
					res.Done = append(res.Done, fmt.Sprintf("removed %d orphaned stub(s) from submodules/%s/sessions", len(ofiles), sm.Name))
				}
			}
			if removedEmpty+removedWarn+removedOrphan == 0 {
				return danceResult{Done: []string{"no dead session files to prune"}}, nil
			}
			if err := s.publishMainLocked(ctx, fmt.Sprintf("frontend: prune %d empty + %d warning-only transcript(s) + %d orphaned stub(s)", removedEmpty, removedWarn, removedOrphan)); err != nil {
				return danceResult{}, err
			}
			return res, nil
		},
	}
}

// danceCleanupStale removes the unregistered edit-*/beehive-* worktree
// directories under .worktrees that dead editor sessions and capped passes leave
// behind (the "stale worktrees" hygiene class). Destructive: it deletes
// directories, so its apply is confirm-gated and recomputes the stale set under
// the git lock before touching disk.
func (s *Server) danceCleanupStale() *dance {
	return &dance{
		Name:        "cleanup-stale",
		Title:       "Remove stale worktrees",
		Summary:     "Delete unregistered edit-*/beehive-* worktree directories left under .worktrees by dead editor sessions and capped passes.",
		Destructive: true,
		plan: func(ctx context.Context) (dancePlan, error) {
			items, err := staleWorktrees(ctx, s.repo.Root, s.git)
			if err != nil {
				return dancePlan{}, err
			}
			var p dancePlan
			for _, it := range items {
				p.Actions = append(p.Actions, danceAction{
					Op:     "remove",
					Target: filepath.ToSlash(filepath.Join(".worktrees", it.Name)),
					Detail: it.Detail,
				})
			}
			if len(p.Actions) == 0 {
				p.Report = append(p.Report, "no stale worktrees found")
			}
			return p, nil
		},
		apply: func(ctx context.Context) (danceResult, error) {
			// Serialize against every other primary-checkout mutation, then
			// RECOMPUTE the stale set under the lock so the apply acts on the live
			// state, never a plan that raced a concurrent publish.
			s.gitMu.Lock()
			defer s.gitMu.Unlock()
			items, err := staleWorktrees(ctx, s.repo.Root, s.git)
			if err != nil {
				return danceResult{}, err
			}
			var res danceResult
			for _, it := range items {
				// Guard: a bare basename under .worktrees, never a path — the scan
				// only ever yields basenames, so anything else is not ours to touch.
				if filepath.Base(it.Name) != it.Name {
					continue
				}
				if err := os.RemoveAll(filepath.Join(s.repo.Root, ".worktrees", it.Name)); err != nil {
					return danceResult{}, err
				}
				res.Done = append(res.Done, "removed .worktrees/"+it.Name)
			}
			if len(res.Done) == 0 {
				res.Done = append(res.Done, "no stale worktrees to remove")
			}
			return res, nil
		},
	}
}

// danceGC reclaims abandoned editor worktrees: the edit-* worktrees that are both
// stale (no fresh session record) and clean (no pending unpublished change),
// exactly what the editor's startup Reload prunes. Destructive: it removes
// worktrees + branches, so its apply is confirm-gated and runs under the git lock.
func (s *Server) danceGC() *dance {
	return &dance{
		Name:        "gc",
		Title:       "Reclaim abandoned editor worktrees",
		Summary:     "Remove edit-* editor worktrees that are stale (no fresh session) and clean (no pending change), mirroring the editor's startup reclaim.",
		Destructive: true,
		plan: func(ctx context.Context) (dancePlan, error) {
			branches, err := s.editors.Reclaimable(ctx)
			if err != nil {
				return dancePlan{}, err
			}
			var p dancePlan
			for _, b := range branches {
				p.Actions = append(p.Actions, danceAction{
					Op:     "reclaim",
					Target: b,
					Detail: "stale editor worktree with no pending change",
				})
			}
			if len(p.Actions) == 0 {
				p.Report = append(p.Report, "no reclaimable editor worktrees")
			}
			return p, nil
		},
		apply: func(ctx context.Context) (danceResult, error) {
			// Under the primary-checkout lock (Reload removes worktrees + deletes
			// branches). Snapshot the exact reclaim set first (read-only), then let
			// Reload perform the identical stale/clean reclaim AND re-register the
			// fresh/pending sessions it keeps, so the daemon's in-memory set stays
			// correct — the apply reuses the editor's own tested reclaim, never a
			// second divergent implementation.
			s.gitMu.Lock()
			defer s.gitMu.Unlock()
			branches, err := s.editors.Reclaimable(ctx)
			if err != nil {
				return danceResult{}, err
			}
			if err := s.editors.Reload(ctx); err != nil {
				return danceResult{}, err
			}
			var res danceResult
			for _, b := range branches {
				res.Done = append(res.Done, "reclaimed "+b)
			}
			if len(res.Done) == 0 {
				res.Done = append(res.Done, "no reclaimable editor worktrees")
			}
			return res, nil
		},
	}
}

// danceResources is a read-only inventory of each submodule target's deploy state
// (INFRASTRUCTURE.md) and produced artifacts (ARTIFACTS.md). Blue/green is a
// per-submodule property, so the report scopes every deploy-env line to a named
// submodule and never presents a hive-wide "active env" for the coordination root
// (which is not a deployable target). Report-only: it has no apply, so the
// registry refuses to "apply" it.
func (s *Server) danceResources() *dance {
	return &dance{
		Name:       "resources",
		Title:      "Report infrastructure & artifacts",
		Summary:    "Read-only inventory of each submodule: its own active blue/green deploy env and the produced artifacts.",
		ReportOnly: true,
		plan: func(ctx context.Context) (dancePlan, error) {
			var p dancePlan
			subs, err := s.repo.Submodules()
			if err != nil {
				return dancePlan{}, err
			}
			for _, sm := range subs {
				p.Report = append(p.Report, infraLine(sm.Name, filepath.Join(sm.Path, repo.InfraFile)))
				p.Report = append(p.Report, artifactsLine(sm.Name, filepath.Join(sm.Path, repo.Artifacts)))
			}
			return p, nil
		},
	}
}

// danceInfraConventions normalizes each SUBMODULE's own INFRASTRUCTURE.md so it
// declares the blue/green deploy markers (Active + Environments), filling in the
// conventional defaults only for markers that are ABSENT. Blue/green is a
// per-submodule property, so it acts on every submodule's
// submodules/<name>/INFRASTRUCTURE.md independently and never on the coordination
// root (which is not a deployable target). It is non-destructive (it never removes
// or rewrites an existing marker) and idempotent, so it applies without a confirm —
// but it still previews each edit as a diff and writes via the guarded publish path.
func (s *Server) danceInfraConventions() *dance {
	return &dance{
		Name:    "infra-conventions",
		Title:   "Normalize infrastructure conventions",
		Summary: "Ensure each submodule's INFRASTRUCTURE.md declares its own blue/green deploy markers (Active + Environments), adding the conventional defaults when absent.",
		plan: func(ctx context.Context) (dancePlan, error) {
			subs, err := s.repo.Submodules()
			if err != nil {
				return dancePlan{}, err
			}
			var p dancePlan
			for _, sm := range subs {
				path := filepath.Join(sm.Path, repo.InfraFile)
				before, err := readFileOrEmpty(path)
				if err != nil {
					return dancePlan{}, err
				}
				after := normalizeInfraConventions(before)
				if after == before {
					continue
				}
				display := filepath.ToSlash(filepath.Join("submodules", sm.Name, repo.InfraFile))
				p.Diffs = append(p.Diffs, &danceDiff{Path: display, Before: before, After: after})
				p.Actions = append(p.Actions, danceAction{
					Op:     "write",
					Target: display,
					Detail: "add " + sm.Name + "'s missing blue/green deploy markers",
				})
			}
			if len(p.Actions) == 0 {
				p.Report = append(p.Report, "every submodule's INFRASTRUCTURE.md already declares the blue/green markers")
			}
			return p, nil
		},
		apply: func(ctx context.Context) (danceResult, error) {
			subs, err := s.repo.Submodules()
			if err != nil {
				return danceResult{}, err
			}
			var res danceResult
			wrote := false
			for _, sm := range subs {
				path := filepath.Join(sm.Path, repo.InfraFile)
				before, err := readFileOrEmpty(path)
				if err != nil {
					return danceResult{}, err
				}
				after := normalizeInfraConventions(before)
				if after == before {
					continue
				}
				if err := os.WriteFile(path, []byte(after), 0o644); err != nil {
					return danceResult{}, err
				}
				res.Done = append(res.Done, "wrote "+filepath.ToSlash(filepath.Join("submodules", sm.Name, repo.InfraFile))+" with the conventional deploy markers")
				wrote = true
			}
			if !wrote {
				return danceResult{Done: []string{"every submodule's INFRASTRUCTURE.md already follows conventions"}}, nil
			}
			if err := s.publishMain(ctx, "frontend: normalize submodule INFRASTRUCTURE conventions"); err != nil {
				return danceResult{}, err
			}
			return res, nil
		},
	}
}

// normalizeInfraConventions returns one target's INFRASTRUCTURE.md source with the
// blue/green deploy markers guaranteed present: an absent Active: is filled with
// the default active env and an absent Environments: with the default env set, both
// via the typed artifacts model so the rest of the document round-trips verbatim.
// Already-conventional source is returned unchanged (idempotent). It operates on
// whatever single file the caller hands it — the caller is responsible for scoping
// that to a submodule, never the hive root.
func normalizeInfraConventions(src string) string {
	in := artifacts.ParseInfra(src)
	d := in.Deployment() // resolves defaults for any absent marker
	if in.Active == "" {
		in.SetActive(d.Active)
	}
	if len(in.Envs) == 0 {
		in.SetEnvs(d.Envs)
	}
	return in.String()
}

// infraLine summarizes a target's INFRASTRUCTURE.md deploy state for the
// resources report: the active env and the available set, or an absent/error note.
func infraLine(label, path string) string {
	in, err := artifacts.LoadInfra(path)
	if err != nil {
		return fmt.Sprintf("%s: infrastructure unreadable: %v", label, err)
	}
	if !in.Present() {
		return fmt.Sprintf("%s: no INFRASTRUCTURE.md", label)
	}
	d := in.Deployment()
	return fmt.Sprintf("%s: active %s of [%s]", label, d.Active, strings.Join(d.Envs, ", "))
}

// artifactsLine summarizes a target's ARTIFACTS.md for the resources report: the
// produced artifact names, or an absent/empty/error note.
func artifactsLine(label, path string) string {
	a, err := artifacts.LoadArtifacts(path)
	if err != nil {
		return fmt.Sprintf("%s: artifacts unreadable: %v", label, err)
	}
	if !a.Present() {
		return fmt.Sprintf("%s: no ARTIFACTS.md", label)
	}
	if len(a.Items) == 0 {
		return fmt.Sprintf("%s: ARTIFACTS.md lists no artifacts", label)
	}
	names := make([]string, 0, len(a.Items))
	for _, it := range a.Items {
		names = append(names, it.Name)
	}
	return fmt.Sprintf("%s: artifacts %s", label, strings.Join(names, ", "))
}

// readFileOrEmpty reads path, treating a missing file as empty content (not an
// error), so a normalization dance can propose creating a file from scratch.
func readFileOrEmpty(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(b), nil
}

// isTrue parses a confirm/checkbox form value: the truthy encodings a browser or
// API client sends for an explicit yes. Anything else (including absent) is false,
// so the destructive-confirm gate defaults to REFUSE.
func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

// dancePanel is the view model for one dance's card/panel: identity + flags plus,
// once a dry-run or apply has run, the plan, the rendered per-file diffs, the
// result, and any note/error to surface.
type dancePanel struct {
	Name        string
	Title       string
	Summary     string
	Destructive bool
	ReportOnly  bool

	Plan   *dancePlan
	Diffs  []editor.FileDiffBox
	Result *danceResult
	Note   string
	Err    string

	// Confirming marks the panel as the destructive-confirm gate: apply was
	// invoked without confirmation, so the plan is re-shown with a distinct
	// "confirm and apply" control (which resubmits with confirm set). Nothing was
	// mutated to reach this state.
	Confirming bool
}

// newDancePanel seeds a panel from a dance's static identity (no plan/result yet).
func newDancePanel(sk *dance) dancePanel {
	return dancePanel{
		Name:        sk.Name,
		Title:       sk.Title,
		Summary:     sk.Summary,
		Destructive: sk.Destructive,
		ReportOnly:  sk.ReportOnly,
	}
}

// withPlan attaches a plan (and its rendered per-file diffs) to the panel. Each
// changed file becomes its own editor.FileDiffBox (via RenderMultiFileDiff), so
// a multi-file plan previews as one independently collapsible box per file.
func (p dancePanel) withPlan(plan dancePlan) dancePanel {
	p.Plan = &plan
	changes := make([]editor.FileChange, 0, len(plan.Diffs))
	for _, d := range plan.Diffs {
		if d.changed() {
			// chat-diff-syntax-highlight-bug: precompute per-line
			// syntax-highlighted HTML for BOTH sides so the chat-diff panel
			// renders proper language/token/diff markup instead of plain text.
			// lexer is chosen from the file path; an unknown type yields a nil
			// lexer and highlightLines returns nil, so RenderMultiFileDiff falls
			// back to the plain char-diff exactly as before.
			lexer := lexerFor(d.Path)
			changes = append(changes, editor.FileChange{
				Path:    d.Path,
				Old:     d.Before,
				New:     d.After,
				OldHTML: highlightLines(d.Before, lexer),
				NewHTML: highlightLines(d.After, lexer),
			})
		}
	}
	p.Diffs = editor.RenderMultiFileDiff(changes)
	return p
}

// dancePanels builds the view model for every registered dance (identity + a
// dry-run control, no plan run on load). The combined hygiene page renders these
// beneath the read-only cruft scan, so the diagnostic and its deterministic
// remediations live on one page.
func (s *Server) dancePanels() []dancePanel {
	panels := make([]dancePanel, 0)
	for _, sk := range s.dances().list() {
		panels = append(panels, newDancePanel(sk))
	}
	return panels
}

// dancePlanHandler runs a dance's deterministic dry-run and renders its panel with
// the plan. An unknown dance is a 404. It mutates nothing.
func (s *Server) dancePlanHandler(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	reg := s.dances()
	sk, plan, err := reg.plan(r.Context(), name)
	if err != nil {
		if errors.Is(err, errUnknownDance) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "dance_panel.html", newDancePanel(sk).withPlan(plan))
}

// danceApplyHandler applies a dance after the registry's invocation guards. An
// unknown dance is a 404, a report-only dance is a 400, and a destructive dance
// invoked WITHOUT confirm re-renders the plan with a confirmation prompt and
// performs NO mutation. On success it renders the result plus a fresh post-apply
// plan so the panel reflects the new (typically now-clean) state.
func (s *Server) danceApplyHandler(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	reg := s.dances()
	sk, res, err := reg.apply(r.Context(), name, isTrue(r.FormValue("confirm")))
	if err != nil {
		switch {
		case errors.Is(err, errUnknownDance):
			http.NotFound(w, r)
		case errors.Is(err, errReportOnly):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, errConfirmRequired):
			// No mutation happened. Re-show the plan with a distinct confirm control
			// so applying takes a deliberate second, explicit action; 200 so htmx
			// swaps the gate panel in (the confirm requirement is the message, not
			// an error state).
			panel := newDancePanel(sk)
			panel.Confirming = true
			panel.Note = "Confirmation required: this action is destructive. Confirm to apply."
			if _, plan, perr := reg.plan(r.Context(), name); perr == nil {
				panel = panel.withPlan(plan)
			}
			s.render(w, "dance_panel.html", panel)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	panel := newDancePanel(sk)
	panel.Result = &res
	if _, plan, perr := reg.plan(r.Context(), name); perr == nil {
		panel = panel.withPlan(plan)
	}
	s.render(w, "dance_panel.html", panel)
}

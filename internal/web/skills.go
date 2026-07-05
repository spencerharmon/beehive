package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spencerharmon/beehive/internal/artifacts"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/repo"
)

// chat-skills: named, invocable maintenance skills exposed from the beehived chat
// surface. Each skill runs a deterministic, READ-ONLY dry-run that returns a
// SkillPlan (a human report plus the concrete actions applying it WOULD take);
// nothing is mutated until the operator applies that exact plan. A report-only
// skill carries no actions (there is nothing to apply); a destructive skill's
// apply is gated behind an explicit confirmation — no destructive action ever
// runs without approval. This mirrors the chat-diff-editor-core propose→approve
// loop: the plan is the "proposal", apply is the "approve", and the plan is held
// verbatim between the two so what the operator approves is exactly what runs.

var (
	// errUnknownSkill is returned by the registry for a name it does not know
	// (accept: "unknown skill errors").
	errUnknownSkill = errors.New("skill: unknown skill")
	// errSkillNeedsConfirm is returned when applying a destructive plan without
	// the explicit operator confirmation the approval path requires (accept: "no
	// destructive action without approval").
	errSkillNeedsConfirm = errors.New("skill: destructive action requires confirmation")
	// errNoPendingPlan is returned when apply is called for a skill that has no
	// dry-run plan awaiting approval (the operator must dry-run first).
	errNoPendingPlan = errors.New("skill: no pending plan to apply — run the dry-run first")
)

// skillActionKind enumerates the concrete mutating operations a skill's apply
// dispatches on. Every action a plan can carry is one of these, so the complete
// set of effects "apply" can ever have is bounded and auditable in applyOne.
type skillActionKind int

const (
	// actRemoveWorktreeDir removes a leaked, unregistered worktree directory
	// under <root>/.worktrees/<name> (the gc-worktree-reclaim/editor-session-
	// persist abandoned-worktree leak).
	actRemoveWorktreeDir skillActionKind = iota
	// actRevertRemote removes an unexpected (non-origin) git remote that leaked
	// into the shared repo config (`git remote remove <name>`).
	actRevertRemote
	// actResyncCheckout resets a drifted submodule checkout back to its recorded
	// gitlink SHA (`git -C submodules/<x>/repo reset --hard <sha>`). The checkout
	// is derived state, never authored, so resetting it is safe.
	actResyncCheckout
)

// SkillAction is one concrete, already-decided step in a plan: a human summary
// (shown in the preview) plus the typed operation applyOne performs. A plan is
// computed once in dry-run and applied verbatim, so the operator approves exactly
// the actions that run — there is no re-scan between preview and apply.
type SkillAction struct {
	Summary string          // human-readable, rendered in the plan preview
	Kind    skillActionKind // which operation
	Target  string          // worktree dir name | remote name | submodule path
	SHA     string          // resync only: the recorded gitlink SHA to reset to
}

// SkillPlan is the deterministic result of invoking a skill in dry-run: a one-
// line Summary, read-only Report lines (always safe to show), and the concrete
// Actions applying it WOULD perform. Building a plan MUTATES NOTHING. A plan with
// no Actions is report-only (nothing to apply). Destructive marks a plan whose
// apply performs an irreversible git/filesystem change and therefore needs an
// explicit confirm.
type SkillPlan struct {
	Skill       string
	Title       string
	Summary     string
	Report      []string
	Actions     []SkillAction
	Destructive bool
}

// Actionable reports whether the plan has steps to apply (vs a pure report).
func (p SkillPlan) Actionable() bool { return len(p.Actions) > 0 }

// SkillResult is the outcome of applying a plan: one outcome line per action and
// the done/failed counts, for the panel to render what actually happened.
type SkillResult struct {
	Skill  string
	Lines  []string
	Done   int
	Failed int
}

// Skill is one named, invocable maintenance operation exposed from the chat
// surface. Plan computes a deterministic, read-only preview; the manager owns
// apply (via applySkillActions) so the confirm gate lives in exactly one place.
type Skill interface {
	Name() string
	Title() string
	Summary() string
	Destructive() bool
	Plan(ctx context.Context) (SkillPlan, error)
}

// ---- resources: report the INFRASTRUCTURE.md deploy rigs (read-only) ----

// resourcesSkill reports the INFRASTRUCTURE.md "rigs" — the blue/green deploy
// environments and active env — for the hive root and every submodule. Purely
// diagnostic: it only reads files through the typed artifacts model and never
// mutates, so its plan carries no actions.
type resourcesSkill struct {
	root string
	repo *repo.Repo
}

func (s *resourcesSkill) Name() string      { return "resources" }
func (s *resourcesSkill) Title() string     { return "Infrastructure resources" }
func (s *resourcesSkill) Destructive() bool { return false }
func (s *resourcesSkill) Summary() string {
	return "Report the INFRASTRUCTURE.md deploy rigs (environments + active env) for the hive root and each submodule. Read-only."
}

func (s *resourcesSkill) Plan(ctx context.Context) (SkillPlan, error) {
	p := SkillPlan{Skill: s.Name(), Title: s.Title(), Summary: s.Summary()}
	rootInfra, err := artifacts.LoadInfra(filepath.Join(s.root, repo.InfraFile))
	if err != nil {
		return SkillPlan{}, err
	}
	p.Report = append(p.Report, "hive root: "+infraLine(rootInfra))
	subs, err := s.repo.Submodules()
	if err != nil {
		return SkillPlan{}, err
	}
	for _, sm := range subs {
		in, err := artifacts.LoadInfra(filepath.Join(sm.Path, repo.InfraFile))
		if err != nil {
			return SkillPlan{}, err
		}
		p.Report = append(p.Report, sm.Name+": "+infraLine(in))
	}
	return p, nil
}

// infraLine renders one INFRASTRUCTURE.md rig summary from the RAW parsed markers
// (not the defaulted Deployment), so an absent file and a present-but-unmarked
// file read differently instead of both showing the blue/green fallback.
func infraLine(in artifacts.Infra) string {
	if !in.Present() {
		return "no INFRASTRUCTURE.md"
	}
	if in.Active == "" && len(in.Envs) == 0 {
		return "present, no blue/green markers"
	}
	active := in.Active
	if active == "" {
		active = "(unset)"
	}
	envs := "(unset)"
	if len(in.Envs) > 0 {
		envs = strings.Join(in.Envs, "/")
	}
	return "active " + active + ", environments " + envs
}

// ---- infra-conventions: check INFRASTRUCTURE.md conventions (read-only) ----

// infraConventionsSkill checks each present INFRASTRUCTURE.md against the
// blue/green deploy conventions and reports any breaches. Read-only: it reports
// violations for a human (or a follow-up edit via the chat editor) to fix, and
// never mutates, so its plan carries no actions.
type infraConventionsSkill struct {
	root string
	repo *repo.Repo
}

func (s *infraConventionsSkill) Name() string      { return "infra-conventions" }
func (s *infraConventionsSkill) Title() string     { return "Infrastructure conventions" }
func (s *infraConventionsSkill) Destructive() bool { return false }
func (s *infraConventionsSkill) Summary() string {
	return "Check each INFRASTRUCTURE.md against the blue/green deploy conventions (Active marker set; Active is one of Environments). Read-only report."
}

func (s *infraConventionsSkill) Plan(ctx context.Context) (SkillPlan, error) {
	p := SkillPlan{Skill: s.Name(), Title: s.Title(), Summary: s.Summary()}
	subs, err := s.repo.Submodules()
	if err != nil {
		return SkillPlan{}, err
	}
	violations := 0
	for _, sm := range subs {
		in, err := artifacts.LoadInfra(filepath.Join(sm.Path, repo.InfraFile))
		if err != nil {
			return SkillPlan{}, err
		}
		if !in.Present() {
			continue // no INFRASTRUCTURE.md: a submodule may not deploy — nothing to check
		}
		for _, v := range infraViolations(in) {
			p.Report = append(p.Report, sm.Name+": "+v)
			violations++
		}
	}
	if violations == 0 {
		p.Report = append(p.Report, "all INFRASTRUCTURE.md files conform to the blue/green conventions")
	}
	return p, nil
}

// infraViolations returns the convention breaches in one parsed INFRASTRUCTURE.md.
func infraViolations(in artifacts.Infra) []string {
	var out []string
	if in.Active == "" {
		out = append(out, "missing an `Active:` marker")
	}
	if len(in.Envs) == 0 {
		out = append(out, "missing an `Environments:` marker")
		return out // membership is uncheckable without the environment list
	}
	if in.Active != "" && !contains(in.Envs, in.Active) {
		out = append(out, "active env `"+in.Active+"` is not one of Environments ("+strings.Join(in.Envs, ", ")+")")
	}
	return out
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// ---- gc: reclaim leaked worktree directories (destructive) ----

// gcSkill reclaims the leaked editor/pass worktree directories under
// <root>/.worktrees that git no longer tracks — the abandoned-worktree leak
// gc-worktree-reclaim (capped passes) and editor-session-persist (abandoned edit
// sessions) address. Its dry-run reuses the read-only hive-hygiene staleWorktrees
// scan; apply removes exactly those directories. Destructive → needs confirm.
type gcSkill struct {
	root string
	git  *git.Repo
}

func (s *gcSkill) Name() string      { return "gc" }
func (s *gcSkill) Title() string     { return "Garbage-collect stale worktrees" }
func (s *gcSkill) Destructive() bool { return true }
func (s *gcSkill) Summary() string {
	return "Reclaim leaked editor/pass worktree directories under .worktrees that git no longer tracks (the gc-worktree-reclaim / editor-session-persist sweep). Destructive: requires confirmation."
}

func (s *gcSkill) Plan(ctx context.Context) (SkillPlan, error) {
	p := SkillPlan{Skill: s.Name(), Title: s.Title(), Summary: s.Summary(), Destructive: true}
	stale, err := staleWorktrees(ctx, s.root, s.git)
	if err != nil {
		return SkillPlan{}, err
	}
	if len(stale) == 0 {
		p.Report = append(p.Report, "no stale worktree directories to reclaim")
		return p, nil
	}
	for _, it := range stale {
		p.Actions = append(p.Actions, SkillAction{
			Summary: "remove leaked worktree dir .worktrees/" + it.Name + " (" + it.Detail + ")",
			Kind:    actRemoveWorktreeDir,
			Target:  it.Name,
		})
	}
	return p, nil
}

// ---- cleanup-stale: revert config/checkout drift (destructive) ----

// cleanupStaleSkill reverts the git drift the swarm leaves behind under a shared
// checkout: unexpected (non-origin) remotes that leaked into the shared repo
// config, and submodule checkouts whose HEAD drifted off the recorded gitlink.
// Its dry-run reuses the read-only hive-hygiene remote/checkout scans; apply
// removes each stray remote and resets each drifted checkout to its recorded
// gitlink (derived state, safe to reset). Destructive → needs confirm.
type cleanupStaleSkill struct {
	root string
	git  *git.Repo
}

func (s *cleanupStaleSkill) Name() string      { return "cleanup-stale" }
func (s *cleanupStaleSkill) Title() string     { return "Revert stale git drift" }
func (s *cleanupStaleSkill) Destructive() bool { return true }
func (s *cleanupStaleSkill) Summary() string {
	return "Revert config/checkout drift: remove unexpected (non-origin) remotes and reset drifted submodule checkouts to the recorded gitlink. Destructive: requires confirmation."
}

func (s *cleanupStaleSkill) Plan(ctx context.Context) (SkillPlan, error) {
	p := SkillPlan{Skill: s.Name(), Title: s.Title(), Summary: s.Summary(), Destructive: true}
	remotes, err := unexpectedRemotes(ctx, s.git)
	if err != nil {
		return SkillPlan{}, err
	}
	for _, it := range remotes {
		p.Actions = append(p.Actions, SkillAction{
			Summary: "remove unexpected remote " + it.Name,
			Kind:    actRevertRemote,
			Target:  it.Name,
		})
	}
	drift, err := s.driftedCheckouts(ctx)
	if err != nil {
		return SkillPlan{}, err
	}
	p.Actions = append(p.Actions, drift...)
	if len(p.Actions) == 0 {
		p.Report = append(p.Report, "no config or checkout drift to revert")
	}
	return p, nil
}

// driftedCheckouts scans the declared submodules for a checkout HEAD that has
// drifted off its recorded gitlink and returns a resync action per drift,
// carrying the FULL recorded SHA so apply resets to an exact commit. It reads the
// gitlink SHAs from the index (trackedGitlinks) and each checkout HEAD, mutating
// nothing. An orphan gitlink or a missing/HEAD-less checkout is skipped (not a
// resync target).
func (s *cleanupStaleSkill) driftedCheckouts(ctx context.Context) ([]SkillAction, error) {
	declared, err := declaredGitlinkPaths(ctx, s.git)
	if err != nil {
		return nil, err
	}
	links, err := trackedGitlinks(ctx, s.git)
	if err != nil {
		return nil, err
	}
	var out []SkillAction
	for _, l := range links {
		if !declared[l.Path] {
			continue // orphan gitlink: surfaced elsewhere, not a resync target
		}
		sub := git.New(filepath.Join(s.root, filepath.FromSlash(l.Path)))
		head, err := sub.RevParse(ctx, "HEAD")
		if err != nil {
			continue // not checked out / no HEAD: nothing to reset
		}
		if head == l.SHA {
			continue // already in sync
		}
		out = append(out, SkillAction{
			Summary: "reset checkout " + l.Path + " " + short(head) + " -> recorded gitlink " + short(l.SHA),
			Kind:    actResyncCheckout,
			Target:  l.Path,
			SHA:     l.SHA,
		})
	}
	return out, nil
}

// ---- apply: the single mutation entrypoint ----

// applySkillActions performs exactly the actions in a plan and returns a result.
// It is the ONE place a skill mutates anything, so the confirm gate and the whole
// set of possible effects are enforced/auditable in a single spot. A destructive
// plan is refused (errSkillNeedsConfirm) unless confirm is true. A report-only
// plan (no actions) is a clean no-op. Each action's outcome (done or an error
// line) is recorded; a failing action does not abort the rest (best-effort
// cleanup) and its error is surfaced in the result, never swallowed.
func applySkillActions(ctx context.Context, root string, g *git.Repo, plan SkillPlan, confirm bool) (SkillResult, error) {
	if plan.Destructive && !confirm {
		return SkillResult{}, errSkillNeedsConfirm
	}
	res := SkillResult{Skill: plan.Skill}
	removedWorktree := false
	for _, a := range plan.Actions {
		if err := applyOne(ctx, root, g, a); err != nil {
			res.Lines = append(res.Lines, "FAILED: "+a.Summary+": "+err.Error())
			res.Failed++
			continue
		}
		res.Lines = append(res.Lines, "done: "+a.Summary)
		res.Done++
		if a.Kind == actRemoveWorktreeDir {
			removedWorktree = true
		}
	}
	// After removing worktree dirs, prune git's admin refs so a later `git
	// worktree list` is clean. Idempotent; only worth running if one succeeded.
	if removedWorktree {
		_, _ = g.Run(ctx, "worktree", "prune")
	}
	return res, nil
}

// applyOne performs a single typed action. Every mutating branch is guarded so a
// malformed target can never escape its intended scope (a worktree removal stays
// inside .worktrees; origin is never removed as a "stray" remote).
func applyOne(ctx context.Context, root string, g *git.Repo, a SkillAction) error {
	switch a.Kind {
	case actRemoveWorktreeDir:
		// A leaked, unregistered dir: remove the tree. Require a plain single
		// path segment so the join can never escape .worktrees.
		name := a.Target
		if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
			return fmt.Errorf("unsafe worktree name %q", a.Target)
		}
		return os.RemoveAll(filepath.Join(root, ".worktrees", name))
	case actRevertRemote:
		if a.Target == "" || a.Target == "origin" {
			return fmt.Errorf("refusing to remove remote %q", a.Target)
		}
		_, err := g.Run(ctx, "remote", "remove", a.Target)
		return err
	case actResyncCheckout:
		if a.SHA == "" {
			return fmt.Errorf("resync %q: no recorded gitlink SHA", a.Target)
		}
		clean := filepath.ToSlash(filepath.Clean(a.Target))
		if strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("unsafe submodule path %q", a.Target)
		}
		sub := git.New(filepath.Join(root, filepath.FromSlash(clean)))
		return sub.HardReset(ctx, a.SHA)
	default:
		return fmt.Errorf("unknown skill action kind %d", a.Kind)
	}
}

// ---- registry + manager ----

// skillRegistry is the ordered, name-indexed set of maintenance skills. Lookup of
// an unknown name is an error (accept: "unknown skill errors").
type skillRegistry struct {
	order  []Skill
	byName map[string]Skill
}

// newSkillRegistry builds the registry with the four shipped skills, ordered
// destructive-first so the maintenance-heavy ones lead the surface.
func newSkillRegistry(root string, r *repo.Repo, g *git.Repo) *skillRegistry {
	reg := &skillRegistry{byName: map[string]Skill{}}
	reg.add(&cleanupStaleSkill{root: root, git: g})
	reg.add(&gcSkill{root: root, git: g})
	reg.add(&resourcesSkill{root: root, repo: r})
	reg.add(&infraConventionsSkill{root: root, repo: r})
	return reg
}

func (reg *skillRegistry) add(s Skill) {
	reg.order = append(reg.order, s)
	reg.byName[s.Name()] = s
}

// lookup returns the skill registered under name, or errUnknownSkill.
func (reg *skillRegistry) lookup(name string) (Skill, error) {
	s, ok := reg.byName[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", errUnknownSkill, name)
	}
	return s, nil
}

func (reg *skillRegistry) list() []Skill { return reg.order }

// skillManager exposes the registry to the chat surface. It computes a skill's
// dry-run plan and holds it pending until the operator applies (confirms) or
// re-runs it, so apply performs EXACTLY the previewed plan — no re-scan between
// preview and confirm. Plans are in-memory only (no persistence); a beehived
// restart simply drops any un-applied preview, which is safe because a preview
// mutated nothing.
type skillManager struct {
	reg  *skillRegistry
	root string
	git  *git.Repo

	mu      sync.Mutex
	pending map[string]SkillPlan // skill name -> last actionable dry-run awaiting apply
}

func newSkillManager(root string, r *repo.Repo, g *git.Repo) *skillManager {
	return &skillManager{
		reg:     newSkillRegistry(root, r, g),
		root:    root,
		git:     g,
		pending: map[string]SkillPlan{},
	}
}

// list returns the registered skills (for the surface's index).
func (m *skillManager) list() []Skill { return m.reg.list() }

// plan runs a skill's read-only dry-run and, when the plan is actionable, records
// it as the pending plan for that skill so a subsequent apply runs exactly it.
// This method mutates NO repo state (the guarantee is the skill's Plan; it only
// records the preview in memory). An unknown skill name errors.
func (m *skillManager) plan(ctx context.Context, name string) (SkillPlan, error) {
	sk, err := m.reg.lookup(name)
	if err != nil {
		return SkillPlan{}, err
	}
	p, err := sk.Plan(ctx)
	if err != nil {
		return SkillPlan{}, err
	}
	if p.Actionable() {
		m.mu.Lock()
		m.pending[name] = p
		m.mu.Unlock()
	}
	return p, nil
}

// apply executes the pending plan for a skill, requiring confirm for a
// destructive one. It consumes the pending plan (a plan applies once). An unknown
// skill, a missing pending plan, or a destructive plan without confirm each
// errors WITHOUT mutating anything — the pending plan is preserved on refusal so
// the operator can confirm and retry.
func (m *skillManager) apply(ctx context.Context, name string, confirm bool) (SkillResult, error) {
	if _, err := m.reg.lookup(name); err != nil {
		return SkillResult{}, err
	}
	m.mu.Lock()
	p, ok := m.pending[name]
	m.mu.Unlock()
	if !ok {
		return SkillResult{}, errNoPendingPlan
	}
	if p.Destructive && !confirm {
		return SkillResult{}, errSkillNeedsConfirm
	}
	res, err := applySkillActions(ctx, m.root, m.git, p, confirm)
	if err != nil {
		return SkillResult{}, err
	}
	m.mu.Lock()
	delete(m.pending, name)
	m.mu.Unlock()
	return res, nil
}

// ---- HTTP handlers: the chat-skills surface ----

// skillsPage renders the maintenance-skill index: one card per registered skill
// (name, title, summary, a destructive marker) with its dry-run trigger and an
// initially-empty result panel HTMX swaps into. Strictly read-only — listing a
// skill runs nothing.
func (s *Server) skillsPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "skills.html", map[string]interface{}{"Skills": s.skills.list()})
}

// skillPlan runs a skill's read-only dry-run and returns the preview fragment:
// the report plus the concrete actions applying it WOULD take (recorded pending
// so a following apply runs exactly this plan). It MUTATES NOTHING. An unknown
// skill is a 404; a scan failure is a 500 (surfaced, never a silent empty plan).
func (s *Server) skillPlan(w http.ResponseWriter, r *http.Request) {
	plan, err := s.skills.plan(r.Context(), r.PathValue("name"))
	if err != nil {
		s.skillError(w, err)
		return
	}
	s.render(w, "skill_panel.html", map[string]interface{}{"Plan": plan})
}

// skillApply executes the pending plan for a skill and returns the result
// fragment. A destructive skill requires the explicit confirm=yes the panel's
// Apply control submits; without it the apply is refused (400) and nothing is
// mutated. Unknown skill -> 404; no prior dry-run -> 409.
func (s *Server) skillApply(w http.ResponseWriter, r *http.Request) {
	res, err := s.skills.apply(r.Context(), r.PathValue("name"), r.FormValue("confirm") == "yes")
	if err != nil {
		s.skillError(w, err)
		return
	}
	s.render(w, "skill_panel.html", map[string]interface{}{"Result": res})
}

// skillError maps a skill error to the HTTP status the htmx toast surfaces:
// unknown skill -> 404, missing dry-run -> 409, missing confirmation -> 400,
// anything else -> 500. The underlying message is passed through, never swallowed.
func (s *Server) skillError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUnknownSkill):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, errNoPendingPlan):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, errSkillNeedsConfirm):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

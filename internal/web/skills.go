package web

// This file implements chat-skills: a registry of named, invocable maintenance
// skills surfaced on the beehive frontend. Each skill offers a deterministic,
// read-only DRY-RUN (a plan of exactly what it would change) and a separate APPLY
// that performs precisely that change. Destructive skills refuse to apply without
// an explicit confirmation, and an unknown skill name is a hard error — the four
// acceptance guarantees for this feature.
//
// The registry is the single lookup/dispatch point: plan() stamps a skill's
// identity onto its dry-run and apply() enforces the invocation contract
// (unknown -> error, report-only -> no apply, destructive -> confirm-gated)
// before any mutation runs. Skills close over *Server so their plans are live
// scans and their applies reuse the server's guarded git write path.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spencerharmon/beehive/internal/artifacts"
	"github.com/spencerharmon/beehive/internal/editor"
	"github.com/spencerharmon/beehive/internal/repo"
)

// The invocation-guard errors. They are sentinels (wrapped, matched with
// errors.Is) so the HTTP layer maps each to a distinct status without string
// matching, and so a caller can tell "no such skill" apart from "you must
// confirm" — the difference between a 404 and a refusal-to-mutate.
var (
	errUnknownSkill    = errors.New("unknown skill")
	errReportOnly      = errors.New("skill is report-only and has no apply action")
	errConfirmRequired = errors.New("skill is destructive and requires explicit confirmation")
)

// skillAction is one concrete mutation a skill's apply would perform, surfaced in
// the dry-run so an operator sees exactly what will change before approving.
type skillAction struct {
	Op     string // "remove" | "reclaim" | "write"
	Target string // the path / branch / id acted on
	Detail string // human explanation (no action implied by itself)
}

// skillDiff is a proposed whole-file rewrite of ONE target's file (e.g. a single
// submodule's INFRASTRUCTURE.md), rendered as a unified diff so a file-editing
// skill previews its change like the chat editor. A plan carries one per file it
// would touch, each scoped to exactly that target.
type skillDiff struct {
	Path   string
	Before string
	After  string
}

// changed reports whether the proposed rewrite actually differs from the current
// file — a no-op normalization proposes nothing.
func (d *skillDiff) changed() bool { return d != nil && d.Before != d.After }

// skillPlan is the deterministic dry-run of a skill: a read-only description of
// what applying WOULD do, computed without mutating anything. The identity/flag
// fields are stamped by the registry, not the plan closure.
type skillPlan struct {
	Skill       string
	Title       string
	Summary     string
	Destructive bool
	ReportOnly  bool

	Report  []string      // informational findings (report-only skills, or "nothing to do")
	Actions []skillAction // the concrete mutations apply would perform
	Diffs   []*skillDiff  // proposed file rewrites, one per target file the skill would edit
}

// Empty reports whether the plan would change nothing: no actions and no real
// diff. The panel uses it to show "already clean" and suppress the apply control.
func (p skillPlan) Empty() bool {
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

// skillResult is the outcome of applying a skill: the concrete changes made.
type skillResult struct {
	Skill string
	Done  []string
}

// skill is one named, invocable maintenance action. plan computes the read-only
// dry-run; apply performs it. A destructive skill's apply is gated on an explicit
// confirm by the REGISTRY (apply below), never by the closure, so every skill's
// mutation path is protected uniformly.
type skill struct {
	Name        string
	Title       string
	Summary     string
	Destructive bool
	ReportOnly  bool
	plan        func(ctx context.Context) (skillPlan, error)
	apply       func(ctx context.Context) (skillResult, error)
}

// skillRegistry is the ordered set of skills with name lookup. It is rebuilt per
// request (cheap: just closures) so every plan is a live scan.
type skillRegistry struct {
	order  []string
	byName map[string]*skill
}

// list returns the skills in registration order (deterministic for the index).
func (r *skillRegistry) list() []*skill {
	out := make([]*skill, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.byName[n])
	}
	return out
}

// lookup resolves a skill by name, returning errUnknownSkill (wrapped with the
// name) when absent — the acceptance's "unknown skill errors".
func (r *skillRegistry) lookup(name string) (*skill, error) {
	sk, ok := r.byName[name]
	if !ok {
		return nil, fmt.Errorf("%q: %w", name, errUnknownSkill)
	}
	return sk, nil
}

// plan runs a skill's dry-run and stamps the skill's identity/flags onto the
// result, so plan closures compute only Report/Actions/Diff. It mutates nothing.
func (r *skillRegistry) plan(ctx context.Context, name string) (*skill, skillPlan, error) {
	sk, err := r.lookup(name)
	if err != nil {
		return nil, skillPlan{}, err
	}
	p, err := sk.plan(ctx)
	if err != nil {
		return sk, skillPlan{}, err
	}
	p.Skill, p.Title, p.Summary = sk.Name, sk.Title, sk.Summary
	p.Destructive, p.ReportOnly = sk.Destructive, sk.ReportOnly
	return sk, p, nil
}

// apply runs a skill's mutation AFTER enforcing the invocation contract: an
// unknown skill errors, a report-only skill has nothing to apply, and a
// destructive skill refuses without an explicit confirm. Each guard returns its
// sentinel error with NO side effect, so "no destructive action without
// approval" holds structurally — the closure never runs on a guard failure.
func (r *skillRegistry) apply(ctx context.Context, name string, confirm bool) (*skill, skillResult, error) {
	sk, err := r.lookup(name)
	if err != nil {
		return nil, skillResult{}, err
	}
	if sk.ReportOnly || sk.apply == nil {
		return sk, skillResult{}, errReportOnly
	}
	if sk.Destructive && !confirm {
		return sk, skillResult{}, errConfirmRequired
	}
	res, err := sk.apply(ctx)
	if err != nil {
		return sk, skillResult{}, err
	}
	res.Skill = sk.Name
	return sk, res, nil
}

// skills builds the maintenance-skill registry over this server. The set and
// order are deterministic; each skill closes over s for its live scan/apply.
func (s *Server) skills() *skillRegistry {
	reg := &skillRegistry{byName: map[string]*skill{}}
	add := func(sk *skill) {
		reg.order = append(reg.order, sk.Name)
		reg.byName[sk.Name] = sk
	}
	add(s.skillCleanupStale())
	add(s.skillGC())
	add(s.skillResources())
	add(s.skillInfraConventions())
	return reg
}

// skillCleanupStale removes the unregistered edit-*/beehive-* worktree
// directories under .worktrees that dead editor sessions and capped passes leave
// behind (the "stale worktrees" hygiene class). Destructive: it deletes
// directories, so its apply is confirm-gated and recomputes the stale set under
// the git lock before touching disk.
func (s *Server) skillCleanupStale() *skill {
	return &skill{
		Name:        "cleanup-stale",
		Title:       "Remove stale worktrees",
		Summary:     "Delete unregistered edit-*/beehive-* worktree directories left under .worktrees by dead editor sessions and capped passes.",
		Destructive: true,
		plan: func(ctx context.Context) (skillPlan, error) {
			items, err := staleWorktrees(ctx, s.repo.Root, s.git)
			if err != nil {
				return skillPlan{}, err
			}
			var p skillPlan
			for _, it := range items {
				p.Actions = append(p.Actions, skillAction{
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
		apply: func(ctx context.Context) (skillResult, error) {
			// Serialize against every other primary-checkout mutation, then
			// RECOMPUTE the stale set under the lock so the apply acts on the live
			// state, never a plan that raced a concurrent publish.
			s.gitMu.Lock()
			defer s.gitMu.Unlock()
			items, err := staleWorktrees(ctx, s.repo.Root, s.git)
			if err != nil {
				return skillResult{}, err
			}
			var res skillResult
			for _, it := range items {
				// Guard: a bare basename under .worktrees, never a path — the scan
				// only ever yields basenames, so anything else is not ours to touch.
				if filepath.Base(it.Name) != it.Name {
					continue
				}
				if err := os.RemoveAll(filepath.Join(s.repo.Root, ".worktrees", it.Name)); err != nil {
					return skillResult{}, err
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

// skillGC reclaims abandoned editor worktrees: the edit-* worktrees that are both
// stale (no fresh session record) and clean (no pending unpublished change),
// exactly what the editor's startup Reload prunes. Destructive: it removes
// worktrees + branches, so its apply is confirm-gated and runs under the git lock.
func (s *Server) skillGC() *skill {
	return &skill{
		Name:        "gc",
		Title:       "Reclaim abandoned editor worktrees",
		Summary:     "Remove edit-* editor worktrees that are stale (no fresh session) and clean (no pending change), mirroring the editor's startup reclaim.",
		Destructive: true,
		plan: func(ctx context.Context) (skillPlan, error) {
			branches, err := s.editors.Reclaimable(ctx)
			if err != nil {
				return skillPlan{}, err
			}
			var p skillPlan
			for _, b := range branches {
				p.Actions = append(p.Actions, skillAction{
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
		apply: func(ctx context.Context) (skillResult, error) {
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
				return skillResult{}, err
			}
			if err := s.editors.Reload(ctx); err != nil {
				return skillResult{}, err
			}
			var res skillResult
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

// skillResources is a read-only inventory of each submodule target's deploy state
// (INFRASTRUCTURE.md) and produced artifacts (ARTIFACTS.md). Blue/green is a
// per-submodule property, so the report scopes every deploy-env line to a named
// submodule and never presents a hive-wide "active env" for the coordination root
// (which is not a deployable target). Report-only: it has no apply, so the
// registry refuses to "apply" it.
func (s *Server) skillResources() *skill {
	return &skill{
		Name:       "resources",
		Title:      "Report infrastructure & artifacts",
		Summary:    "Read-only inventory of each submodule: its own active blue/green deploy env and the produced artifacts.",
		ReportOnly: true,
		plan: func(ctx context.Context) (skillPlan, error) {
			var p skillPlan
			subs, err := s.repo.Submodules()
			if err != nil {
				return skillPlan{}, err
			}
			for _, sm := range subs {
				p.Report = append(p.Report, infraLine(sm.Name, filepath.Join(sm.Path, repo.InfraFile)))
				p.Report = append(p.Report, artifactsLine(sm.Name, filepath.Join(sm.Path, repo.Artifacts)))
			}
			return p, nil
		},
	}
}

// skillInfraConventions normalizes each SUBMODULE's own INFRASTRUCTURE.md so it
// declares the blue/green deploy markers (Active + Environments), filling in the
// conventional defaults only for markers that are ABSENT. Blue/green is a
// per-submodule property, so it acts on every submodule's
// submodules/<name>/INFRASTRUCTURE.md independently and never on the coordination
// root (which is not a deployable target). It is non-destructive (it never removes
// or rewrites an existing marker) and idempotent, so it applies without a confirm —
// but it still previews each edit as a diff and writes via the guarded publish path.
func (s *Server) skillInfraConventions() *skill {
	return &skill{
		Name:    "infra-conventions",
		Title:   "Normalize infrastructure conventions",
		Summary: "Ensure each submodule's INFRASTRUCTURE.md declares its own blue/green deploy markers (Active + Environments), adding the conventional defaults when absent.",
		plan: func(ctx context.Context) (skillPlan, error) {
			subs, err := s.repo.Submodules()
			if err != nil {
				return skillPlan{}, err
			}
			var p skillPlan
			for _, sm := range subs {
				path := filepath.Join(sm.Path, repo.InfraFile)
				before, err := readFileOrEmpty(path)
				if err != nil {
					return skillPlan{}, err
				}
				after := normalizeInfraConventions(before)
				if after == before {
					continue
				}
				display := filepath.ToSlash(filepath.Join("submodules", sm.Name, repo.InfraFile))
				p.Diffs = append(p.Diffs, &skillDiff{Path: display, Before: before, After: after})
				p.Actions = append(p.Actions, skillAction{
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
		apply: func(ctx context.Context) (skillResult, error) {
			subs, err := s.repo.Submodules()
			if err != nil {
				return skillResult{}, err
			}
			var res skillResult
			wrote := false
			for _, sm := range subs {
				path := filepath.Join(sm.Path, repo.InfraFile)
				before, err := readFileOrEmpty(path)
				if err != nil {
					return skillResult{}, err
				}
				after := normalizeInfraConventions(before)
				if after == before {
					continue
				}
				if err := os.WriteFile(path, []byte(after), 0o644); err != nil {
					return skillResult{}, err
				}
				res.Done = append(res.Done, "wrote "+filepath.ToSlash(filepath.Join("submodules", sm.Name, repo.InfraFile))+" with the conventional deploy markers")
				wrote = true
			}
			if !wrote {
				return skillResult{Done: []string{"every submodule's INFRASTRUCTURE.md already follows conventions"}}, nil
			}
			if err := s.publishMain(ctx, "frontend: normalize submodule INFRASTRUCTURE conventions"); err != nil {
				return skillResult{}, err
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
// error), so a normalization skill can propose creating a file from scratch.
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

// skillDiffView is one file's rendered diff for the panel: the target path plus
// the diff rows, so a multi-file plan (e.g. per-submodule normalization) previews
// each file's change under its own path.
type skillDiffView struct {
	Path string
	Rows []editor.DiffRow
}

// skillPanel is the view model for one skill's card/panel: identity + flags plus,
// once a dry-run or apply has run, the plan, the rendered per-file diffs, the
// result, and any note/error to surface.
type skillPanel struct {
	Name        string
	Title       string
	Summary     string
	Destructive bool
	ReportOnly  bool

	Plan   *skillPlan
	Diffs  []skillDiffView
	Result *skillResult
	Note   string
	Err    string

	// Confirming marks the panel as the destructive-confirm gate: apply was
	// invoked without confirmation, so the plan is re-shown with a distinct
	// "confirm and apply" control (which resubmits with confirm set). Nothing was
	// mutated to reach this state.
	Confirming bool
}

// newSkillPanel seeds a panel from a skill's static identity (no plan/result yet).
func newSkillPanel(sk *skill) skillPanel {
	return skillPanel{
		Name:        sk.Name,
		Title:       sk.Title,
		Summary:     sk.Summary,
		Destructive: sk.Destructive,
		ReportOnly:  sk.ReportOnly,
	}
}

// withPlan attaches a plan (and its rendered per-file diffs) to the panel.
func (p skillPanel) withPlan(plan skillPlan) skillPanel {
	p.Plan = &plan
	for _, d := range plan.Diffs {
		if d.changed() {
			p.Diffs = append(p.Diffs, skillDiffView{Path: d.Path, Rows: editor.RenderDiff(d.Before, d.After)})
		}
	}
	return p
}

// skillsPage renders the maintenance-skills index: every registered skill as a
// card with a dry-run control. Read-only — it runs no skill's plan on load.
func (s *Server) skillsPage(w http.ResponseWriter, r *http.Request) {
	panels := make([]skillPanel, 0)
	for _, sk := range s.skills().list() {
		panels = append(panels, newSkillPanel(sk))
	}
	s.render(w, "skills.html", map[string]interface{}{"Skills": panels, "Title": pageTitle("skills"), "Nav": "skills"})
}

// skillPlanHandler runs a skill's deterministic dry-run and renders its panel with
// the plan. An unknown skill is a 404. It mutates nothing.
func (s *Server) skillPlanHandler(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	reg := s.skills()
	sk, plan, err := reg.plan(r.Context(), name)
	if err != nil {
		if errors.Is(err, errUnknownSkill) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "skill_panel.html", newSkillPanel(sk).withPlan(plan))
}

// skillApplyHandler applies a skill after the registry's invocation guards. An
// unknown skill is a 404, a report-only skill is a 400, and a destructive skill
// invoked WITHOUT confirm re-renders the plan with a confirmation prompt and
// performs NO mutation. On success it renders the result plus a fresh post-apply
// plan so the panel reflects the new (typically now-clean) state.
func (s *Server) skillApplyHandler(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	reg := s.skills()
	sk, res, err := reg.apply(r.Context(), name, isTrue(r.FormValue("confirm")))
	if err != nil {
		switch {
		case errors.Is(err, errUnknownSkill):
			http.NotFound(w, r)
		case errors.Is(err, errReportOnly):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, errConfirmRequired):
			// No mutation happened. Re-show the plan with a distinct confirm control
			// so applying takes a deliberate second, explicit action; 200 so htmx
			// swaps the gate panel in (the confirm requirement is the message, not
			// an error state).
			panel := newSkillPanel(sk)
			panel.Confirming = true
			panel.Note = "Confirmation required: this action is destructive. Confirm to apply."
			if _, plan, perr := reg.plan(r.Context(), name); perr == nil {
				panel = panel.withPlan(plan)
			}
			s.render(w, "skill_panel.html", panel)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	panel := newSkillPanel(sk)
	panel.Result = &res
	if _, plan, perr := reg.plan(r.Context(), name); perr == nil {
		panel = panel.withPlan(plan)
	}
	s.render(w, "skill_panel.html", panel)
}

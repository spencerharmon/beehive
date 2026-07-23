package web

// bootstrap-agent-autodetect: detect an unbootstrapped repo on frontend open and
// auto-open a setup agent on the SAME chat surface (chat-diff-editor-core).
//
// The detector is a READ-ONLY classification that mirrors the BOOTSTRAP.md setup
// walkthrough: it stats a handful of files and reports the SPECIFIC steps still
// unmet (missing LOCALS.md, no runner config, no registered submodules, or a
// submodule with ROI.md but no PLAN.md). A repo that clears every signal is
// BOOTSTRAPPED and gets no banner and no auto-opened agent.
//
// When unbootstrapped, the dashboard renders an advisory banner whose embedded
// agent lazily loads GET /bootstrap. That handler opens (or reuses) a SINGLETON
// chat-edit session over LOCALS.md seeded with BOOTSTRAP.md + the unmet steps, so
// the operator is walked through setup. Everything here is advisory and
// idempotent: detection re-runs each open (the banner disappears once signals
// clear) and the agent is the ordinary propose-then-approve loop — nothing writes
// LOCALS.md, adds a submodule, or commits to main without an explicit human
// approval on that surface.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/editor"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spencerharmon/beehive/prompts"
)

// bootstrapStep is one unmet setup step: a stable Key (for tests/routing), a
// short Title, and a one-line Detail shown in the banner and injected into the
// agent's system prompt.
type bootstrapStep struct {
	Key    string
	Title  string
	Detail string
}

// bootstrapState is the result of a detection pass: the ORDERED unmet steps
// (empty when the repo is fully set up). It is passed to the dashboard banner and
// drives the setup agent's seeding.
type bootstrapState struct {
	Unmet []bootstrapStep
}

// Bootstrapped reports whether every setup signal is clear (no unmet steps), so
// the dashboard shows no banner and opens no agent.
func (b bootstrapState) Bootstrapped() bool { return len(b.Unmet) == 0 }

// detectBootstrap classifies a beehive repo's setup state from files alone
// (read-only): it stats LOCALS.md, the runner config, the registered submodules,
// and each submodule's ROI/PLAN pair, appending one unmet step per failing
// BOOTSTRAP.md signal in setup order (locals, submodules, plan, config). subs is
// the already-listed submodule set (repo.Submodules) so the caller controls the
// one directory read; cfgDir is the host config dir (Config.Dir).
func detectBootstrap(root, cfgDir string, subs []repo.Submodule) bootstrapState {
	var st bootstrapState
	// Step 2: LOCALS.md — the site-specific operator record.
	if !fileExists(filepath.Join(root, repo.LocalsFile)) {
		st.Unmet = append(st.Unmet, bootstrapStep{
			Key:    "locals",
			Title:  "Author LOCALS.md",
			Detail: "record this install's site-specific facts (source & build, deploy, scheduler, topology, safety rules) in LOCALS.md at the repo root",
		})
	}
	// Step 3a: at least one registered target.
	if len(subs) == 0 {
		st.Unmet = append(st.Unmet, bootstrapStep{
			Key:    "submodules",
			Title:  "Add a target submodule",
			Detail: "no targets are registered yet — add one with `beehive submodule add <name> <git-url>` (or the add-submodule form below)",
		})
	}
	// Step 3b: a target with intent (ROI.md) but no derived PLAN.md yet. One
	// aggregated step is enough to flag the repo; a bootstrap pass derives the
	// plan for every such submodule.
	for _, sm := range subs {
		if sm.NeedsBootstrap() {
			st.Unmet = append(st.Unmet, bootstrapStep{
				Key:    "plan",
				Title:  "Bootstrap a plan for " + sm.Name,
				Detail: sm.Name + " has an ROI.md but no PLAN.md — a bootstrap pass will decompose its intent into a plan",
			})
			break
		}
	}
	// Step 4: the runner config.
	if !runnerConfigured(root, cfgDir) {
		st.Unmet = append(st.Unmet, bootstrapStep{
			Key:    "config",
			Title:  "Configure the runner",
			Detail: "no runner config found — create " + config.FileName + " (a common spot is " + filepath.Join(config.DefaultDir, config.FileName) + ") with agent_cmd, model, ttl_minutes, etc.",
		})
	}
	return st
}

// runnerConfigured reports whether a runner config layer is present: either the
// host file (<cfgDir>/config.yaml) or the in-repo global (<root>/config.yaml).
// This mirrors the layer paths config.Resolve reads, so "configured" here means
// exactly the same thing the runner sees.
func runnerConfigured(root, cfgDir string) bool {
	if cfgDir != "" && fileExists(filepath.Join(cfgDir, config.FileName)) {
		return true
	}
	return fileExists(filepath.Join(root, config.FileName))
}

// fileExists reports whether path is an existing regular file (not a directory).
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// bootstrapState is the Server's live detection pass: it lists the submodules
// once and classifies against the repo root + resolved host config dir.
func (s *Server) bootstrapState() bootstrapState {
	subs, _ := s.repo.Submodules()
	return detectBootstrap(s.repo.Root, s.cfg.Dir, subs)
}

// readBootstrapGuide returns the setup walkthrough to seed the agent with: the
// repo's own BOOTSTRAP.md when present (the authoritative on-disk copy an
// instruction update maintains), falling back to the binary's embedded default so
// seeding still works if the file was removed.
func readBootstrapGuide(root string) string {
	if b, err := os.ReadFile(filepath.Join(root, repo.BootstrapFile)); err == nil {
		return string(b)
	}
	return prompts.BootstrapGuide
}

// bootstrapSystemPrompt builds the setup agent's system prompt: the coordination
// editor's direct-edit contract over LOCALS.md (edit-session-consolidation — the
// bootstrap agent is now an editor.Session of Kind bootstrap, so it edits
// LOCALS.md directly with its tools and the operator reviews the diff and clicks
// Merge, exactly like every other edit-with-AI surface) plus LOCALS.md's own
// editing rules (resolveFileContext) and a bootstrap preamble that states the
// role, the detected unmet steps, and the hard rule to route submodule adds
// through the CLI (never a bare mkdir) — then the full BOOTSTRAP.md guide.
func bootstrapSystemPrompt(guide string, st bootstrapState) string {
	var b strings.Builder
	b.WriteString(editor.FilePrompt(repo.LocalsFile))
	b.WriteString("\n\n----\n\n")
	b.WriteString(resolveFileContext(repo.LocalsFile))
	b.WriteString("\n\n----\n\n")
	b.WriteString("This beehive install is NOT fully set up yet, and you are its setup guide. ")
	b.WriteString("Walk the operator through the remaining steps below, following the BOOTSTRAP.md guide. ")
	b.WriteString("Your one editable file is LOCALS.md (setup step 2): edit it directly to help the operator author it — the operator reviews the diff and clicks Merge to publish it; nothing is live until they do, so never claim it is written until they merge. ")
	b.WriteString("To add a target, tell the operator to run `beehive submodule add <name> <git-url>` or use the dashboard's add-submodule form; NEVER create submodule directories by hand. ")
	b.WriteString("Do not run setup commands yourself and do not report a step done until the operator confirms it.\n\n")
	if len(st.Unmet) > 0 {
		b.WriteString("Remaining setup steps detected in this repo:\n")
		for _, s := range st.Unmet {
			b.WriteString("- " + s.Title + ": " + s.Detail + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("---- BEGIN BOOTSTRAP.md ----\n")
	b.WriteString(guide)
	b.WriteString("\n---- END BOOTSTRAP.md ----\n")
	return b.String()
}

// bootstrapIntro is the visible opening message seeded into the session log so
// the operator sees why the agent opened and what is outstanding (the log is
// otherwise empty until the first turn).
func bootstrapIntro(st bootstrapState) string {
	var titles []string
	for _, s := range st.Unmet {
		titles = append(titles, s.Title)
	}
	msg := "This repo isn't fully bootstrapped yet, so I opened the setup guide (BOOTSTRAP.md)."
	if len(titles) > 0 {
		msg += " Outstanding steps: " + strings.Join(titles, "; ") + "."
	}
	msg += " Tell me where you'd like to start, or ask me to draft LOCALS.md — nothing is written to main until you review the diff and click Merge."
	return msg
}

// openBootstrapSession opens (or returns the already-open) SINGLETON setup agent:
// an editor.Session of Kind bootstrap over LOCALS.md seeded with the bootstrap
// system prompt and a visible intro. It is idempotent — repeated dashboard opens
// reuse the one session (found by Kind, so it survives a restart via editor
// recovery) rather than cutting a fresh worktree each time. bootstrapMu
// serializes the check-or-open so concurrent loads converge on one session.
func (s *Server) openBootstrapSession(ctx context.Context, sys, intro string) (*editor.Session, error) {
	s.bootstrapMu.Lock()
	defer s.bootstrapMu.Unlock()
	for _, sess := range s.editors.List() {
		if sess.Kind() == editor.KindBootstrap {
			return sess, nil
		}
	}
	return s.editors.OpenSession(ctx, editor.Spec{
		File:   repo.LocalsFile,
		Kind:   editor.KindBootstrap,
		System: sys,
		Slug:   "bootstrap",
		Intro:  []editor.Turn{{Role: "system", Text: intro, At: time.Now()}},
	})
}

// bootstrapAgent (GET /bootstrap) lazily surfaces the setup agent for the
// dashboard banner's htmx load. It re-detects first: a repo that has become
// bootstrapped returns 204 (the banner clears, no worktree is cut). Otherwise it
// opens/reuses the singleton session and renders the agent fragment. Opening the
// session cuts ONE ephemeral edit worktree (the shared editor Manager's, GC-
// reclaimable) but writes nothing to LOCALS.md or main — that only happens on an
// explicit Merge.
func (s *Server) bootstrapAgent(w http.ResponseWriter, r *http.Request) {
	st := s.bootstrapState()
	if st.Bootstrapped() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	guide := readBootstrapGuide(s.repo.Root)
	sess, err := s.openBootstrapSession(r.Context(), bootstrapSystemPrompt(guide, st), bootstrapIntro(st))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "bootstrap_agent.html", map[string]interface{}{
		"ID": sess.ID, "Path": sess.File, "Unmet": st.Unmet,
	})
}

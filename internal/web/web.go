// Package web is the beehived frontend: file-derived read views and git-backed
// writes over the beehive repo. HTMX templates and assets are embedded so the
// daemon ships as a single binary. ROI.md is writable only here.
package web

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spencerharmon/beehive/internal/artifacts"
	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/editor"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spencerharmon/beehive/internal/submod"
	"github.com/spencerharmon/beehive/internal/swarm"
)

//go:embed templates/*.html
var tmplFS embed.FS

//go:embed assets/*
var assetFS embed.FS

// submoduleAddTimeout bounds a `git submodule add` (a network clone needing
// creds); it is larger than the 30s plain-commit budget because cloning a real
// remote can legitimately take longer than a local commit.
const submoduleAddTimeout = 2 * time.Minute

// Server holds the parsed templates and the repo it serves.
type Server struct {
	repo    *repo.Repo
	cfg     config.Config
	git     *git.Repo
	tmpl    *template.Template
	editors *editor.Manager
	cache   *viewCache

	// chat is the generic chat-diff editor over ANY repo file: a per-edit ROOT
	// worktree + opencode session that proposes a full-file change rendered as a
	// unified diff, applied+committed only on human approval (chat-diff-editor-core).
	chat *chatManager

	// gitMu serializes operations that mutate the primary beehive checkout (where
	// main is checked out): the viewer's periodic `git pull --ff-only main` that
	// follows off-box sessions, and the frontend's own commit/publish. Without it a
	// poll-driven pull races a concurrent frontend commit on the shared index
	// (index.lock). Read-only git (log/show/status) is not guarded.
	gitMu sync.Mutex

	// pullIvl coalesces the follow-the-remote pulls: many open session panes each
	// poll every ~2s, but only one actual pull runs per interval. 0 disables
	// coalescing (tests). syncMu guards the last-pull bookkeeping (pulled/lastPull/
	// pullErr) that backs the session panes' staleness banner.
	pullIvl  time.Duration
	syncMu   sync.Mutex
	pulled   bool
	lastPull time.Time
	pullErr  error

	// streamInterval is the SSE re-read cadence for a live session transcript
	// (sessionStream). 0 means the default (streamIvl); tests set it small to keep
	// them fast.
	streamInterval time.Duration
}

// New builds a Server over the beehive repo at root.
func New(r *repo.Repo, cfg config.Config) (*Server, error) {
	t, err := template.ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	em, err := editor.NewManager(r.Root, cfg)
	if err != nil {
		return nil, err
	}
	g := git.New(r.Root)
	// The chat-diff editor drives its own opencode client (same server/model as
	// the single-file editor); it opens a per-edit ROOT worktree and awaits each
	// turn (opencode-turn-poll) before rendering the proposed diff.
	oc := &swarm.Opencode{Base: cfg.AgentURL, Model: cfg.Model, Temperature: cfg.Temperature, MaxTokens: cfg.MaxTokens, HTTP: &http.Client{Timeout: 0}}
	return &Server{repo: r, cfg: cfg, git: g, tmpl: t, editors: em, cache: newViewCache(), pullIvl: pullInterval(cfg), chat: newChatManager(r.Root, oc)}, nil
}

// pullInterval is the resolved follow-the-remote coalescing window (config
// session_pull_seconds), defaulting to 2s when unset. It bounds how often the
// polled session panes actually hit the remote to fast-forward local main.
func pullInterval(cfg config.Config) time.Duration {
	s := cfg.SessionPullSeconds
	if s <= 0 {
		s = 2
	}
	return time.Duration(s) * time.Second
}

// RecoverEditors runs the editor's startup recovery: it re-registers persisted
// in-flight edit sessions and prunes stale/abandoned edit worktrees left behind
// by a previous beehived (see editor.Manager.Reload). It is best-effort startup
// housekeeping — the daemon calls it once before serving and treats a failure as
// non-fatal — so a recovery hiccup never blocks the frontend from starting.
func (s *Server) RecoverEditors(ctx context.Context) error {
	return s.editors.Reload(ctx)
}

// Routes returns the mux wired to all handlers.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("GET /bootstrap", s.bootstrapAgent)
	mux.HandleFunc("GET /stats", s.stats)
	mux.HandleFunc("GET /submodule/{name}", s.explorer)
	mux.HandleFunc("GET /submodule/{name}/branches", s.branches)
	mux.HandleFunc("GET /submodule/{name}/doc/{file}", s.doc)
	mux.HandleFunc("GET /submodule/{name}/plan", s.plan)
	mux.HandleFunc("POST /submodule/{name}/plan/delete", s.planDelete)
	mux.HandleFunc("GET /submodule/{name}/sessions", s.sessionsList)
	mux.HandleFunc("GET /submodule/{name}/sessions/body", s.sessionsListBody)
	mux.HandleFunc("GET /submodule/{name}/session/{branch}", s.sessionView)
	mux.HandleFunc("GET /submodule/{name}/session/{branch}/body", s.sessionBody)
	mux.HandleFunc("GET /submodule/{name}/session/{branch}/stream", s.sessionStream)
	mux.HandleFunc("GET /roi/{name}", s.roiGet)
	mux.HandleFunc("POST /roi/{name}", s.roiPost)
	mux.HandleFunc("GET /secrets", s.secretsGet)
	mux.HandleFunc("POST /secrets", s.secretsPost)
	mux.HandleFunc("GET /merge", s.mergeGet)
	mux.HandleFunc("POST /merge", s.mergePost)
	mux.HandleFunc("POST /submodule/add", s.submoduleAdd)
	mux.HandleFunc("POST /submodule/link", s.submoduleLink)
	mux.HandleFunc("GET /env", s.envGet)
	mux.HandleFunc("POST /env/deploy", s.envDeploy)
	mux.HandleFunc("GET /human", s.human)
	mux.HandleFunc("GET /hygiene", s.hygiene)
	// Maintenance skills: an index of named actions each with a read-only dry-run
	// (plan) and a separate apply; destructive skills gate apply on confirm.
	mux.HandleFunc("GET /skills", s.skillsPage)
	mux.HandleFunc("POST /skills/{name}/plan", s.skillPlanHandler)
	mux.HandleFunc("POST /skills/{name}/apply", s.skillApplyHandler)
	// AI editor chat (browser): one worktree branch per session.
	mux.HandleFunc("GET /edit", s.editEntry)
	mux.HandleFunc("POST /edit", s.chatOpen)
	mux.HandleFunc("GET /edit/{id}", s.chatPage)
	mux.HandleFunc("GET /edit/{id}/panel", s.chatPanel)
	mux.HandleFunc("POST /edit/{id}/message", s.chatMessage)
	mux.HandleFunc("POST /edit/{id}/approve", s.chatApprove)
	mux.HandleFunc("POST /edit/{id}/reject", s.chatReject)
	mux.HandleFunc("GET /editor/{id}", s.editorPage)
	mux.HandleFunc("GET /editor/{id}/panel", s.editorPanel)
	mux.HandleFunc("POST /editor/{id}/chat", s.editorChat)
	mux.HandleFunc("POST /editor/{id}/merge", s.editorMerge)
	mux.HandleFunc("POST /editor/{id}/close", s.editorClose)
	// AI editor chat (JSON API): browser-free clients.
	mux.HandleFunc("POST /api/editor", s.apiEditorOpen)
	mux.HandleFunc("GET /api/editor/{id}", s.apiEditorGet)
	mux.HandleFunc("POST /api/editor/{id}/chat", s.apiEditorChat)
	mux.HandleFunc("POST /api/editor/{id}/merge", s.apiEditorMerge)
	mux.HandleFunc("GET /api/editor/{id}/diff", s.apiEditorDiff)
	mux.Handle("GET /assets/", http.FileServer(http.FS(assetFS)))
	return mux
}

func (s *Server) render(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) submodule(name string) (repo.Submodule, error) {
	subs, err := s.repo.Submodules()
	if err != nil {
		return repo.Submodule{}, err
	}
	for _, sm := range subs {
		if sm.Name == name {
			return sm, nil
		}
	}
	return repo.Submodule{}, os.ErrNotExist
}

// subView is the dashboard's per-submodule card, all derived live from files
// each request (no cache). State is the swarm status (active/dormant/bootstrap);
// Pending/Human are task counts from the unified parser (internal/plan) so they
// match what the runner/selector see; Env is the submodule's active blue/green
// deploy env from the typed artifacts model ("" when it has no
// INFRASTRUCTURE.md); Working is true when a task is actively claimed (a fresh
// session+heartbeat), driving the card's live overlay.
type subView struct {
	Name    string
	State   string
	Stamp   string
	Pending int
	Human   int
	Env     string
	Working bool
}

// EnvClass is the design-system badge hue modifier for the active deploy env:
// env-blue / env-green for the standard blue/green envs, "" (a neutral badge)
// for any other or absent env. Resolving it in Go bounds the class names the
// template can emit to a known set rather than interpolating a raw file value
// into the class attribute.
func (v subView) EnvClass() string {
	switch v.Env {
	case "blue":
		return "env-blue"
	case "green":
		return "env-green"
	default:
		return ""
	}
}

// headSHA resolves the beehive repo HEAD short SHA used to key the parse cache,
// returning "" when HEAD is unresolvable (a repo with no commits yet) so callers
// read through uncached rather than failing. Resolve it ONCE per request and
// share it across every submodule's cached read: a multi-submodule dashboard
// then pays a single `git rev-parse`, not one per submodule, and every card in
// that render sees a coherent commit snapshot.
func (s *Server) headSHA(ctx context.Context) string {
	h, err := s.git.Head(ctx)
	if err != nil {
		return ""
	}
	return h
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	views, err := s.subViews(r.Context(), time.Now(), s.ttl())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	env, _ := parseEnv(filepath.Join(s.repo.Root, repo.InfraFile))
	// Read-only hygiene summary alongside the submodule cards. A scan error is
	// surfaced inside the widget (not swallowed) rather than failing the whole
	// dashboard, which is the operator's primary page.
	hyg, err := scanHygiene(r.Context(), s.repo.Root, s.git)
	if err != nil {
		hyg = Hygiene{Skill: hygieneSkill, Err: err.Error()}
	}
	s.render(w, "dashboard.html", map[string]interface{}{"Subs": views, "Env": env, "Hygiene": hyg, "Bootstrap": s.bootstrapState()})
}

// subViews builds the dashboard card data for every submodule: State
// (active/dormant/bootstrap), the ROI Stamp, the Pending/Human task counts from
// the unified parser (internal/plan via planView — the same parse the
// runner/selector use), the active blue/green Env from the submodule's own
// INFRASTRUCTURE.md via the typed artifacts model, and Working (a task carrying a
// fresh session+heartbeat claim). now/ttl are passed in so the claim-freshness
// derivation is deterministically testable; the handler supplies time.Now() and
// the resolved TTL.
//
// The PLAN.md read+parse is memoized per repo HEAD via planView, but the swarm
// status stays current: the counts and Working flag are re-projected against
// now/ttl every call (so a claim still goes stale on TTL expiry with no new
// commit), and any file change advances HEAD and drops the cache. ctx carries
// the request's cancellation/deadline into the HEAD lookup.
func (s *Server) subViews(ctx context.Context, now time.Time, ttl time.Duration) ([]subView, error) {
	subs, err := s.repo.Submodules()
	if err != nil {
		return nil, err
	}
	// Resolve HEAD once for the whole render: every submodule's cached parse is
	// keyed by this one generation, so the dashboard pays a single rev-parse and
	// all cards reflect the same commit.
	head := s.headSHA(ctx)
	var views []subView
	for _, sm := range subs {
		v := subView{Name: sm.Name, State: "active"}
		switch {
		case sm.Dormant():
			v.State = "dormant"
		case sm.NeedsBootstrap():
			v.State = "bootstrap"
		}
		// The ROI stamp and the task counts both come from the SAME cached
		// PLAN.md parse (p.ROIStamp is plan.Parse().ROI — the exact value
		// Submodule.ROIStamp scans PLAN.md for), so the dashboard reads each
		// PLAN.md once per generation instead of twice. Counts: Pending = every
		// task not DONE, Human = NEEDS-HUMAN only. A NEEDS-HUMAN task is BOTH
		// pending and human — the two counters legitimately overlap, but each task
		// increments each counter at most once (never double-counted within a
		// counter). Working = any task with a fresh claim (session+heartbeat within
		// the TTL). A parse error leaves this submodule's stamp/counts empty rather
		// than failing the whole dashboard (mirrors the pre-existing per-submodule
		// resilience).
		if p, err := s.planView(head, sm.PlanPath(), now, ttl); err == nil {
			v.Stamp = p.ROIStamp
			for _, it := range p.Items {
				if it.Status != StatusDone {
					v.Pending++
				}
				if it.Status == StatusHuman {
					v.Human++
				}
				if it.Active {
					v.Working = true
				}
			}
		}
		// Env badge: the submodule's own blue/green deploy state via the typed
		// artifacts model. An absent INFRASTRUCTURE.md leaves Env "" (no badge)
		// instead of a misleading default; a read error is surfaced as no badge
		// too (best-effort overview, never a dashboard-wide failure).
		if in, err := artifacts.LoadInfra(filepath.Join(sm.Path, repo.InfraFile)); err == nil && in.Present() {
			v.Env = in.Deployment().Active
		}
		views = append(views, v)
	}
	return views, nil
}

// fileLink is one row of the explorer's optional-file index: a view/edit (or,
// when absent, create) link for one member of the KNOWN per-submodule optional-
// file set (repo.OptionalFiles). The index is driven by that DECLARED set, not by
// the directory listing, so an absent file renders a discoverable create link
// instead of being invisible (optional-file-links). File is the basename and the
// template composes the repo-relative editor path submodules/<name>/<File>; the
// chat-diff editor (chat-diff-editor-core) opens a present file on its current
// contents (view+edit) and an absent one on an EMPTY base (create), seeded per
// file via chat-diff-file-context — including ROI.md, which stays human-owned and
// is therefore never auto-generated (the editor only writes on human approval).
type fileLink struct {
	Label   string // display name, e.g. "INFRASTRUCTURE" (basename minus .md)
	File    string // basename, e.g. "INFRASTRUCTURE.md"
	Present bool   // file exists on disk in this submodule
}

// optionalFileLinks builds the explorer's optional-file index for sm from the
// DECLARED set repo.OptionalFiles (never the disk listing), stamping each member
// present/absent by a plain existence check so a missing file still yields a row.
func optionalFileLinks(sm repo.Submodule) []fileLink {
	links := make([]fileLink, 0, len(repo.OptionalFiles))
	for _, f := range repo.OptionalFiles {
		_, err := os.Stat(filepath.Join(sm.Path, f))
		links = append(links, fileLink{
			Label:   strings.TrimSuffix(f, ".md"),
			File:    f,
			Present: err == nil,
		})
	}
	return links
}

func (s *Server) explorer(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Explorer is a read-only VIEW pane: render each doc markdown -> sanitized
	// HTML (renderMarkdown drops raw HTML / unsafe links). The raw source stays
	// reachable through the per-file editor (ROI editor / chat editor links).
	docs := map[string]template.HTML{}
	// PLAN and ROI render their raw markdown (PLAN's structure lives in
	// internal/plan; ROI is human-owned and edited verbatim). AGENTS.md is the
	// per-submodule rules overlay and RULES.md is the beehive-owned overlay
	// ADDITIVE to it — both are shown when present (the explorer's docs map is
	// rendered by sorted key, so "AGENTS" precedes "RULES": the AGENTS-then-RULES
	// order the agent context also applies). os.ReadFile only populates a label on
	// success, so an absent file is silently skipped — RULES.md's absence is a
	// safe no-op.
	for label, f := range map[string]string{
		"PLAN":   repo.PlanFile,
		"ROI":    repo.ROIFile,
		"AGENTS": repo.AgentsFile,
		"RULES":  repo.RulesFile,
	} {
		if b, err := os.ReadFile(filepath.Join(sm.Path, f)); err == nil {
			docs[label] = renderMarkdown(string(b))
		}
	}
	// INFRASTRUCTURE.md and ARTIFACTS.md are read through internal/artifacts (the
	// typed model) instead of raw text: the rendered HTML is the model's
	// round-tripped serialization, and the same parse feeds the dashboard env
	// badge. An absent doc is skipped (Present()==false).
	if in, err := artifacts.LoadInfra(filepath.Join(sm.Path, repo.InfraFile)); err == nil && in.Present() {
		docs["INFRA"] = renderMarkdown(in.String())
	}
	if a, err := artifacts.LoadArtifacts(filepath.Join(sm.Path, repo.Artifacts)); err == nil && a.Present() {
		docs["ARTIFACTS"] = renderMarkdown(a.String())
	}
	// The optional-file index (optional-file-links) renders view/edit/create links
	// for the FULL known optional-file set UNIFORMLY, present or absent, so a file
	// that does not exist yet is still discoverable (the docs map above only shows
	// present files' rendered content — the index is what makes an absent member
	// reachable). Driven by the declared set, not the directory listing.
	s.render(w, "explorer.html", map[string]interface{}{
		"Name":  sm.Name,
		"Docs":  docs,
		"Files": optionalFileLinks(sm),
	})
}

func (s *Server) branches(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	off, lim := pageParams(r)
	// Scoped to this ONE submodule's checkout: commitGraph reads only
	// sm.RepoDir(), so the view never crawls across submodules.
	cs, err := commitGraph(r.Context(), sm.RepoDir(), off, lim)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	for i := range cs {
		cs[i].DocHref = resolveDocHref(sm, cs[i].DocPath)
	}
	prev := off - lim
	if prev < 0 {
		prev = 0
	}
	s.render(w, "branch_view.html", map[string]interface{}{
		"Name":     sm.Name,
		"Sections": sectionByDate(cs),
		"Prev":     prev,
		"HasPrev":  off > 0,
		"Next":     off + lim,
		"HasNext":  len(cs) == lim, // a full page may have more
	})
}

// doc renders one of a submodule's Beehive change docs
// (submodules/<name>/docs/<file>) as sanitized markdown, so the branch view's
// commit-stamp links resolve to a readable page. file is a basename guarded
// against traversal and the read is scoped to that single submodule's docs/ dir
// (never another submodule, never outside docs/).
func (s *Server) doc(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	file := r.PathValue("file")
	if !safeBranch(file) {
		http.NotFound(w, r)
		return
	}
	b, err := os.ReadFile(filepath.Join(sm.Path, "docs", file))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "doc_view.html", map[string]interface{}{
		"Name": sm.Name, "File": file, "Body": renderMarkdown(string(b)),
	})
}

func (s *Server) plan(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, err := s.planView(s.headSHA(r.Context()), sm.PlanPath(), time.Now(), s.ttl())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Link each task to the change doc its implementing commit stamped
	// (Beehive: <taskid> <docpath>): scan this submodule's history once for the
	// stamps, then resolve each to a viewable doc under the submodule's docs/
	// (resolveDocHref guards traversal + existence, so a link is never dead).
	docs := changeDocsByTask(r.Context(), sm.RepoDir())
	for i := range p.Items {
		p.Items[i].DocHref = resolveDocHref(sm, docs[p.Items[i].ID])
	}
	s.render(w, "plan_items.html", map[string]interface{}{"Name": sm.Name, "Plan": p})
}

// planDelete removes a submodule's PLAN.md and publishes the deletion, so the
// next honeybee sees ROI present + PLAN absent (NeedsBootstrap) and rebuilds the
// plan from ROI.md from scratch. Destructive: in-flight task state (claims,
// heartbeats, attempt counts) in that PLAN is discarded; running honeybees will
// finish their current turn against their own worktree copy and a stale claim,
// then the fresh bootstrap supersedes it. Operator-initiated only.
func (s *Server) planDelete(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	planPath := sm.PlanPath()
	if _, statErr := os.Stat(planPath); os.IsNotExist(statErr) {
		// Already absent: nothing to commit, just show the (bootstrap-pending) plan.
		http.Redirect(w, r, "/submodule/"+sm.Name+"/plan", http.StatusSeeOther)
		return
	}
	if err := os.Remove(planPath); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := s.publishMain(r.Context(), "frontend: delete PLAN "+sm.Name+" (force rebootstrap from ROI)"); err != nil {
		http.Error(w, "deleted locally but publish to remote failed: "+err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/submodule/"+sm.Name+"/plan", http.StatusSeeOther)
}

// publishMain is the single write path every mutating frontend handler uses: it
// records the working-tree change on the beehived primary checkout's main AND
// propagates it to the remote so other hosts' honeybees (which branch off
// origin/main) see it. It stage-and-commits everything (git add -A) under msg,
// then pushes. An empty commit is not an error — an idempotent write (an
// already-merged merge, an unchanged file) stages nothing and still succeeds. No
// remote => single-host: the local commit is the whole publish (honeybees branch
// off local main) and the push is skipped. On a non-fast-forward (a peer advanced
// the remote under us) it fetches, merges the advanced branch in — never
// clobbering the peer's commit — and retries the push once; a real merge conflict
// is aborted (checkout left clean) and surfaced.
func (s *Server) publishMain(ctx context.Context, msg string) error {
	// Serialize against the follow-the-remote pull and other frontend writes: all
	// touch the primary checkout's index/refs (index.lock).
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	if err := s.git.Commit(ctx, msg); err != nil && !errors.Is(err, git.ErrNothing) {
		return err
	}
	remote, err := s.git.Remote(ctx)
	if err != nil || remote == "" {
		return err
	}
	// Push the primary checkout's own branch (resolved, not hardcoded "main") so
	// the publish tracks whatever branch the checkout is on.
	branch, err := s.git.CurrentBranch(ctx)
	if err != nil {
		return err
	}
	if err := s.git.Push(ctx, remote, branch); err == nil {
		return nil
	}
	if err := s.git.Fetch(ctx, remote, branch); err != nil {
		return err
	}
	if _, err := s.git.Run(ctx, "merge", "--no-edit", "FETCH_HEAD"); err != nil {
		_, _ = s.git.Run(ctx, "merge", "--abort")
		return err
	}
	return s.git.Push(ctx, remote, branch)
}

func (s *Server) roiGet(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	b, _ := os.ReadFile(sm.ROIPath())
	// The textarea carries the RAW source verbatim (the edit round-trip); the
	// preview renders the same source to sanitized HTML for reading.
	s.render(w, "roi_editor.html", map[string]interface{}{
		"Name": sm.Name, "Body": string(b), "Rendered": renderMarkdown(string(b)),
	})
}

func (s *Server) roiPost(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	body := r.FormValue("body")
	if err := os.WriteFile(sm.ROIPath(), []byte(body), 0o644); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := s.publishMain(r.Context(), "frontend: edit ROI "+sm.Name); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "roi_editor.html", map[string]interface{}{"Name": sm.Name, "Body": body, "Saved": true, "Rendered": renderMarkdown(body)})
}

func (s *Server) secretsGet(w http.ResponseWriter, r *http.Request) {
	keys, err := listSecretKeys(r.Context(), s.cfg.GPGHome, filepath.Join(s.repo.Root, repo.SecretsFile))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "secrets_panel.html", map[string]interface{}{"Keys": keys})
}

func (s *Server) secretsPost(w http.ResponseWriter, r *http.Request) {
	key, val := r.FormValue("key"), r.FormValue("value")
	if key == "" {
		http.Error(w, "key required", 400)
		return
	}
	p := filepath.Join(s.repo.Root, repo.SecretsFile)
	if err := setSecret(r.Context(), s.cfg.GPGHome, p, s.cfg.GPGRecipient, key, val); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := s.publishMain(r.Context(), "frontend: update secret "+key); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.secretsGet(w, r)
}

func (s *Server) mergeGet(w http.ResponseWriter, r *http.Request) {
	s.renderMerge(w, nil)
}

// renderMerge renders the merge panel fragment, always injecting the live
// submodule list and a (possibly empty) Selected name so the template's
// preselect comparison is type-safe. Extra keys (Merged/Branch/Tracked) drive
// the post-merge success banner.
func (s *Server) renderMerge(w http.ResponseWriter, data map[string]interface{}) {
	if data == nil {
		data = map[string]interface{}{}
	}
	if _, ok := data["Selected"]; !ok {
		data["Selected"] = ""
	}
	subs, _ := s.repo.Submodules()
	data["Subs"] = subs
	s.render(w, "merge_panel.html", data)
}

// mergePost publishes a merge end-to-end rather than merging locally and
// stopping. It merges the chosen branch into the submodule's tracked branch
// (resolved from .gitmodules, never a hardcoded "main"), pushes that branch to
// the submodule's origin, then advances + commits the beehive pointer and
// publishes the beehive root through the same shared write path the other
// mutating handlers use (publishMain: commit -> push). A conflict is aborted
// cleanly (origin and pointer untouched) and returns 409; an already-merged
// branch moves nothing and is still a success (idempotent).
func (s *Server) mergePost(w http.ResponseWriter, r *http.Request) {
	name, branch := r.FormValue("name"), r.FormValue("branch")
	if name == "" || branch == "" {
		http.Error(w, "name and branch required", 400)
		return
	}
	sm, err := s.submodule(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	tracked := s.trackedBranch(ctx, sm)
	g := git.New(sm.RepoDir())
	// Merge INTO the tracked branch: check it out first so the merge advances that
	// branch ref itself (submodule checkouts are frequently left detached), which
	// is exactly what the subsequent push publishes.
	if _, err := g.Run(ctx, "checkout", tracked); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := g.Merge(ctx, branch); err != nil {
		if errors.Is(err, git.ErrConflict) {
			// Abort the conflicted merge so the checkout (and origin) are left
			// exactly as found — no partial publish.
			_, _ = g.Run(ctx, "merge", "--abort")
			http.Error(w, "merge conflict", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	// Publish the merged tracked branch to the submodule's origin (branch-tracking
	// model) so other clones and the recorded pointer agree. No remote => a
	// single-host target with nothing to push. A failed push stops here, BEFORE
	// the pointer is advanced, so the recorded pointer never points past origin.
	remote, err := g.Remote(ctx)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if remote != "" {
		if err := g.Push(ctx, remote, tracked); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	// Advance + commit the beehive pointer (stages the submodule gitlink) and
	// publish the beehive root, reusing the same path planDelete/roiPost use. An
	// already-merged branch stages nothing (publishMain tolerates it) and is still
	// a success.
	if err := s.publishMain(ctx, "frontend: merge "+branch+" into "+tracked+" ("+name+")\n\nBeehive: frontend-merge "+name); err != nil {
		http.Error(w, "merged locally but publish to remote failed: "+err.Error(), 500)
		return
	}
	s.renderMerge(w, map[string]interface{}{
		"Selected": name, "Merged": true, "Branch": branch, "Tracked": tracked,
	})
}

// trackedBranch resolves the submodule's tracked branch from .gitmodules
// (submodule.<path>.branch) — the branch the beehive pointer follows, the same
// lookup `beehive submodule sync` uses. It defaults to "main" only when the entry
// is unset, so the merge target is never hardcoded.
func (s *Server) trackedBranch(ctx context.Context, sm repo.Submodule) string {
	rel := filepath.Join("submodules", sm.Name, "repo")
	branch, err := s.git.Run(ctx, "config", "-f", ".gitmodules", "submodule."+rel+".branch")
	if err != nil || branch == "" {
		return "main"
	}
	return branch
}

// submoduleAdd registers a target repo as a real tracked git submodule through
// the shared submod.Add (the same `git submodule add` the CLI runs), then commits
// the new .gitmodules + gitlink. The old handler just os.MkdirAll'd an inert dir
// with no repo/ and no tracking. The clone needs network/creds and can be slow,
// so it is bounded by submoduleAddTimeout and errors are surfaced to the user.
func (s *Server) submoduleAdd(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), submoduleAddTimeout)
	defer cancel()
	added, err := submod.Add(ctx, s.repo.Root, r.FormValue("url"), r.FormValue("name"), r.FormValue("branch"))
	if err != nil {
		switch {
		case errors.Is(err, submod.ErrURLRequired), errors.Is(err, submod.ErrInvalidName):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, submod.ErrExists):
			http.Error(w, err.Error(), http.StatusConflict)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	if err := s.publishMain(r.Context(), "frontend: add submodule "+added); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// submoduleLink records a from->to dependency through the cycle-checked links
// schema (submod.AddDep) and commits valid, sorted YAML — replacing the old raw
// `from: [to]` append that was neither schema-valid nor cycle-checked. A cycle,
// self-dependency, or empty edge is a client error rejected without writing.
func (s *Server) submoduleLink(w http.ResponseWriter, r *http.Request) {
	from, to := r.FormValue("from"), r.FormValue("to")
	if err := submod.AddDep(s.repo.Root, from, to); err != nil {
		switch {
		case errors.Is(err, submod.ErrInvalidDep):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, submod.ErrCycle):
			http.Error(w, err.Error(), http.StatusConflict)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	if err := s.publishMain(r.Context(), "frontend: link "+from+" -> "+to); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) envGet(w http.ResponseWriter, r *http.Request) {
	env, _ := parseEnv(filepath.Join(s.repo.Root, repo.InfraFile))
	s.render(w, "env_panel.html", map[string]interface{}{"Env": env})
}

func (s *Server) envDeploy(w http.ResponseWriter, r *http.Request) {
	target := r.FormValue("target")
	if target == "" {
		http.Error(w, "target required", 400)
		return
	}
	if err := deploy(filepath.Join(s.repo.Root, repo.InfraFile), target); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := s.publishMain(r.Context(), "frontend: deploy "+target); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.envGet(w, r)
}

func (s *Server) human(w http.ResponseWriter, r *http.Request) {
	subs, _ := s.repo.Submodules()
	type row struct {
		Sub  string
		Item PlanItem
	}
	var rows []row
	head := s.headSHA(r.Context())
	for _, sm := range subs {
		p, _ := s.planView(head, sm.PlanPath(), time.Now(), s.ttl())
		for _, it := range p.Items {
			if it.Status == StatusHuman {
				rows = append(rows, row{Sub: sm.Name, Item: it})
			}
		}
	}
	s.render(w, "human.html", map[string]interface{}{"Rows": rows})
}

// hygiene renders the standalone read-only hive-hygiene page: a full scan of the
// git cruft that accumulates under updateInstead (stale worktrees, orphan
// submodule gitlinks, drifted submodule checkouts, unexpected remotes), counts
// with a drill-down, and the beehive-hygiene cleanup skill as the remediation
// pointer. The handler is strictly diagnostic — scanHygiene mutates nothing.
func (s *Server) hygiene(w http.ResponseWriter, r *http.Request) {
	hyg, err := scanHygiene(r.Context(), s.repo.Root, s.git)
	if err != nil {
		hyg = Hygiene{Skill: hygieneSkill, Err: err.Error()}
	}
	s.render(w, "hygiene_panel.html", map[string]interface{}{"Hygiene": hyg})
}

// ttl is the resolved claim heartbeat TTL: a task's session+heartbeat is "active"
// within it and "stale" (GC-reclaimable) beyond it. It mirrors the runner's TTL
// (config ttl_minutes) so the frontend's active/stale derivation matches
// selection. A non-positive config value falls back to the 60m default.
func (s *Server) ttl() time.Duration {
	m := s.cfg.TTLMinutes
	if m <= 0 {
		m = 60
	}
	return time.Duration(m) * time.Minute
}

// streamIvl is the SSE re-read cadence for a live session transcript: how often
// sessionStream re-derives the file-backed transcript (following off-box main
// each tick) and pushes it if changed. It is short enough to feel token-live but
// bounded so a viewer never hammers git; tests override it via streamInterval to
// run fast. The default (1s) beats the 2s htmx poll it supersedes while the
// stream is connected.
func (s *Server) streamIvl() time.Duration {
	if s.streamInterval > 0 {
		return s.streamInterval
	}
	return time.Second
}

func pageParams(r *http.Request) (offset, limit int) {
	offset, limit = 0, 50
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	return
}

func atoi(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("nan")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

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
	"time"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/editor"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spencerharmon/beehive/internal/submod"
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
	return &Server{repo: r, cfg: cfg, git: git.New(r.Root), tmpl: t, editors: em}, nil
}

// Routes returns the mux wired to all handlers.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("GET /submodule/{name}", s.explorer)
	mux.HandleFunc("GET /submodule/{name}/branches", s.branches)
	mux.HandleFunc("GET /submodule/{name}/plan", s.plan)
	mux.HandleFunc("POST /submodule/{name}/plan/delete", s.planDelete)
	mux.HandleFunc("GET /submodule/{name}/sessions", s.sessionsList)
	mux.HandleFunc("GET /submodule/{name}/sessions/body", s.sessionsListBody)
	mux.HandleFunc("GET /submodule/{name}/session/{branch}", s.sessionView)
	mux.HandleFunc("GET /submodule/{name}/session/{branch}/body", s.sessionBody)
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
	// AI editor chat (browser): one worktree branch per session.
	mux.HandleFunc("GET /edit", s.editNew)
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

// subView is dashboard per-submodule status.
type subView struct {
	Name    string
	State   string
	Stamp   string
	Pending int
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	subs, err := s.repo.Submodules()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	var views []subView
	for _, sm := range subs {
		v := subView{Name: sm.Name, State: "active"}
		switch {
		case sm.Dormant():
			v.State = "dormant"
		case sm.NeedsBootstrap():
			v.State = "bootstrap"
		}
		v.Stamp, _ = sm.ROIStamp()
		if p, err := parsePlan(sm.PlanPath()); err == nil {
			for _, it := range p.Items {
				if it.Status != StatusDone {
					v.Pending++
				}
			}
		}
		views = append(views, v)
	}
	env, _ := parseEnv(filepath.Join(s.repo.Root, repo.InfraFile))
	s.render(w, "dashboard.html", map[string]interface{}{"Subs": views, "Env": env})
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
	for label, f := range map[string]string{
		"PLAN": repo.PlanFile, "ROI": repo.ROIFile,
		"INFRA": repo.InfraFile, "ARTIFACTS": repo.Artifacts,
	} {
		if b, err := os.ReadFile(filepath.Join(sm.Path, f)); err == nil {
			docs[label] = renderMarkdown(string(b))
		}
	}
	s.render(w, "explorer.html", map[string]interface{}{"Name": sm.Name, "Docs": docs})
}

func (s *Server) branches(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	off, lim := pageParams(r)
	cs, err := commitGraph(r.Context(), sm.RepoDir(), off, lim)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "branch_view.html", map[string]interface{}{
		"Name": sm.Name, "Commits": cs, "Next": off + lim, "Prev": off - lim,
	})
}

func (s *Server) plan(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, err := parsePlan(sm.PlanPath())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
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
	if err := s.commit(r.Context(), "frontend: delete PLAN "+sm.Name+" (force rebootstrap from ROI)"); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := s.publishMain(r.Context()); err != nil {
		http.Error(w, "deleted locally but publish to remote failed: "+err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/submodule/"+sm.Name+"/plan", http.StatusSeeOther)
}

// publishMain pushes the beehived primary checkout's main to the remote so other
// hosts' honeybees (which branch off origin/main) see committed changes. No-op
// when the repo has no remote (single-host: honeybees branch off local main,
// already updated). On a non-fast-forward it fetches, merges, and retries.
func (s *Server) publishMain(ctx context.Context) error {
	remote, err := s.git.Remote(ctx)
	if err != nil || remote == "" {
		return err
	}
	if err := s.git.Push(ctx, remote, "main"); err == nil {
		return nil
	}
	if err := s.git.Fetch(ctx, remote, "main"); err != nil {
		return err
	}
	if _, err := s.git.Run(ctx, "merge", "--no-edit", "FETCH_HEAD"); err != nil {
		_, _ = s.git.Run(ctx, "merge", "--abort")
		return err
	}
	return s.git.Push(ctx, remote, "main")
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
	if err := s.commit(r.Context(), "frontend: edit ROI "+sm.Name); err != nil {
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
	if err := s.commit(r.Context(), "frontend: update secret "+key); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.secretsGet(w, r)
}

func (s *Server) mergeGet(w http.ResponseWriter, r *http.Request) {
	subs, _ := s.repo.Submodules()
	s.render(w, "merge_panel.html", map[string]interface{}{"Subs": subs})
}

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
	g := git.New(sm.RepoDir())
	if err := g.Merge(r.Context(), branch); err != nil {
		if errors.Is(err, git.ErrConflict) {
			http.Error(w, "merge conflict", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	if err := s.commit(r.Context(), "frontend: merge "+branch+" in "+name); err != nil && !errors.Is(err, git.ErrNothing) {
		http.Error(w, err.Error(), 500)
		return
	}
	s.mergeGet(w, r)
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
	if err := s.commit(r.Context(), "frontend: add submodule "+added); err != nil && !errors.Is(err, git.ErrNothing) {
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
	if err := s.commit(r.Context(), "frontend: link "+from+" -> "+to); err != nil && !errors.Is(err, git.ErrNothing) {
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
	if err := s.commit(r.Context(), "frontend: deploy "+target); err != nil && !errors.Is(err, git.ErrNothing) {
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
	for _, sm := range subs {
		p, _ := parsePlan(sm.PlanPath())
		for _, it := range p.Items {
			if it.Status == StatusHuman {
				rows = append(rows, row{Sub: sm.Name, Item: it})
			}
		}
	}
	s.render(w, "human.html", map[string]interface{}{"Rows": rows})
}

func (s *Server) commit(ctx context.Context, msg string) error {
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return s.git.Commit(c, msg)
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

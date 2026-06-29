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
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/repo"
)

//go:embed templates/*.html
var tmplFS embed.FS

//go:embed assets/*
var assetFS embed.FS

// Server holds the parsed templates and the repo it serves.
type Server struct {
	repo *repo.Repo
	cfg  config.Config
	git  *git.Repo
	tmpl *template.Template
}

// New builds a Server over the beehive repo at root.
func New(r *repo.Repo, cfg config.Config) (*Server, error) {
	t, err := template.ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{repo: r, cfg: cfg, git: git.New(r.Root), tmpl: t}, nil
}

// Routes returns the mux wired to all handlers.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("GET /submodule/{name}", s.explorer)
	mux.HandleFunc("GET /submodule/{name}/branches", s.branches)
	mux.HandleFunc("GET /submodule/{name}/plan", s.plan)
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
	docs := map[string]string{}
	for label, f := range map[string]string{
		"PLAN": repo.PlanFile, "ROI": repo.ROIFile,
		"INFRA": repo.InfraFile, "ARTIFACTS": repo.Artifacts,
	} {
		if b, err := os.ReadFile(filepath.Join(sm.Path, f)); err == nil {
			docs[label] = string(b)
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

func (s *Server) roiGet(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	b, _ := os.ReadFile(sm.ROIPath())
	s.render(w, "roi_editor.html", map[string]interface{}{"Name": sm.Name, "Body": string(b)})
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
	s.render(w, "roi_editor.html", map[string]interface{}{"Name": sm.Name, "Body": body, "Saved": true})
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
	if err := setSecret(r.Context(), s.cfg.GPGHome, p, s.cfg.Recipient, key, val); err != nil {
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

func (s *Server) submoduleAdd(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if name == "" || filepath.Base(name) != name {
		http.Error(w, "invalid name", 400)
		return
	}
	if err := os.MkdirAll(filepath.Join(s.repo.Root, "submodules", name), 0o755); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := s.commit(r.Context(), "frontend: add submodule "+name); err != nil && !errors.Is(err, git.ErrNothing) {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) submoduleLink(w http.ResponseWriter, r *http.Request) {
	from, to := r.FormValue("from"), r.FormValue("to")
	if from == "" || to == "" {
		http.Error(w, "from and to required", 400)
		return
	}
	p := filepath.Join(s.repo.Root, repo.LinksFile)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if _, err := f.WriteString(from + ": [" + to + "]\n"); err != nil {
		f.Close()
		http.Error(w, err.Error(), 500)
		return
	}
	f.Close()
	if err := s.commit(r.Context(), "frontend: link "+from+" -> "+to); err != nil {
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

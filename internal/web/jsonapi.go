package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spencerharmon/beehive/internal/secrets"
)

// ---- JSON API: one additive endpoint per existing HTML view ----
//
// Every handler here mirrors an existing HTML view's route and data-gathering
// function exactly (dashboard/plan/roiGet/docExplorer/doc/branches/commitView/
// stats/hygiene), but marshals the SAME underlying data as JSON instead of
// rendering a template. None of these replace the HTML routes above — they are
// purely additive surfaces for the beemacs Emacs client (and any other
// browser-free consumer) to read the exact same state the HTML pages show,
// without scraping rendered markup. writeJSON (editor.go) is the shared JSON
// encoder every handler below uses, matching the existing /api/editor/* JSON
// convention.

// dashboardJSON mirrors dashboard (web.go): the tracked-submodule cards
// (state, ROI stamp, pending/human counts, active-honeybee count) plus the
// hygiene/root-file/skills-drift widgets the HTML dashboard shows.
func (s *Server) dashboardJSON(w http.ResponseWriter, r *http.Request) {
	views, err := s.subViews(r.Context(), time.Now(), s.ttl())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	h, err := scanHygiene(r.Context(), s.repo.Root, s.git)
	if err != nil {
		h = Hygiene{Err: err.Error()}
	}
	rootFiles := s.rootFileLinks()
	skillsDrift := s.skillsDrift()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"subs":              views,
		"hygiene":           h,
		"bootstrap":         s.bootstrapState(),
		"root_files":        rootFiles,
		"root_files_drift":  rootFilesDrift(rootFiles),
		"skills_drift":      skillsDrift,
		"instruction_drift": rootFilesDrift(rootFiles) || len(skillsDrift) > 0,
	})
}

// planJSON mirrors plan (web.go): a submodule's live task list, identical to
// planViewData — the same claim/running state and doc links the plan page and
// the runner's own selection use.
func (s *Server) planJSON(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown submodule"})
		return
	}
	p, err := s.planViewData(r.Context(), sm)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"name": sm.Name, "plan": p})
}

// roiJSON mirrors roiGet (web.go): a submodule's raw ROI.md content plus its
// tracked remote url, without the HTML preview render.
func (s *Server) roiJSON(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown submodule"})
		return
	}
	b, _ := os.ReadFile(sm.ROIPath())
	rel := filepath.Join("submodules", sm.Name, "repo")
	remoteURL, _ := s.git.Run(r.Context(), "config", "-f", ".gitmodules", "submodule."+rel+".url")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":       sm.Name,
		"body":       string(b),
		"remote_url": strings.TrimSpace(remoteURL),
	})
}

// docsJSON mirrors docExplorer (web.go): the flat list of every file under a
// submodule's docs/ tree.
func (s *Server) docsJSON(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown submodule"})
		return
	}
	entries, err := docTree(sm)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"name": sm.Name, "docs": entries})
}

// docJSON mirrors doc (web.go): one doc's raw content. Routed under a distinct
// "doc.json/{file...}" segment (rather than reusing "doc/{file...}") so the two
// wildcard-terminated routes never collide in the net/http mux.
func (s *Server) docJSON(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown submodule"})
		return
	}
	file := r.PathValue("file")
	if !safeDocPath(file) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "invalid path"})
		return
	}
	b, err := os.ReadFile(filepath.Join(sm.Path, "docs", filepath.FromSlash(file)))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name": sm.Name, "file": file, "body": string(b),
	})
}

// branchesJSON mirrors branches (web.go): the paginated commit list, including
// the same doc-href/delivery-flip enrichment the HTML branch view applies —
// but flat (no date sectioning, which is an HTML-only grouping).
func (s *Server) branchesJSON(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown submodule"})
		return
	}
	off, lim := pageParams(r)
	cs, err := commitGraph(r.Context(), sm.RepoDir(), off, lim)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	deliveries := indexDeliveries(s.buildDeliveries(r.Context(), s.headSHA(r.Context()), sm, doneTaskIDs(sm)))
	for i := range cs {
		cs[i].DocHref = resolveDocHref(sm, cs[i].DocPath)
		if d, ok := deliveries[cs[i].DocTask]; ok {
			cs[i].FlipSHA = d.FlipSHA
			cs[i].FlipHref = d.FlipHref
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":     sm.Name,
		"commits":  cs,
		"offset":   off,
		"limit":    lim,
		"has_next": len(cs) == lim,
	})
}

// commitJSON mirrors commitView (delivery.go): one hive commit's PLAN.md
// before/after content, scoped to a single submodule, without the HTML
// row/hunk diff rendering (an HTML-only view model).
func (s *Server) commitJSON(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown submodule"})
		return
	}
	sha := r.PathValue("sha")
	if !safeSHA(sha) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "invalid sha"})
		return
	}
	full, err := s.git.RevParse(r.Context(), sha)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	meta, err := s.git.Run(r.Context(), "show", "-s", "--date=short", "--format=%an%x1f%ad%x1f%s", full)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	f := strings.SplitN(meta, "\x1f", 3)
	for len(f) < 3 {
		f = append(f, "")
	}
	planPath := planRelPath(sm)
	before, _ := s.git.Show(r.Context(), full+"^", planPath)
	after, _ := s.git.Show(r.Context(), full, planPath)
	shortSHA := full[:min(12, len(full))]
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":        sm.Name,
		"sha":         shortSHA,
		"author":      f[0],
		"date":        f[1],
		"subject":     f[2],
		"plan_before": before,
		"plan_after":  after,
	})
}

// statsJSON mirrors stats (stats.go): the same unfiltered/grouped stats data,
// without the HTML-only filter-chip view model.
func (s *Server) statsJSON(w http.ResponseWriter, r *http.Request) {
	filters := parseFilters(r)
	groupBy := parseGroupBy(r)
	if len(filters) > 0 || len(groupBy) > 0 {
		rows, err := s.computeGroupedStats(r.Context(), filters, groupBy)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"grouped": rows})
		return
	}
	subs, total, err := s.computeStats(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"subs": subs, "total": total})
}

// skillsJSON mirrors hygiene (web.go): the cruft-scan classes/packs, the dance
// (skills) registry's identity list, and the view-cache widget, as the
// beemacs client's "skills registry" surface (/skills pre-rename redirects to
// /hygiene for the same reason).
func (s *Server) skillsJSON(w http.ResponseWriter, r *http.Request) {
	hyg, err := scanHygiene(r.Context(), s.repo.Root, s.git)
	if err != nil {
		hyg = Hygiene{Err: err.Error()}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"hygiene": hyg,
		"dances":  s.dancePanels(),
		"cache":   cacheWidget(s.cache),
	})
}

// secretsJSON mirrors secretsGet/submoduleSecretsGet (web.go): the SAME
// key-names-only view every HTML secrets_panel shows, but as a single JSON
// document covering BOTH scopes so the beemacs client can render one combined
// view — the active repo's global secrets (root SECRETS.yaml.gpg) plus every
// tracked submodule's OWN secrets file. Like the HTML panels, values are NEVER
// returned, only key names, and every read goes through listSecretKeys with
// the ACTIVE repo's own keyring (s.cfg.GPGHome) — no cross-repo keyring reuse.
func (s *Server) secretsJSON(w http.ResponseWriter, r *http.Request) {
	globalKeys, err := listSecretKeys(r.Context(), s.cfg.GPGHome, filepath.Join(s.repo.Root, repo.SecretsFile))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	subs, err := s.repo.Submodules()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	subSecrets := make([]map[string]interface{}, 0, len(subs))
	for _, sm := range subs {
		keys, err := listSecretKeys(r.Context(), s.cfg.GPGHome, secrets.SubmodulePath(s.repo.Root, sm.Name))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		subSecrets = append(subSecrets, map[string]interface{}{
			"name": sm.Name,
			"keys": keys,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"global":     globalKeys,
		"submodules": subSecrets,
	})
}

// secretsWriteJSON mirrors secretsPost/submoduleSecretsPost (web.go): writes
// one key into either the active repo's global SECRETS.yaml.gpg (no
// "submodule" field, or an empty one) or one named submodule's OWN secrets
// file, reusing the SAME setSecret validation/GPG-write logic those HTML
// handlers call — never duplicating it. Like every /api/editor/* and *.json
// write handler, it returns {"error": "..."} (never a bare http.Error body) on
// failure so the JSON client always gets a parseable response.
func (s *Server) secretsWriteJSON(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Submodule string `json:"submodule"`
		Key       string `json:"key"`
		Value     string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key required"})
		return
	}
	var p, label string
	if req.Submodule == "" {
		p = filepath.Join(s.repo.Root, repo.SecretsFile)
		label = req.Key
	} else {
		sm, err := s.submodule(req.Submodule)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown submodule"})
			return
		}
		p = secrets.SubmodulePath(s.repo.Root, sm.Name)
		label = sm.Name + "/" + req.Key
	}
	if err := setSecret(r.Context(), s.cfg.GPGHome, p, s.cfg.GPGRecipient, req.Key, req.Value); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := s.publishMain(r.Context(), "frontend: update secret "+label); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.secretsJSON(w, r)
}

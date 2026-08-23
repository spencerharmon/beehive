package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
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

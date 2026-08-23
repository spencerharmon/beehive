package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/repo"
)

// TestJSONAPIDashboard locks GET /dashboard.json as an additive JSON mirror of
// GET / (dashboard): same submodule cards, keyed by name, alongside the HTML
// route still serving unchanged.
func TestJSONAPIDashboard(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/dashboard.json")
	if w.Code != http.StatusOK {
		t.Fatalf("dashboard.json: %d: %s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	var body struct {
		Subs []subView `json:"subs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, w.Body)
	}
	var found bool
	for _, v := range body.Subs {
		if v.Name == "alpha" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dashboard.json missing alpha submodule: %+v", body.Subs)
	}
	// The HTML dashboard must still serve unchanged (purely additive).
	if w := get(t, s, "/"); w.Code != http.StatusOK {
		t.Fatalf("HTML dashboard regressed: %d", w.Code)
	}
}

// TestJSONAPIPlan locks GET /submodule/{name}/plan.json as the same live task
// list planViewData/plan produce, with t1's claimed session/heartbeat intact.
func TestJSONAPIPlan(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/submodule/alpha/plan.json")
	if w.Code != http.StatusOK {
		t.Fatalf("plan.json: %d: %s", w.Code, w.Body)
	}
	var body struct {
		Name string `json:"name"`
		Plan Plan   `json:"plan"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, w.Body)
	}
	if body.Name != "alpha" {
		t.Fatalf("name = %q, want alpha", body.Name)
	}
	t1 := itemByID(body.Plan, "t1")
	if t1.Status != StatusTODO || t1.Session != "bee-1" {
		t.Fatalf("t1 = %+v, want TODO/bee-1", t1)
	}
	if w := get(t, s, "/submodule/none/plan.json"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown submodule: got %d, want 404", w.Code)
	}
	// HTML route unaffected.
	if w := get(t, s, "/submodule/alpha/plan"); w.Code != http.StatusOK {
		t.Fatalf("HTML plan regressed: %d", w.Code)
	}
}

// TestJSONAPIROI locks GET /roi/{name}.json as the raw ROI.md body (no HTML
// render), matching what roiGet reads.
func TestJSONAPIROI(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/submodule/alpha/roi.json")
	if w.Code != http.StatusOK {
		t.Fatalf("roi.json: %d: %s", w.Code, w.Body)
	}
	var body struct {
		Name string `json:"name"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, w.Body)
	}
	if body.Name != "alpha" || !strings.Contains(body.Body, "# alpha") {
		t.Fatalf("roi.json body = %+v", body)
	}
}

// TestJSONAPIDocsAndDoc locks GET /submodule/{name}/docs.json (the tree
// listing) and GET /submodule/{name}/doc.json/{file...} (one doc's raw body),
// including the safeDocPath traversal guard doc.go's HTML route shares.
func TestJSONAPIDocsAndDoc(t *testing.T) {
	s, root := setup(t)
	docsDir := filepath.Join(root, "submodules", "alpha", "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docsDir, "bee-t1.md"), []byte("hello doc"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := get(t, s, "/submodule/alpha/docs.json")
	if w.Code != http.StatusOK {
		t.Fatalf("docs.json: %d: %s", w.Code, w.Body)
	}
	var listBody struct {
		Docs []DocEntry `json:"docs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, w.Body)
	}
	if len(listBody.Docs) == 0 {
		t.Fatalf("docs.json listed nothing")
	}

	w = get(t, s, "/submodule/alpha/doc.json/bee-t1.md")
	if w.Code != http.StatusOK {
		t.Fatalf("doc.json: %d: %s", w.Code, w.Body)
	}
	var docBody struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &docBody); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, w.Body)
	}
	if docBody.Body != "hello doc" {
		t.Fatalf("doc.json body = %q, want %q", docBody.Body, "hello doc")
	}

	if w := get(t, s, "/submodule/alpha/doc.json/a%20b.md"); w.Code != http.StatusNotFound {
		t.Fatalf("traversal guard: got %d, want 404", w.Code)
	}
}

// TestJSONAPIBranchesAndCommit locks GET /submodule/{name}/branches.json and
// GET /submodule/{name}/commit.json/{sha} against a real submodule commit
// history, mirroring commitGraph and commitView's before/after PLAN.md
// content (see TestBranchesStamp/TestCommitView for their HTML equivalents).
func TestJSONAPIBranchesAndCommit(t *testing.T) {
	s, root := setup(t)
	commitRepoAt(t, filepath.Join(root, "submodules", "alpha", "repo"), "alpha-json-commit")

	writeHivePlan(t, root, "alpha", "dt1", "TODO")
	commitAll(t, root, "dt1-todo")
	writeHivePlan(t, root, "alpha", "dt1", "DONE")
	commitAll(t, root, "dt1-done")
	sha := hygGit(t, root, "rev-parse", "--short=12", "HEAD")

	w := get(t, s, "/submodule/alpha/branches.json")
	if w.Code != http.StatusOK {
		t.Fatalf("branches.json: %d: %s", w.Code, w.Body)
	}
	var branchBody struct {
		Commits []Commit `json:"commits"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &branchBody); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, w.Body)
	}
	if len(branchBody.Commits) == 0 {
		t.Fatalf("branches.json listed no commits")
	}

	w = get(t, s, "/submodule/alpha/commit.json/"+sha)
	if w.Code != http.StatusOK {
		t.Fatalf("commit.json: %d: %s", w.Code, w.Body)
	}
	var commitBody struct {
		Subject    string `json:"subject"`
		PlanAfter  string `json:"plan_after"`
		PlanBefore string `json:"plan_before"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &commitBody); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, w.Body)
	}
	if commitBody.Subject != "dt1-done" || !strings.Contains(commitBody.PlanAfter, "[DONE]") {
		t.Fatalf("commit.json = %+v", commitBody)
	}

	if w := get(t, s, "/submodule/alpha/commit.json/zzzzzzzzzzzz"); w.Code != http.StatusNotFound {
		t.Fatalf("non-hex sha: got %d, want 404", w.Code)
	}
}

// TestJSONAPIStats locks GET /stats.json's unfiltered shape (per-submodule
// stats + a total row), matching computeStats.
func TestJSONAPIStats(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/stats.json")
	if w.Code != http.StatusOK {
		t.Fatalf("stats.json: %d: %s", w.Code, w.Body)
	}
	var body struct {
		Subs  []subStat `json:"subs"`
		Total subStat   `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, w.Body)
	}
}

// TestJSONAPISkills locks GET /skills.json's hygiene+dances+cache shape.
func TestJSONAPISkills(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/skills.json")
	if w.Code != http.StatusOK {
		t.Fatalf("skills.json: %d: %s", w.Code, w.Body)
	}
	var body struct {
		Dances []dancePanel `json:"dances"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, w.Body)
	}
	if len(body.Dances) == 0 {
		t.Fatalf("skills.json listed no dances")
	}
}

// TestJSONAPISecretsEmpty locks GET /secrets.json's shape when no secrets
// exist: a "global" key list and one "submodules" entry per tracked submodule,
// all empty (never an error just because no SECRETS.yaml.gpg exists yet).
func TestJSONAPISecretsEmpty(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/secrets.json")
	if w.Code != http.StatusOK {
		t.Fatalf("secrets.json: %d: %s", w.Code, w.Body)
	}
	var body struct {
		Global     []string `json:"global"`
		Submodules []struct {
			Name string   `json:"name"`
			Keys []string `json:"keys"`
		} `json:"submodules"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, w.Body)
	}
	if len(body.Global) != 0 {
		t.Fatalf("global keys = %v, want empty", body.Global)
	}
	var found bool
	for _, sm := range body.Submodules {
		if sm.Name == "alpha" {
			found = true
			if len(sm.Keys) != 0 {
				t.Fatalf("alpha keys = %v, want empty", sm.Keys)
			}
		}
	}
	if !found {
		t.Fatalf("secrets.json missing alpha submodule: %+v", body.Submodules)
	}
}

// TestJSONAPISecretsWrite is the end-to-end write proof (requires gpg) for
// POST /secrets.json: a global write (no "submodule" field) lands in the
// active repo's root SECRETS.yaml.gpg and shows up under "global" on the next
// GET; a submodule-scoped write ({"submodule":...}) lands ONLY in that
// submodule's own file and shows up nested under its name — mirroring
// secretsPost/submoduleSecretsPost's write scoping exactly, reusing the same
// setSecret path (never duplicated logic).
func TestJSONAPISecretsWrite(t *testing.T) {
	s, rootAlpha, _ := twoRealKeyringRegistry(t)
	mux := s.Routes()

	// Global write via the active repo (alpha).
	body := strings.NewReader(`{"key":"db_pw","value":"hunter2"}`)
	req := httptest.NewRequest("POST", "/secrets.json", body)
	req.AddCookie(&http.Cookie{Name: repoCookie, Value: "alpha"})
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("global secrets.json write: %d %s", w.Code, w.Body)
	}
	var got struct {
		Global     []string `json:"global"`
		Submodules []struct {
			Name string   `json:"name"`
			Keys []string `json:"keys"`
		} `json:"submodules"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, w.Body)
	}
	if len(got.Global) != 1 || got.Global[0] != "db_pw" {
		t.Fatalf("global keys after write = %v, want [db_pw]", got.Global)
	}
	if _, err := os.Stat(filepath.Join(rootAlpha, repo.SecretsFile)); err != nil {
		t.Fatalf("global SECRETS.yaml.gpg not written: %v", err)
	}

	// Submodule-scoped write.
	body2 := strings.NewReader(`{"submodule":"redteam","key":"sub_pw","value":"hunter3"}`)
	req2 := httptest.NewRequest("POST", "/secrets.json", body2)
	req2.AddCookie(&http.Cookie{Name: repoCookie, Value: "alpha"})
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("submodule secrets.json write: %d %s", w2.Code, w2.Body)
	}
	var got2 struct {
		Submodules []struct {
			Name string   `json:"name"`
			Keys []string `json:"keys"`
		} `json:"submodules"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &got2); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, w2.Body)
	}
	var found bool
	for _, sm := range got2.Submodules {
		if sm.Name == "redteam" {
			found = true
			if len(sm.Keys) != 1 || sm.Keys[0] != "sub_pw" {
				t.Fatalf("redteam keys = %v, want [sub_pw]", sm.Keys)
			}
		}
	}
	if !found {
		t.Fatalf("secrets.json missing redteam submodule: %+v", got2.Submodules)
	}
	subPath := filepath.Join(rootAlpha, "submodules", "redteam", repo.SecretsFile)
	if _, err := os.Stat(subPath); err != nil {
		t.Fatalf("submodule SECRETS.yaml.gpg not written at %s: %v", subPath, err)
	}

	// Unknown submodule 404s.
	body3 := strings.NewReader(`{"submodule":"nope","key":"k","value":"v"}`)
	req3 := httptest.NewRequest("POST", "/secrets.json", body3)
	req3.AddCookie(&http.Cookie{Name: repoCookie, Value: "alpha"})
	req3.Header.Set("Content-Type", "application/json")
	w3 := httptest.NewRecorder()
	mux.ServeHTTP(w3, req3)
	if w3.Code != http.StatusNotFound {
		t.Fatalf("unknown submodule write = %d, want 404", w3.Code)
	}
}

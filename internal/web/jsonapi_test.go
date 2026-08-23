package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
)

// seedStaleWorktrees plants two unregistered edit-*/beehive-* directories under
// .worktrees (the stale-worktree cruft cleanup-stale removes) plus a
// non-matching directory that must never be touched, after a base commit so git
// worktree queries resolve a HEAD. Returns the stale names and the keep name.
func seedStaleWorktrees(t *testing.T, root string) (stale []string, keep string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "seed"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	hygGit(t, root, "add", "-A")
	hygGit(t, root, "commit", "-q", "-m", "seed")
	wtDir := filepath.Join(root, ".worktrees")
	stale = []string{"edit-roi-alpha-999", "beehive-1782800000-222"}
	keep = "random-keep"
	for _, n := range append(append([]string{}, stale...), keep) {
		if err := os.MkdirAll(filepath.Join(wtDir, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return stale, keep
}

// TestDancesRenderOnHygienePage locks that the combined hygiene page renders
// every registered dance with a dry-run control (discoverable) AND still carries
// the read-only hygiene scan — the two surfaces are merged onto one page.
func TestDancesRenderOnHygienePage(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/hygiene")
	if w.Code != http.StatusOK {
		t.Fatalf("hygiene page: got %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"cleanup-stale", "gc", "resources", "infra-conventions", "repair-plan", "prune-empty-sessions", "Dry-run", "Dances", "Hive hygiene"} {
		if !strings.Contains(body, want) {
			t.Fatalf("hygiene page missing %q:\n%s", want, body)
		}
	}
}

// TestSkillsURLRedirects locks that the pre-rename /skills URL redirects to the
// combined hygiene page so old links/bookmarks keep working.
func TestSkillsURLRedirects(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/skills")
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("/skills: got %d want 301", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/hygiene" {
		t.Fatalf("/skills redirect Location = %q want /hygiene", loc)
	}
}

// TestSkillUnknownIs404 is the "unknown dance errors" acceptance: neither
// dry-run nor apply resolves an unregistered name.
func TestSkillUnknownIs404(t *testing.T) {
	s, _ := setup(t)
	if w := postForm(t, s, "/dances/nope/plan", url.Values{}); w.Code != http.StatusNotFound {
		t.Fatalf("unknown plan: got %d want 404", w.Code)
	}
	if w := postForm(t, s, "/dances/nope/apply", url.Values{"confirm": {"on"}}); w.Code != http.StatusNotFound {
		t.Fatalf("unknown apply: got %d want 404", w.Code)
	}
}

// TestSkillResourcesReportOnly proves a report-only dance produces a dry-run
// inventory and refuses apply (no mutation path at all). It also locks that the
// inventory is per-submodule: no hive-wide "root" deploy-env line (blue/green is
// not a global concept).
func TestSkillResourcesReportOnly(t *testing.T) {
	s, _ := setup(t)
	plan := postForm(t, s, "/dances/resources/plan", url.Values{})
	if plan.Code != http.StatusOK {
		t.Fatalf("resources plan: got %d", plan.Code)
	}
	b := plan.Body.String()
	if !strings.Contains(b, "alpha") {
		t.Fatalf("resources plan missing submodule inventory:\n%s", b)
	}
	// The coordination root is not a deploy target: no "root: active ..." line.
	if strings.Contains(b, "root:") {
		t.Fatalf("resources plan must not present a hive-wide root deploy line:\n%s", b)
	}
	if w := postForm(t, s, "/dances/resources/apply", url.Values{"confirm": {"on"}}); w.Code != http.StatusBadRequest {
		t.Fatalf("report-only apply: got %d want 400", w.Code)
	}
}

// TestSkillCleanupStaleConfirmGateAndApply is the destructive-dance acceptance:
// the dry-run lists exactly the stale dirs without mutating, an unconfirmed
// apply refuses (mutating nothing), and only a confirmed apply performs
// precisely the proposed removals while sparing the non-matching directory.
func TestSkillCleanupStaleConfirmGateAndApply(t *testing.T) {
	s, root := setup(t)
	stale, keep := seedStaleWorktrees(t, root)
	exists := func(n string) bool {
		_, err := os.Stat(filepath.Join(root, ".worktrees", n))
		return err == nil
	}

	// Dry-run: lists exactly the stale dirs as remove actions; mutates nothing.
	plan := postForm(t, s, "/dances/cleanup-stale/plan", url.Values{})
	if plan.Code != http.StatusOK {
		t.Fatalf("plan: got %d", plan.Code)
	}
	pb := plan.Body.String()
	if !strings.Contains(pb, "remove") {
		t.Fatalf("plan missing remove action:\n%s", pb)
	}
	for _, n := range stale {
		if !strings.Contains(pb, n) {
			t.Fatalf("plan missing stale %q:\n%s", n, pb)
		}
		if !exists(n) {
			t.Fatalf("dry-run must not remove %q", n)
		}
	}

	// Apply WITHOUT confirm: the gate refuses, asks to confirm, mutates nothing.
	gate := postForm(t, s, "/dances/cleanup-stale/apply", url.Values{})
	if gate.Code != http.StatusOK {
		t.Fatalf("unconfirmed apply: got %d want 200", gate.Code)
	}
	if b := gate.Body.String(); !strings.Contains(b, "Confirmation required") {
		t.Fatalf("unconfirmed apply must ask to confirm:\n%s", b)
	}
	for _, n := range stale {
		if !exists(n) {
			t.Fatalf("unconfirmed apply must not remove %q", n)
		}
	}

	// Apply WITH confirm: removes exactly the stale dirs, keeps the non-matching.
	done := postForm(t, s, "/dances/cleanup-stale/apply", url.Values{"confirm": {"on"}})
	if done.Code != http.StatusOK {
		t.Fatalf("confirmed apply: got %d", done.Code)
	}
	if b := done.Body.String(); !strings.Contains(b, "applied") {
		t.Fatalf("confirmed apply must report applied:\n%s", b)
	}
	for _, n := range stale {
		if exists(n) {
			t.Fatalf("confirmed apply must remove %q", n)
		}
	}
	if !exists(keep) {
		t.Fatalf("confirmed apply must keep non-matching %q", keep)
	}
}

// TestSkillInfraConventionsAppliesExactPlan proves the non-destructive, diff-
// previewing dance normalizes each SUBMODULE's OWN INFRASTRUCTURE.md and never the
// hive coordination root (blue/green is per-submodule, not a global concept). With
// alpha lacking the markers and bravo already declaring them, the dry-run proposes
// the markers only for alpha's submodules/alpha/INFRASTRUCTURE.md; apply (no
// confirm) writes exactly that; bravo is byte-for-byte untouched; the root is never
// given deploy markers; and a second dry-run is a no-op.
func TestSkillInfraConventionsAppliesExactPlan(t *testing.T) {
	s, root := setup(t)
	// bravo already declares its own markers -> a no-op the dance must skip.
	bravo := filepath.Join(root, "submodules", "bravo")
	if err := os.MkdirAll(bravo, 0o755); err != nil {
		t.Fatal(err)
	}
	const bravoInfra = "# infra\nActive: green\nEnvironments: blue, green\n"
	if err := os.WriteFile(filepath.Join(bravo, repo.InfraFile), []byte(bravoInfra), 0o644); err != nil {
		t.Fatal(err)
	}
	rootInfra := filepath.Join(root, repo.InfraFile)
	alphaTarget := filepath.ToSlash(filepath.Join("submodules", "alpha", repo.InfraFile))

	plan := postForm(t, s, "/dances/infra-conventions/plan", url.Values{})
	if plan.Code != http.StatusOK {
		t.Fatalf("plan: got %d", plan.Code)
	}
	pb := plan.Body.String()
	// The proposed markers, scoped to alpha's OWN INFRASTRUCTURE.md path.
	for _, want := range []string{"Active: blue", "Environments: blue, green", alphaTarget} {
		if !strings.Contains(pb, want) {
			t.Fatalf("plan missing %q:\n%s", want, pb)
		}
	}
	// bravo already declares its markers -> never proposed (per-submodule scan).
	if strings.Contains(pb, "bravo") {
		t.Fatalf("plan must not propose the already-conventional bravo:\n%s", pb)
	}

	done := postForm(t, s, "/dances/infra-conventions/apply", url.Values{})
	if done.Code != http.StatusOK {
		t.Fatalf("apply: got %d body=%s", done.Code, done.Body)
	}
	// alpha's OWN doc got exactly the conventional markers.
	got, err := os.ReadFile(filepath.Join(root, "submodules", "alpha", repo.InfraFile))
	if err != nil {
		t.Fatalf("read applied alpha infra: %v", err)
	}
	if want := "Active: blue\nEnvironments: blue, green\n"; string(got) != want {
		t.Fatalf("alpha applied content = %q, want %q", string(got), want)
	}
	// bravo is byte-for-byte untouched by alpha's normalization.
	if bb, _ := os.ReadFile(filepath.Join(bravo, repo.InfraFile)); string(bb) != bravoInfra {
		t.Fatalf("bravo INFRASTRUCTURE.md changed: %q", bb)
	}
	// The coordination root's INFRASTRUCTURE.md stays EMPTY: infra-conventions
	// never writes blue/green deploy markers to the hive root (repo.Init seeds it
	// empty; a global write would have filled in "Active: ..."). This is the exact
	// inversion of the old global behavior.
	if rb, err := os.ReadFile(rootInfra); err != nil || string(rb) != "" {
		t.Fatalf("root INFRASTRUCTURE.md must stay empty (no blue/green markers), got %q err=%v", rb, err)
	}

	again := postForm(t, s, "/dances/infra-conventions/plan", url.Values{})
	if b := again.Body.String(); !strings.Contains(b, "already") {
		t.Fatalf("second plan should be a no-op:\n%s", b)
	}
}

// TestSkillRepairPlanFixesCorruptStamp is the plan-repair acceptance: an
// unparseable PLAN.md carrying the empty session=/heartbeat= OOM-mid-write
// signature is (a) surfaced by the dry-run with the exact task and dropped
// stamps, (b) left untouched by an UNCONFIRMED apply (destructive gate), and
// (c) surgically repaired to a parseable file by a CONFIRMED apply — dropping
// only the dead claim while preserving attempts/deps/weight/status/body.
func TestSkillRepairPlanFixesCorruptStamp(t *testing.T) {
	s, root := setup(t)
	planPath := filepath.Join(root, "submodules", "alpha", repo.PlanFile)
	const corrupt = "<!-- Beehive-ROI: abc123 -->\n# Plan\n\n" +
		"## broken [DONE] <!-- attempts=2 deps=x,y weight=8 session= heartbeat= -->\nbody stays\nDoc: d.md\n"
	if err := os.WriteFile(planPath, []byte(corrupt), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Parse(corrupt); err == nil {
		t.Fatal("precondition: fixture must be unparseable")
	}

	// (a) Dry-run surfaces the task and the exact repair.
	dry := postForm(t, s, "/dances/repair-plan/plan", url.Values{})
	if dry.Code != http.StatusOK {
		t.Fatalf("plan: got %d", dry.Code)
	}
	db := dry.Body.String()
	for _, want := range []string{"broken", "drop empty", "session&#43;heartbeat"} {
		if !strings.Contains(db, want) {
			t.Fatalf("dry-run missing %q:\n%s", want, db)
		}
	}

	// (b) Unconfirmed apply refuses and mutates nothing (still corrupt on disk).
	gate := postForm(t, s, "/dances/repair-plan/apply", url.Values{})
	if gate.Code != http.StatusOK {
		t.Fatalf("unconfirmed apply: got %d want 200", gate.Code)
	}
	if !strings.Contains(strings.ToLower(gate.Body.String()), "confirm") {
		t.Fatalf("unconfirmed apply must ask to confirm:\n%s", gate.Body.String())
	}
	if b, _ := os.ReadFile(planPath); string(b) != corrupt {
		t.Fatalf("unconfirmed apply mutated the file:\n%s", b)
	}

	// (c) Confirmed apply repairs the file to a parseable state.
	done := postForm(t, s, "/dances/repair-plan/apply", url.Values{"confirm": {"on"}})
	if done.Code != http.StatusOK {
		t.Fatalf("confirmed apply: got %d body=%s", done.Code, done.Body)
	}
	fixed, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Parse(string(fixed))
	if err != nil {
		t.Fatalf("repaired PLAN.md must parse, got %v\n%s", err, fixed)
	}
	tk := p.Task("broken")
	if tk == nil {
		t.Fatal("task lost during repair")
	}
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatalf("dead claim not released: session=%q heartbeat=%v", tk.Session, tk.Heartbeat)
	}
	if tk.Attempts != 2 || tk.Weight != 8 || strings.Join(tk.Deps, ",") != "x,y" || tk.Status != plan.StatusDone {
		t.Fatalf("surviving metadata corrupted: %+v", tk)
	}
	if len(tk.Body) == 0 || tk.Body[0] != "body stays" {
		t.Fatalf("body corrupted: %v", tk.Body)
	}
}

// TestSkillPruneEmptySessionsConfirmGateAndApply proves the prune dance lists
// only zero-turn transcripts (naming the looping task), refuses to mutate without
// confirm, and on confirm removes exactly the empties while keeping a real
// transcript and a live streaming stub.
func TestSkillPruneEmptySessionsConfirmGateAndApply(t *testing.T) {
	s, root := setup(t)
	sessDir := filepath.Join(root, "submodules", "alpha", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Two zero-turn transcripts for one looping task (header, empty ## user body).
	empty := "# session %s\n\nsubmodule: alpha · kind: work · branch: bee-loop · model: m\n\n## user\n\n"
	emptyFiles := []string{"bee-loop-1783000000-1.md", "bee-loop-1783000005-2.md"}
	for _, n := range emptyFiles {
		if err := os.WriteFile(filepath.Join(sessDir, n), []byte(fmt.Sprintf(empty, n)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A header-ONLY transcript (no `## user` heading at all — the pass died before
	// the first turn marker; this is the bulk shape observed live) must also prune.
	headerOnly := "bee-loop-1783000008-9.md"
	if err := os.WriteFile(filepath.Join(sessDir, headerOnly), []byte(
		"# session "+headerOnly+"\n\nsubmodule: alpha · kind: work · branch: bee-loop · model: m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	emptyFiles = append(emptyFiles, headerOnly)
	// A real transcript (must be KEPT) and a live streaming stub (must be KEPT).
	real := "bee-real-1783000010-3.md"
	if err := os.WriteFile(filepath.Join(sessDir, real), []byte("# session r\n\n## user\ndo it\n## assistant\ndone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := "bee-live-1783000020-4.md"
	if err := os.WriteFile(filepath.Join(sessDir, stub), []byte(repo.SessionStub("alpha-1783000020-4-session")), 0o644); err != nil {
		t.Fatal(err)
	}
	// A non-transcript .md (no `# session ` header) must never be touched.
	other := "notes.md"
	if err := os.WriteFile(filepath.Join(sessDir, other), []byte("# random notes\n\nnot a session\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Orphaned-stub cases. Need a commit so branches can exist.
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "seed"}} {
		c := exec.Command("git", args...)
		c.Dir = root
		if err := c.Run(); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	// (i) An OLD stub whose branch is gone => dead => must be PRUNED.
	orphanStub := "bee-loop-1780000000-8.md"
	if err := os.WriteFile(filepath.Join(sessDir, orphanStub), []byte(repo.SessionStub("alpha-1780000000-8-session")), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(filepath.Join(sessDir, orphanStub), old, old)
	// (ii) An OLD stub whose branch STILL EXISTS => still streaming => must be KEPT.
	liveBranchStub := "bee-loop-1780000100-7.md"
	liveBranch := "alpha-1780000100-7-session"
	if err := os.WriteFile(filepath.Join(sessDir, liveBranchStub), []byte(repo.SessionStub(liveBranch)), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(filepath.Join(sessDir, liveBranchStub), old, old)
	bc := exec.Command("git", "branch", liveBranch)
	bc.Dir = root
	if err := bc.Run(); err != nil {
		t.Fatalf("git branch: %v", err)
	}
	exists := func(n string) bool {
		_, err := os.Stat(filepath.Join(sessDir, n))
		return err == nil
	}

	// (a) Dry-run: a remove action for alpha's sessions naming the count + the
	// looping task; mutates nothing.
	dry := postForm(t, s, "/dances/prune-empty-sessions/plan", url.Values{})
	if dry.Code != http.StatusOK {
		t.Fatalf("plan: got %d", dry.Code)
	}
	db := dry.Body.String()
	for _, want := range []string{"remove", "bee-loop", "symptom-only"} {
		if !strings.Contains(db, want) {
			t.Fatalf("dry-run missing %q:\n%s", want, db)
		}
	}
	for _, n := range emptyFiles {
		if !exists(n) {
			t.Fatalf("dry-run must not remove %q", n)
		}
	}

	// (b) Unconfirmed apply: refuses, mutates nothing.
	gate := postForm(t, s, "/dances/prune-empty-sessions/apply", url.Values{})
	if gate.Code != http.StatusOK {
		t.Fatalf("unconfirmed apply: got %d want 200", gate.Code)
	}
	if !strings.Contains(gate.Body.String(), "Confirmation required") {
		t.Fatalf("unconfirmed apply must ask to confirm:\n%s", gate.Body.String())
	}
	for _, n := range emptyFiles {
		if !exists(n) {
			t.Fatalf("unconfirmed apply must not remove %q", n)
		}
	}

	// (c) Confirmed apply: removes exactly the empties, keeps real + stub.
	done := postForm(t, s, "/dances/prune-empty-sessions/apply", url.Values{"confirm": {"on"}})
	if done.Code != http.StatusOK {
		t.Fatalf("confirmed apply: got %d body=%s", done.Code, done.Body)
	}
	if !strings.Contains(done.Body.String(), "applied") {
		t.Fatalf("confirmed apply must report applied:\n%s", done.Body.String())
	}
	for _, n := range emptyFiles {
		if exists(n) {
			t.Fatalf("confirmed apply must remove empty %q", n)
		}
	}
	if !exists(real) {
		t.Fatal("confirmed apply must KEEP the real transcript")
	}
	if !exists(stub) {
		t.Fatal("confirmed apply must KEEP the live streaming stub")
	}
	if !exists(other) {
		t.Fatal("confirmed apply must KEEP a non-transcript .md")
	}
	if exists(orphanStub) {
		t.Fatal("confirmed apply must PRUNE an old orphaned stub (branch gone)")
	}
	if !exists(liveBranchStub) {
		t.Fatal("confirmed apply must KEEP a stub whose session branch still exists")
	}
}

// TestWithPlanSyntaxHighlightsChatDiff is the regression test for
// chat-diff-syntax-highlight-bug: withPlan (the chat-diff panel view model) must
// feed precomputed syntax-highlighted per-line HTML into the rendered diff so a
// changed file previews with real language/token markup, not plain escaped text.
// Before the fix withPlan built editor.FileChange without OldHTML/NewHTML, so the
// rendered rows carried no highlight spans and this assertion fails.
func TestWithPlanSyntaxHighlightsChatDiff(t *testing.T) {
	before := "package main\n\nfunc main() {}\n"
	after := "package main\n\nfunc main() { println(\"hi\") }\n"
	plan := dancePlan{
		Diffs: []*danceDiff{{Path: "main.go", Before: before, After: after}},
	}
	panel := dancePanel{}.withPlan(plan)
	if len(panel.Diffs) != 1 {
		t.Fatalf("want 1 file diff box, got %d", len(panel.Diffs))
	}
	var joined strings.Builder
	for _, row := range panel.Diffs[0].Rows {
		joined.WriteString(string(row.HTML))
		joined.WriteString("\n")
	}
	out := joined.String()
	if !strings.Contains(out, `class="hl-kw"`) {
		t.Errorf("chat-diff rows are not syntax-highlighted (no keyword span); got:\n%s", out)
	}
	if !strings.Contains(out, `class="hl-str"`) {
		t.Errorf("chat-diff rows missing string highlight span; got:\n%s", out)
	}
}

// TestAPIDanceUnknownIs404 is the JSON mirror of TestSkillUnknownIs404: the
// /api/dances/{name}/plan and /apply routes 404 an unregistered name via a
// JSON {"error": "..."} body, never an HTML fragment.
func TestAPIDanceUnknownIs404(t *testing.T) {
	s, _ := setup(t)
	if w := postForm(t, s, "/api/dances/nope/plan", url.Values{}); w.Code != http.StatusNotFound {
		t.Fatalf("unknown plan: got %d want 404", w.Code)
	}
	if w := postForm(t, s, "/api/dances/nope/apply", url.Values{"confirm": {"on"}}); w.Code != http.StatusNotFound {
		t.Fatalf("unknown apply: got %d want 404", w.Code)
	}
}

// TestAPIDanceResourcesReportOnly is the JSON mirror of
// TestSkillResourcesReportOnly: plan returns the identity/flags plus the plan
// payload, and apply on a report-only dance is a 400 (never a mutation).
func TestAPIDanceResourcesReportOnly(t *testing.T) {
	s, _ := setup(t)
	w := postForm(t, s, "/api/dances/resources/plan", url.Values{})
	if w.Code != http.StatusOK {
		t.Fatalf("resources plan: got %d: %s", w.Code, w.Body)
	}
	var got struct {
		Name        string `json:"name"`
		Title       string `json:"title"`
		Destructive bool   `json:"destructive"`
		ReportOnly  bool   `json:"reportOnly"`
		Plan        struct {
			Empty bool `json:"empty"`
			Diffs []struct {
				Path   string `json:"path"`
				Before string `json:"before"`
				After  string `json:"after"`
			} `json:"diffs"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v: %s", err, w.Body)
	}
	if got.Name != "resources" || !got.ReportOnly {
		t.Fatalf("unexpected plan payload: %+v", got)
	}
	if w := postForm(t, s, "/api/dances/resources/apply", url.Values{"confirm": {"on"}}); w.Code != http.StatusBadRequest {
		t.Fatalf("report-only apply: got %d want 400", w.Code)
	}
}

// TestAPIDanceCleanupStaleConfirmGateAndApply is the JSON mirror of
// TestSkillCleanupStaleConfirmGateAndApply: an unconfirmed destructive apply
// reports confirmRequired without mutating, and a confirmed apply performs
// precisely the proposed removals.
func TestAPIDanceCleanupStaleConfirmGateAndApply(t *testing.T) {
	s, root := setup(t)
	stale, keep := seedStaleWorktrees(t, root)
	exists := func(n string) bool {
		_, err := os.Stat(filepath.Join(root, ".worktrees", n))
		return err == nil
	}

	// Unconfirmed apply: no mutation, reports confirmRequired.
	w := postForm(t, s, "/api/dances/cleanup-stale/apply", url.Values{})
	if w.Code != http.StatusOK {
		t.Fatalf("unconfirmed apply: got %d: %s", w.Code, w.Body)
	}
	var unconfirmed struct {
		ConfirmRequired bool `json:"confirmRequired"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &unconfirmed); err != nil {
		t.Fatalf("decode: %v: %s", err, w.Body)
	}
	if !unconfirmed.ConfirmRequired {
		t.Fatalf("unconfirmed apply must report confirmRequired: %s", w.Body)
	}
	for _, n := range stale {
		if !exists(n) {
			t.Fatalf("unconfirmed apply mutated: %s removed", n)
		}
	}

	// Confirmed apply: removes exactly the stale dirs, spares the kept one.
	w = postForm(t, s, "/api/dances/cleanup-stale/apply", url.Values{"confirm": {"on"}})
	if w.Code != http.StatusOK {
		t.Fatalf("confirmed apply: got %d: %s", w.Code, w.Body)
	}
	for _, n := range stale {
		if exists(n) {
			t.Fatalf("confirmed apply left stale dir: %s", n)
		}
	}
	if !exists(keep) {
		t.Fatalf("confirmed apply removed the kept dir: %s", keep)
	}
}

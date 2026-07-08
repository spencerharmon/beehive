package web

import (
	"context"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/editor"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/instruct"
	"github.com/spencerharmon/beehive/internal/links"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spencerharmon/beehive/prompts"
)

func setup(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	if err := repo.Init(root); err != nil {
		t.Fatal(err)
	}
	g := exec.Command("git", "init")
	g.Dir = root
	if err := g.Run(); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][]string{{"user.email", "t@t"}, {"user.name", "t"}} {
		c := exec.Command("git", "config", kv[0], kv[1])
		c.Dir = root
		c.Run()
	}
	sm := filepath.Join(root, "submodules", "alpha")
	os.MkdirAll(sm, 0o755)
	os.WriteFile(filepath.Join(sm, repo.ROIFile), []byte("# alpha\n"), 0o644)
	// Real H2-header PLAN.md format (internal/plan), NOT the legacy bullet form:
	// `## <id> [STATUS] <!-- attempts=N deps=a,b weight=W session=<id>
	// heartbeat=<RFC3339> -->` with a free-form body (Desc = first line, Doc =
	// the "Doc:" line). t1 carries a session+heartbeat claim.
	os.WriteFile(filepath.Join(sm, repo.PlanFile), []byte(
		"<!-- Beehive-ROI: abc123 -->\n# Plan\n\n"+
			"## t1 [TODO] <!-- attempts=0 deps=t0 weight=16 session=bee-1 heartbeat=2026-06-30T11:00:00Z -->\n"+
			"build the thing\nFiles: a.go\nDoc: br-t1.md\nAccept: works\n\n"+
			"## t2 [NEEDS-HUMAN] <!-- attempts=4 deps= -->\nstuck task\nDoc: br-t2.md\n\n"+
			"## t3 [DONE] <!-- attempts=0 deps= -->\nok done\nDoc: br-t3.md\n"), 0o644)
	r, _ := repo.Open(root)
	s, err := New(r, config.Defaults(root))
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func get(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

// renderTmpl executes a named template to a string. The polled fragments
// (session_body / session_list / editor_panel) need live session/editor data to
// reach through a handler, so their scroll-preserve wiring is asserted directly
// off the parsed template set (white-box) instead of standing up a fake session.
func renderTmpl(t *testing.T, s *Server, name string, data interface{}) string {
	t.Helper()
	var b strings.Builder
	if err := s.tmpl.ExecuteTemplate(&b, name, data); err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return b.String()
}

// TestScrollPreserveScriptEmbedded locks that the save/restore script ships on a
// real full-page response (the layout footer), with no external lib reference.
// It is what keeps every polled pane from yanking the reader to the top.
func TestScrollPreserveScriptEmbedded(t *testing.T) {
	s, _ := setup(t)
	page := get(t, s, "/").Body.String() // dashboard renders the layout header+footer
	for _, want := range []string{
		"htmx:beforeSwap", "htmx:afterSwap", // save before, restore after a swap
		"data-scroll-preserve", // the pane contract the script keys off
		"data-scroll-pin",      // bottom-pin (follow live output only when pinned)
		"scrollTop",            // per-pane restore
		"window.scrollTo",      // window-scroll restore (covers page-scrolled lists)
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("embedded scroll-restore script missing %q", want)
		}
	}
}

// TestScrollPreserveWiring locks the per-fragment contract: every polled scroll
// region carries a STABLE pane id + data-scroll-preserve, and the live-growing
// panes (session transcript, editor chat) additionally pin to bottom. DOM
// scrollTop itself is not unit-testable in Go; the manual scroll check is in the
// change doc. A rename here must break this test on purpose.
func TestScrollPreserveWiring(t *testing.T) {
	s, _ := setup(t)

	// Session transcript: stable id, preserve, and pin (it grows at the bottom).
	body := renderTmpl(t, s, "session_body.html", map[string]interface{}{"Body": "turn\n"})
	for _, want := range []string{`id="session-transcript"`, "data-scroll-preserve", "data-scroll-pin"} {
		if !strings.Contains(body, want) {
			t.Fatalf("session_body.html missing %q:\n%s", want, body)
		}
	}

	// Auto-refreshing session list: preserve wiring on the polled node.
	list := renderTmpl(t, s, "session_list.html", map[string]interface{}{"Name": "alpha"})
	for _, want := range []string{`id="session-list"`, "data-scroll-preserve"} {
		if !strings.Contains(list, want) {
			t.Fatalf("session_list.html missing %q:\n%s", want, list)
		}
	}

	// Editor chat+diff: chat pins to bottom, diff just holds position.
	panel := renderTmpl(t, s, "editor_panel.html", map[string]interface{}{"ID": "e1", "File": "ROI.md"})
	for _, want := range []string{`id="editor-chat"`, `id="editor-diff"`, "data-scroll-preserve", "data-scroll-pin"} {
		if !strings.Contains(panel, want) {
			t.Fatalf("editor_panel.html missing %q:\n%s", want, panel)
		}
	}

	// The transcript poll target shell is present (the script restores into it).
	view := renderTmpl(t, s, "session_view.html", map[string]interface{}{"Name": "alpha", "Branch": "bee-x"})
	if !strings.Contains(view, `id="session-body"`) {
		t.Fatalf("session_view.html missing #session-body poll target:\n%s", view)
	}
}

func TestDashboard(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "alpha") {
		t.Fatalf("dashboard %d: %s", w.Code, w.Body)
	}
}

// TestDashboardCards is the core of dashboard-cards: subViews derives one card
// per submodule with the correct swarm State (active/dormant/bootstrap), the
// active blue/green Env from the submodule's own INFRASTRUCTURE.md (via the typed
// artifacts model), the Pending/Human counts from the unified parser (a
// NEEDS-HUMAN task counts in BOTH, a DONE task in neither), and Working from a
// fresh session+heartbeat claim. now is fixed so Working is deterministic. It
// also renders the card grid and asserts the badges/links are wired.
func TestDashboardCards(t *testing.T) {
	s, root := setup(t)
	// alpha (from setup: t1 TODO+claim, t2 NEEDS-HUMAN, t3 DONE) gets its own
	// INFRASTRUCTURE.md declaring green active -> an env badge on its card.
	if err := os.WriteFile(filepath.Join(root, "submodules", "alpha", repo.InfraFile),
		[]byte("# infra\nActive: green\nEnvironments: blue, green\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// bravo: ROI present, PLAN absent -> bootstrap. No INFRASTRUCTURE.md -> no env.
	bravo := filepath.Join(root, "submodules", "bravo")
	if err := os.MkdirAll(bravo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bravo, repo.ROIFile), []byte("# bravo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// charlie: no ROI -> dormant.
	if err := os.MkdirAll(filepath.Join(root, "submodules", "charlie"), 0o755); err != nil {
		t.Fatal(err)
	}

	// t1's heartbeat is 2026-06-30T11:00:00Z; 30m before now with a 60m TTL => a
	// fresh claim, so alpha is Working.
	now := time.Date(2026, 6, 30, 11, 30, 0, 0, time.UTC)
	views, err := s.subViews(context.Background(), now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]subView{}
	for _, v := range views {
		by[v.Name] = v
	}

	a, ok := by["alpha"]
	if !ok {
		t.Fatalf("no alpha card in %+v", views)
	}
	if a.State != "active" {
		t.Errorf("alpha State = %q, want active", a.State)
	}
	if a.Env != "green" {
		t.Errorf("alpha Env = %q, want green (from INFRASTRUCTURE.md)", a.Env)
	}
	if a.EnvClass() != "env-green" {
		t.Errorf("alpha EnvClass = %q, want env-green", a.EnvClass())
	}
	if a.Pending != 2 {
		t.Errorf("alpha Pending = %d, want 2 (t1 TODO + t2 NEEDS-HUMAN; DONE t3 excluded)", a.Pending)
	}
	if a.Human != 1 {
		t.Errorf("alpha Human = %d, want 1 (t2 NEEDS-HUMAN only)", a.Human)
	}
	if !a.Working {
		t.Errorf("alpha Working = false, want true (t1 claim fresh at now)")
	}
	// Bees is the COUNT of fresh claims (honeybees on the card): alpha has exactly
	// one (t1); it must equal Working > 0.
	if a.Bees != 1 {
		t.Errorf("alpha Bees = %d, want 1 (t1 claim fresh; t2/t3 unclaimed)", a.Bees)
	}
	// Stamp rides the same cached PLAN.md parse (p.ROIStamp), not a second
	// ROIStamp() disk read: alpha's PLAN.md carries `Beehive-ROI: abc123`.
	if a.Stamp != "abc123" {
		t.Errorf("alpha Stamp = %q, want abc123 (from the cached plan parse)", a.Stamp)
	}

	if got := by["bravo"].State; got != "bootstrap" {
		t.Errorf("bravo State = %q, want bootstrap", got)
	}
	if got := by["bravo"].Env; got != "" {
		t.Errorf("bravo Env = %q, want empty (no INFRASTRUCTURE.md)", got)
	}
	if got := by["bravo"].Pending; got != 0 {
		t.Errorf("bravo Pending = %d, want 0 (no PLAN)", got)
	}
	if got := by["bravo"].Bees; got != 0 {
		t.Errorf("bravo Bees = %d, want 0 (no PLAN, no claims)", got)
	}
	if got := by["charlie"].State; got != "dormant" {
		t.Errorf("charlie State = %q, want dormant", got)
	}

	// A claim well past the TTL must NOT read as Working (the card's live overlay
	// is derived from claim freshness, not a status).
	stale, err := s.subViews(context.Background(), now.Add(48*time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range stale {
		if v.Name == "alpha" && v.Working {
			t.Errorf("alpha Working = true at now+48h, want false (claim stale past TTL)")
		}
		if v.Name == "alpha" && v.Bees != 0 {
			t.Errorf("alpha Bees = %d at now+48h, want 0 (claim stale past TTL)", v.Bees)
		}
	}

	// Rendered card grid: the env badge, the NEEDS-HUMAN count linking /human, the
	// swarm-state badges, the pending count, and the honeybee count are all present
	// in the HTML. This body is rendered at real time.Now(), where the fixture's
	// 2026-06-30 claim is long stale, so every card reads "🐝 0" (the badge renders
	// its count regardless of liveness; the live count is asserted off subViews with
	// the fixed now above, and the lit-bee markup in TestDashboardCardPolish).
	body := get(t, s, "/").Body.String()
	for _, want := range []string{
		"card-meta", "green", "needs-human 1", `href="/human"`,
		"bootstrap", "dormant", "pending 2", "🐝 0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard HTML missing %q:\n%s", want, body)
		}
	}
}

// TestDashboardCardPolish locks the dashboard-card-polish refinements on top of
// dashboard-cards: every card shows a 🐝 honeybee count (the live claim count,
// teal-lit when bees are working); the commit/branch-graph link reads "commits",
// not "branches"; the ROI links are ONE labelled view/edit pair per card (no
// duplicate roi links); and the ROI stamp is rendered in a truncating cell whose
// full value stays reachable via the code's title (so a long sha never overflows
// the card). It reuses the setup fixture (alpha, stamp abc123) and drives the
// idle vs live-bee states via the on-disk claim heartbeat (real time.Now()).
func TestDashboardCardPolish(t *testing.T) {
	s, root := setup(t)

	// IDLE: the fixture's t1 heartbeat (2026-06-30) is long stale at real now, so
	// alpha has no live bee. The 🐝 count still renders (as 0) and must NOT carry
	// the lit "bees-live" modifier.
	idle := get(t, s, "/").Body.String()
	if !strings.Contains(idle, "🐝 0") {
		t.Errorf("idle dashboard missing the 🐝 honeybee count (should render 0 for a stale claim):\n%s", idle)
	}
	if strings.Contains(idle, "bees-live") {
		t.Errorf("idle dashboard lit the bee badge with no fresh claim:\n%s", idle)
	}

	// The commit/branch-graph link reads "commits" (the branch view titles itself
	// "<name> commits"), and the old "branches" label is gone.
	if !strings.Contains(idle, `/submodule/alpha/branches">commits</a>`) {
		t.Errorf("dashboard commit link does not read \"commits\":\n%s", idle)
	}
	if strings.Contains(idle, ">branches</a>") {
		t.Errorf("dashboard still shows the old \"branches\" link label:\n%s", idle)
	}

	// Exactly ONE ROI view/edit link pair per card: one /roi/alpha view link and
	// one ROI.md edit link, consolidated in a .roi-links span, with no leftover
	// duplicate "edit roi (AI)" link.
	if n := strings.Count(idle, `href="/roi/alpha">view</a>`); n != 1 {
		t.Errorf("alpha ROI view link count = %d, want exactly 1:\n%s", n, idle)
	}
	if n := strings.Count(idle, `href="/edit?path=submodules/alpha/ROI.md">edit</a>`); n != 1 {
		t.Errorf("alpha ROI edit link count = %d, want exactly 1:\n%s", n, idle)
	}
	if strings.Contains(idle, "edit roi (AI)") {
		t.Errorf("dashboard still shows the old duplicate \"edit roi (AI)\" link:\n%s", idle)
	}
	if !strings.Contains(idle, `class="roi-links"`) {
		t.Errorf("dashboard ROI links are not consolidated into a single roi-links pair:\n%s", idle)
	}

	// ROI stamp overflow fix: the stamp renders in the truncating .card-stamp cell
	// with its full value carried on the code's title (hover), so a long sha never
	// overflows the card body. alpha's stamp is abc123.
	if !strings.Contains(idle, "card-stamp") {
		t.Errorf("dashboard stamp is not in the truncating card-stamp cell:\n%s", idle)
	}
	if !strings.Contains(idle, `title="abc123"`) {
		t.Errorf("dashboard stamp does not expose its full value via the code title (hover):\n%s", idle)
	}

	// LIVE: rewrite alpha's PLAN.md so t1 carries a heartbeat fresh at real now
	// (well within the 60m TTL). alpha now has exactly one live bee, so its card
	// shows "🐝 1" with the teal "bees-live" modifier. The no-commit fixture has an
	// empty HEAD, so planView bypasses the cache and re-reads this on next render.
	fresh := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(root, "submodules", "alpha", repo.PlanFile), []byte(
		"<!-- Beehive-ROI: abc123 -->\n# Plan\n\n"+
			"## t1 [TODO] <!-- attempts=0 deps=t0 weight=16 session=bee-1 heartbeat="+fresh+" -->\n"+
			"build the thing\nFiles: a.go\nDoc: br-t1.md\nAccept: works\n\n"+
			"## t2 [NEEDS-HUMAN] <!-- attempts=4 deps= -->\nstuck task\nDoc: br-t2.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	live := get(t, s, "/").Body.String()
	if !strings.Contains(live, "🐝 1") {
		t.Errorf("live dashboard missing the 🐝 1 honeybee count for the working alpha card:\n%s", live)
	}
	if !strings.Contains(live, "badge bees bees-live") {
		t.Errorf("live dashboard missing the teal bees-live modifier on the working alpha card:\n%s", live)
	}
}

func TestPlanAndHuman(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/submodule/alpha/plan")
	b := w.Body.String()
	// Real H2-header plan: ROI stamp, ids, the derived Doc, the deps cell, and the
	// derived claim label must all surface (none of which the old bullet parser,
	// fed a real plan, produced — it parsed zero rows).
	for _, want := range []string{"abc123", "t1", "TODO", "NEEDS-HUMAN", "DONE", "br-t1.md", "t0"} {
		if !strings.Contains(b, want) {
			t.Fatalf("plan view missing %q:\n%s", want, b)
		}
	}
	h := get(t, s, "/human")
	if !strings.Contains(h.Body.String(), "t2") || strings.Contains(h.Body.String(), "t1<") {
		t.Fatalf("human: %s", h.Body)
	}
}

// TestHumanViewRendersStructuredReasonMarkdown is the /human half of
// plan-view-detail-polish: a NEEDS-HUMAN task's reason — a one-line summary
// plus bullets naming the concrete blocker/needed input, the structure
// HONEYBEE.md's escalation guidance now asks agents to write — renders as
// real markdown markup (a list), not escaped raw text carrying literal "- "
// characters or a raw "Human-needed:" prefix.
func TestHumanViewRendersStructuredReasonMarkdown(t *testing.T) {
	s, root := setup(t)
	sm := filepath.Join(root, "submodules", "alpha")
	if err := os.WriteFile(filepath.Join(sm, repo.PlanFile), []byte(
		"<!-- Beehive-ROI: abc123 -->\n# Plan\n\n"+
			"## stuck [NEEDS-HUMAN] <!-- attempts=4 deps= -->\nblocked\n"+
			"Human-needed: Missing credentials for the deploy API.\n"+
			"- Blocker: cannot authenticate to the release service\n"+
			"- Needed: a fresh API token for that service\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	w := get(t, s, "/human")
	if w.Code != 200 {
		t.Fatalf("human %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, want := range []string{
		"Missing credentials for the deploy API.",
		"<li>Blocker: cannot authenticate to the release service</li>",
		"<li>Needed: a fresh API token for that service</li>",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered reason missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Human-needed:") {
		t.Fatalf("raw Human-needed: prefix leaked into the rendered view:\n%s", body)
	}
	if strings.Contains(body, "- Blocker:") {
		t.Fatalf("bullet rendered as raw dash text instead of a real list:\n%s", body)
	}
}

// TestParsePlanRealFormat is the core of web-plan-parser-unify: the web parser is
// now a thin adapter over internal/plan, so a REAL H2-header PLAN.md (the format
// the runner actually writes, with session/heartbeat claim metadata) parses —
// where the old bullet parser yielded zero items. It asserts task count,
// statuses, deps, heartbeat, the derived Doc, and the NEEDS-HUMAN/pending counts,
// and that active vs stale is derived from session+heartbeat freshness vs the TTL
// (there is no IN-PROGRESS status). now is fixed so active/stale is deterministic.
func TestParsePlanRealFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "PLAN.md")
	// t1: claimed, heartbeat 30m before now + ttl 60m => ACTIVE.
	// t2: claimed, heartbeat 3h before now => STALE (past TTL).
	// t3: NEEDS-HUMAN, unclaimed. t4: DONE, unclaimed.
	src := "<!-- Beehive-ROI: deadbeef -->\n# Plan\n\n" +
		"## t1 [TODO] <!-- attempts=0 deps=t0,t9 weight=16 session=bee-A heartbeat=2026-06-30T11:30:00Z -->\n" +
		"implement t1\nFiles: a.go\nDoc: docs/tasks/t1.md\n\n" +
		"## t2 [NEEDS-REVIEW] <!-- attempts=1 deps= session=bee-B heartbeat=2026-06-30T09:00:00Z -->\n" +
		"review me\nDoc: docs/tasks/t2.md\n\n" +
		"## t3 [NEEDS-HUMAN] <!-- attempts=4 deps= -->\nstuck\nHuman-needed: Need operator decision\n\n" +
		"## t4 [DONE] <!-- attempts=0 deps= -->\ndone\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	now := mustTime(t, "2026-06-30T12:00:00Z")
	ttl := time.Hour

	p, err := parsePlan(path, now, ttl)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.ROIStamp != "deadbeef" {
		t.Fatalf("ROIStamp = %q, want deadbeef", p.ROIStamp)
	}
	if len(p.Items) != 4 {
		t.Fatalf("got %d items, want 4: %+v", len(p.Items), p.Items)
	}
	byID := map[string]PlanItem{}
	for _, it := range p.Items {
		byID[it.ID] = it
	}

	t1 := byID["t1"]
	if t1.Status != StatusTODO {
		t.Fatalf("t1 status = %q, want TODO", t1.Status)
	}
	if len(t1.Deps) != 2 || t1.Deps[0] != "t0" || t1.Deps[1] != "t9" {
		t.Fatalf("t1 deps = %v, want [t0 t9]", t1.Deps)
	}
	if t1.Desc != "implement t1" {
		t.Fatalf("t1 desc = %q, want 'implement t1'", t1.Desc)
	}
	if t1.Doc != "docs/tasks/t1.md" {
		t.Fatalf("t1 doc = %q", t1.Doc)
	}
	if !t1.Heartbeat.Equal(mustTime(t, "2026-06-30T11:30:00Z")) {
		t.Fatalf("t1 heartbeat = %v", t1.Heartbeat)
	}
	// Active vs stale is the session+heartbeat freshness, NOT a status.
	if !t1.Active || t1.Stale {
		t.Fatalf("t1 should be ACTIVE (fresh claim): active=%v stale=%v", t1.Active, t1.Stale)
	}
	if got := t1.Claim(); got != "active bee-A" {
		t.Fatalf("t1 claim = %q, want 'active bee-A'", got)
	}

	t2 := byID["t2"]
	if t2.Status != StatusReview {
		t.Fatalf("t2 status = %q, want NEEDS-REVIEW", t2.Status)
	}
	if t2.Active || !t2.Stale {
		t.Fatalf("t2 should be STALE (expired claim): active=%v stale=%v", t2.Active, t2.Stale)
	}
	if got := t2.Claim(); got != "stale bee-B" {
		t.Fatalf("t2 claim = %q, want 'stale bee-B'", got)
	}

	// t3 NEEDS-HUMAN, t4 DONE: both unclaimed => neither active nor stale.
	for _, id := range []string{"t3", "t4"} {
		if it := byID[id]; it.Active || it.Stale || it.Claim() != "" {
			t.Fatalf("%s unclaimed but active=%v stale=%v claim=%q", id, it.Active, it.Stale, it.Claim())
		}
	}
	if got := byID["t3"].HumanReason; got != "Need operator decision" {
		t.Fatalf("t3 human reason = %q", got)
	}

	// Count semantics the views use: pending = not DONE; human = NEEDS-HUMAN.
	pending, human := 0, 0
	for _, it := range p.Items {
		if it.Status != StatusDone {
			pending++
		}
		if it.Status == StatusHuman {
			human++
		}
	}
	if pending != 3 {
		t.Fatalf("pending = %d, want 3 (t1,t2,t3)", pending)
	}
	if human != 1 {
		t.Fatalf("needs-human = %d, want 1 (t3)", human)
	}
}

// TestParsePlanMissingFile: an absent PLAN.md is an empty plan, not an error
// (a freshly-added, pre-bootstrap submodule).
func TestParsePlanMissingFile(t *testing.T) {
	p, err := parsePlan(filepath.Join(t.TempDir(), "nope.md"), time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("missing file should be empty plan, got err %v", err)
	}
	if len(p.Items) != 0 || p.ROIStamp != "" {
		t.Fatalf("missing file should yield empty plan, got %+v", p)
	}
}

// TestPlanViewMissingFile confirms the cached path matches the uncached baseline
// for the empty-plan case: a not-yet-bootstrapped submodule (no PLAN.md) is an
// empty plan, not an error, and — since a missing read is not cached as an error
// — it still resolves cleanly.
func TestPlanViewMissingFile(t *testing.T) {
	s, root := setup(t)
	commitAll(t, root, "init")
	head := s.headSHA(context.Background())
	p, err := s.planView(head, filepath.Join(root, "submodules", "alpha", "nope.md"), time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("missing file should be empty plan, got err %v", err)
	}
	if len(p.Items) != 0 || p.ROIStamp != "" {
		t.Fatalf("missing file should yield empty plan, got %+v", p)
	}
}

// TestPlanViewCacheHitAndCommitInvalidation is the core of the parse-once cache:
// repeated reads at one repo HEAD parse PLAN.md exactly once (a cache hit adds no
// miss), and a commit — which advances HEAD — drops the generation so the next
// read re-parses. Keying invalidation on HEAD is exactly the design's trigger:
// every claim, heartbeat, status flip, and merge is a commit, so any of them
// advances HEAD and conservatively wipes the cache.
func TestPlanViewCacheHitAndCommitInvalidation(t *testing.T) {
	s, root := setup(t)
	commitAll(t, root, "init") // give the repo a HEAD so the cache engages
	path := filepath.Join(root, "submodules", "alpha", repo.PlanFile)
	now := time.Date(2026, 6, 30, 11, 30, 0, 0, time.UTC)
	ttl := time.Hour
	head1 := s.headSHA(context.Background())
	if head1 == "" {
		t.Fatal("no HEAD after seed commit")
	}

	p1, err := s.planView(head1, path, now, ttl)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.cache.Misses(); got != 1 {
		t.Fatalf("first read misses = %d, want 1", got)
	}
	// Second read at the same HEAD is served from cache: no additional parse.
	p2, err := s.planView(head1, path, now, ttl)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.cache.Misses(); got != 1 {
		t.Fatalf("cached read misses = %d, want 1 (parse-once per HEAD)", got)
	}
	if !reflect.DeepEqual(p1, p2) {
		t.Fatalf("cached view differs from first read:\n %+v\nvs %+v", p1, p2)
	}
	// A commit advances HEAD -> the next read must re-parse.
	commitAll(t, root, "advance")
	head2 := s.headSHA(context.Background())
	if head2 == head1 {
		t.Fatalf("HEAD did not advance after commit (%q)", head2)
	}
	if _, err := s.planView(head2, path, now, ttl); err != nil {
		t.Fatal(err)
	}
	if got := s.cache.Misses(); got != 2 {
		t.Fatalf("post-commit misses = %d, want 2 (HEAD change invalidates)", got)
	}
}

// TestPlanViewMatchesParsePlan proves the cache changes only WHEN the read+parse
// runs, never WHAT the view contains: planView equals the uncached parsePlan
// baseline for the same HEAD/now/ttl.
func TestPlanViewMatchesParsePlan(t *testing.T) {
	s, root := setup(t)
	commitAll(t, root, "init")
	path := filepath.Join(root, "submodules", "alpha", repo.PlanFile)
	now := time.Date(2026, 6, 30, 11, 30, 0, 0, time.UTC)
	ttl := time.Hour

	want, err := parsePlan(path, now, ttl)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.planView(s.headSHA(context.Background()), path, now, ttl)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("planView != parsePlan:\n got %+v\nwant %+v", got, want)
	}
}

// TestPlanViewReprojectsClaimWithoutCommit guards WHAT is cached: only the
// time-independent read+parse. A claim's active->stale flip turns purely on the
// wall clock crossing the TTL with NO new commit (a crashed owner stops
// committing, so HEAD never advances). The view must still flip while the parse
// stays served from cache — i.e. the projection is recomputed every call, never
// memoized.
func TestPlanViewReprojectsClaimWithoutCommit(t *testing.T) {
	s, root := setup(t)
	commitAll(t, root, "init")
	path := filepath.Join(root, "submodules", "alpha", repo.PlanFile)
	ttl := time.Hour
	head := s.headSHA(context.Background())

	// t1's heartbeat is 2026-06-30T11:00:00Z; 30m later within a 60m ttl => active.
	fresh := time.Date(2026, 6, 30, 11, 30, 0, 0, time.UTC)
	p, err := s.planView(head, path, fresh, ttl)
	if err != nil {
		t.Fatal(err)
	}
	if t1 := itemByID(p, "t1"); !t1.Active || t1.Stale {
		t.Fatalf("t1 want active & !stale at fresh time, got %+v", t1)
	}
	if got := s.cache.Misses(); got != 1 {
		t.Fatalf("misses = %d, want 1", got)
	}
	// Same HEAD (no commit), now well past the ttl: the parse is served from cache
	// yet the claim must read stale / not-active.
	expired := fresh.Add(48 * time.Hour)
	p2, err := s.planView(head, path, expired, ttl)
	if err != nil {
		t.Fatal(err)
	}
	if t1 := itemByID(p2, "t1"); t1.Active || !t1.Stale {
		t.Fatalf("t1 want !active & stale past ttl, got %+v", t1)
	}
	if got := s.cache.Misses(); got != 1 {
		t.Fatalf("misses = %d, want 1 (parse cached; projection recomputed live)", got)
	}
}

// itemByID returns the plan item with id (or a zero item), a small test helper
// for asserting on one task's projected claim state.
func itemByID(p Plan, id string) PlanItem {
	for _, it := range p.Items {
		if it.ID == id {
			return it
		}
	}
	return PlanItem{}
}

// commitAll writes a uniquely-named marker file under dir then stages and commits
// everything, guaranteeing a real commit that advances the repo HEAD — the
// viewCache generation key. Used by the cache tests to drive invalidation.
func commitAll(t *testing.T, dir, marker string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "cachemarker-"+marker), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, a := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", marker}} {
		c := exec.Command("git", a...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", a, err, out)
		}
	}
}

// the design-system status pill class (base shape + lower-cased slug hue), the
// claim state derived from session+heartbeat freshness (NOT a status), and the
// compact heartbeat label (empty when unclaimed).
func TestPlanItemViewHelpers(t *testing.T) {
	for status, want := range map[string]string{
		StatusTODO:   "status status-todo",
		StatusReview: "status status-needs-review",
		StatusArb:    "status status-needs-arbitration",
		StatusDone:   "status status-done",
		StatusHuman:  "status status-needs-human",
	} {
		if got := (PlanItem{Status: status}).StatusClass(); got != want {
			t.Fatalf("StatusClass(%q) = %q, want %q", status, got, want)
		}
	}
	hb := mustTime(t, "2026-06-30T11:30:00Z")
	active := PlanItem{Active: true, Session: "bee-A", Heartbeat: hb}
	if active.ClaimState() != "active" || active.HeartbeatLabel() != "2026-06-30 11:30Z" {
		t.Fatalf("active: state=%q label=%q", active.ClaimState(), active.HeartbeatLabel())
	}
	stale := PlanItem{Stale: true, Session: "bee-B", Heartbeat: hb}
	if stale.ClaimState() != "stale" {
		t.Fatalf("stale state = %q, want stale", stale.ClaimState())
	}
	if unc := (PlanItem{}); unc.ClaimState() != "" || unc.HeartbeatLabel() != "" {
		t.Fatalf("unclaimed: state=%q label=%q, want empty", unc.ClaimState(), unc.HeartbeatLabel())
	}
}

// TestResolveDeps proves the dependency indicator: each dep is marked
// satisfied/pending against the plan's own DONE set, and a dep id absent from
// the plan (e.g. a cross-submodule reference) stays unsatisfied.
func TestResolveDeps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "PLAN.md")
	src := "<!-- Beehive-ROI: x -->\n# Plan\n\n" +
		"## a [DONE] <!-- attempts=0 deps= -->\ndone dep\n\n" +
		"## b [TODO] <!-- attempts=0 deps=a,missing -->\nneeds a (done) and missing (absent)\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := parsePlan(path, time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var b PlanItem
	for _, it := range p.Items {
		if it.ID == "b" {
			b = it
		}
	}
	if len(b.DepStates) != 2 {
		t.Fatalf("b deps = %+v, want 2 (a, missing)", b.DepStates)
	}
	if b.DepStates[0].Name != "a" || !b.DepStates[0].Done {
		t.Fatalf("dep a should be satisfied (DONE): %+v", b.DepStates[0])
	}
	if b.DepStates[1].Name != "missing" || b.DepStates[1].Done {
		t.Fatalf("dep missing should be unsatisfied (absent): %+v", b.DepStates[1])
	}
}

// TestPlanViewPills locks the rendered plan view (plan-view-pills): a status pill
// per status (design-system .status-<slug>), the .active overlay on a fresh
// session+heartbeat claim (and NOT on a stale one — there is no IN-PROGRESS
// status), satisfied vs pending dependency chips, the claim label + heartbeat,
// and a resolved change-doc link. White-box render so active/stale is
// deterministic (no wall clock).
func TestPlanViewPills(t *testing.T) {
	s, _ := setup(t)
	hb := mustTime(t, "2026-06-30T11:30:00Z")
	pl := Plan{ROIStamp: "abc123", Items: []PlanItem{
		{
			ID: "imp", Status: StatusTODO, Desc: "implement it",
			DepStates: []Dep{{Name: "dep-done", Done: true}, {Name: "dep-todo", Done: false}},
			Session:   "bee-A", Heartbeat: hb, Active: true,
			DocHref: "/submodule/alpha/doc/bee-imp.md",
		},
		{ID: "rev", Status: StatusReview, Desc: "review me", Session: "bee-B", Heartbeat: hb, Stale: true},
		{ID: "fin", Status: StatusDone, Desc: "shipped", Doc: "docs/tasks/fin.md"},
		{ID: "arb", Status: StatusArb, Desc: "contested"},
		{ID: "hum", Status: StatusHuman, Desc: "stuck"},
	}}
	out := renderTmpl(t, s, "plan_items.html", map[string]interface{}{"Name": "alpha", "Plan": pl})

	// (1) a status pill per status (slug = lower-cased status).
	for _, want := range []string{
		"status status-todo", "status status-needs-review", "status status-done",
		"status status-needs-arbitration", "status status-needs-human",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing status pill %q:\n%s", want, out)
		}
	}
	// (2) the active overlay rides the FRESH-claim pill, not the stale one.
	if !strings.Contains(out, `class="status status-todo active">TODO`) {
		t.Fatalf("active claim did not get the .active pill overlay:\n%s", out)
	}
	if strings.Contains(out, "status-needs-review active") {
		t.Fatalf("stale claim must NOT get the .active overlay:\n%s", out)
	}
	// (3) dependency chips: satisfied (DONE) is a .live badge, pending is plain.
	if !strings.Contains(out, `class="badge live" title="satisfied (DONE)">dep-done`) {
		t.Fatalf("satisfied dep chip missing:\n%s", out)
	}
	if !strings.Contains(out, `class="badge" title="pending">dep-todo`) {
		t.Fatalf("pending dep chip missing:\n%s", out)
	}
	// (4) claim label + session + heartbeat freshness for active and stale.
	for _, want := range []string{
		`class="badge live">active`, "bee-A", "2026-06-30 11:30Z",
		`class="badge">stale`, "bee-B",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("claim rendering missing %q:\n%s", want, out)
		}
	}
	// (5) the change-doc link resolves; a task with only a design Doc and no
	// resolved change doc falls back to muted text (no dead link).
	if !strings.Contains(out, `<a href="/submodule/alpha/doc/bee-imp.md">change doc</a>`) {
		t.Fatalf("change-doc link missing:\n%s", out)
	}
	if !strings.Contains(out, `<span class="muted">docs/tasks/fin.md</span>`) {
		t.Fatalf("design-doc fallback text missing:\n%s", out)
	}
}

// TestPlanChangeDocLink drives the change-doc linkage end-to-end through the
// handler: it scans the submodule repo's Beehive stamps and links a task to the
// change doc its implementing commit recorded when that doc exists under the
// submodule's docs/, and never links a stamp whose doc is absent.
func TestPlanChangeDocLink(t *testing.T) {
	s, root := setup(t)
	docsDir := filepath.Join(root, "submodules", "alpha", "docs")
	os.MkdirAll(docsDir, 0o755)
	os.WriteFile(filepath.Join(docsDir, "bee-t1.md"), []byte("# t1 change doc\n"), 0o644)
	// t1's implementing commit stamps an existing doc; t2's stamps a missing one.
	commitRepoAt(t, filepath.Join(root, "submodules", "alpha", "repo"),
		"impl t1\n\nBeehive: t1 docs/bee-t1.md",
		"impl t2\n\nBeehive: t2 docs/bee-missing.md")

	w := get(t, s, "/submodule/alpha/plan")
	if w.Code != 200 {
		t.Fatalf("plan %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, `href="/submodule/alpha/doc/bee-t1.md"`) {
		t.Fatalf("t1 change-doc link missing:\n%s", body)
	}
	if strings.Contains(body, "/doc/bee-missing.md") {
		t.Fatalf("absent change doc must not be linked:\n%s", body)
	}
	// Pills still render (status-driven, time-independent).
	if !strings.Contains(body, "status-todo") || !strings.Contains(body, "status-needs-human") {
		t.Fatalf("status pills missing from plan view:\n%s", body)
	}
}

// TestPlanChangeDocLinkFallsBackToDesignDoc is plan-view-detail-polish's core
// "none inert" fix: a task with no stamped implementing commit yet (still in
// flight) falls back to its planned "Doc:" design-doc convention line when
// THAT resolves to a real file under the submodule's docs/ tree — including a
// NESTED path (docs/tasks/...), which the old basename-only resolver missed
// (it would look for "haswork.md" flat under docs/, never finding it under
// docs/tasks/). A task whose Doc: line names a file that doesn't exist must
// still render with NO link — the "never a dead href" contract holds for the
// fallback exactly as it does for the commit-stamp mechanism.
func TestPlanChangeDocLinkFallsBackToDesignDoc(t *testing.T) {
	s, root := setup(t)
	sm := filepath.Join(root, "submodules", "alpha")
	tasksDir := filepath.Join(sm, "docs", "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "haswork.md"), []byte("# haswork design doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sm, repo.PlanFile), []byte(
		"<!-- Beehive-ROI: abc123 -->\n# Plan\n\n"+
			"## haswork [TODO] <!-- attempts=0 deps= -->\nin flight, no commit yet\nDoc: docs/tasks/haswork.md\n\n"+
			"## nodoc [TODO] <!-- attempts=0 deps= -->\nno design doc file on disk\nDoc: docs/tasks/missing.md\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	w := get(t, s, "/submodule/alpha/plan")
	if w.Code != 200 {
		t.Fatalf("plan %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, `href="/submodule/alpha/doc/tasks/haswork.md"`) {
		t.Fatalf("nested design-doc fallback link missing:\n%s", body)
	}
	if strings.Contains(body, "/doc/tasks/missing.md") {
		t.Fatalf("absent design doc must not be linked:\n%s", body)
	}
	if !strings.Contains(body, `<span class="muted">docs/tasks/missing.md</span>`) {
		t.Fatalf("unresolvable design doc should still show its path as inert text:\n%s", body)
	}
}

// TestPlanItemExpandRendersFullBody is the expand-in-place half of
// plan-view-detail-polish: a task row carries its full body (not just the
// clipped Desc line) rendered through renderMarkdown into a <details> the
// reader can open, sanitized the same way every other VIEW pane is (a script
// tag is dropped, real markup like a heading/list survives).
func TestPlanItemExpandRendersFullBody(t *testing.T) {
	s, _ := setup(t)
	pl := Plan{ROIStamp: "abc123", Items: []PlanItem{
		{
			ID: "imp", Status: StatusTODO, Desc: "implement it",
			Body: "implement it\n\n## Detail\n- one\n- two\n\n<script>alert(1)</script>",
		},
		{ID: "nobody", Status: StatusDone, Desc: "shipped, no body"},
	}}
	out := renderTmpl(t, s, "plan_items.html", map[string]interface{}{"Name": "alpha", "Plan": pl})
	if !strings.Contains(out, "<details>") || !strings.Contains(out, "<summary") {
		t.Fatalf("expand-in-place affordance missing:\n%s", out)
	}
	for _, want := range []string{`<div class="markdown">`, "<h2>Detail</h2>", "<li>one</li>", "<li>two</li>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expanded body missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "alert(1)") {
		t.Fatalf("expanded body's script payload was not sanitized:\n%s", out)
	}
	// A task with no body gets no expand affordance at all (nothing to reveal).
	if strings.Count(out, "<details>") != 1 {
		t.Fatalf("want exactly one expand affordance (only 'imp' has a body):\n%s", out)
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad time %q: %v", s, err)
	}
	return ts
}

func TestExplorerAndUnknown(t *testing.T) {
	s, _ := setup(t)
	if get(t, s, "/submodule/alpha").Code != 200 {
		t.Fatal("explorer")
	}
	if get(t, s, "/submodule/none").Code != 404 {
		t.Fatal("want 404")
	}
}

func TestROIRoundTrip(t *testing.T) {
	s, root := setup(t)
	form := url.Values{"body": {"# new intent\n"}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/roi/alpha", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.Routes().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("roi post %d: %s", w.Code, w.Body)
	}
	b, _ := os.ReadFile(filepath.Join(root, "submodules", "alpha", repo.ROIFile))
	if string(b) != "# new intent\n" {
		t.Fatalf("roi not written: %q", b)
	}
}

func TestEnvDeploy(t *testing.T) {
	s, root := setup(t)
	form := url.Values{"target": {"green"}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/submodule/alpha/env/deploy", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.Routes().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("deploy %d", w.Code)
	}
	// The write lands in the SUBMODULE's own INFRASTRUCTURE.md, not the root.
	b, _ := os.ReadFile(filepath.Join(root, "submodules", "alpha", repo.InfraFile))
	if !strings.Contains(string(b), "Active: green") {
		t.Fatalf("alpha env: %q", b)
	}
	// The root INFRASTRUCTURE.md is never touched by a per-submodule deploy.
	if _, err := os.Stat(filepath.Join(root, repo.InfraFile)); !os.IsNotExist(err) {
		if rb, _ := os.ReadFile(filepath.Join(root, repo.InfraFile)); strings.Contains(string(rb), "Active: green") {
			t.Fatalf("root INFRASTRUCTURE.md was mutated by a per-submodule deploy: %q", rb)
		}
	}
}

// TestEnvDeployPerSubmoduleIsolated is the core of env-badge-per-submodule: with
// two submodules in OPPOSITE blue/green states, deploying one switches only its
// own INFRASTRUCTURE.md — the other's active env, its panel, and its dashboard
// card badge are all unchanged. Blue/green is per-submodule, never global.
func TestEnvDeployPerSubmoduleIsolated(t *testing.T) {
	s, root := setup(t)
	// alpha active blue, bravo active green — deliberately opposite.
	if err := os.WriteFile(filepath.Join(root, "submodules", "alpha", repo.InfraFile),
		[]byte("# infra\nActive: blue\nEnvironments: blue, green\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bravo := filepath.Join(root, "submodules", "bravo")
	if err := os.MkdirAll(bravo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bravo, repo.ROIFile), []byte("# bravo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bravo, repo.InfraFile),
		[]byte("# infra\nActive: green\nEnvironments: blue, green\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Switch ONLY alpha: blue -> green.
	if w := postForm(t, s, "/submodule/alpha/env/deploy", url.Values{"target": {"green"}}); w.Code != 200 {
		t.Fatalf("alpha deploy %d: %s", w.Code, w.Body)
	}

	// alpha's own doc flipped to green.
	ab, _ := os.ReadFile(filepath.Join(root, "submodules", "alpha", repo.InfraFile))
	if !strings.Contains(string(ab), "Active: green") {
		t.Fatalf("alpha not switched to green: %q", ab)
	}
	// bravo's doc is byte-for-byte untouched (still green, its own state).
	bb, _ := os.ReadFile(filepath.Join(bravo, repo.InfraFile))
	if string(bb) != "# infra\nActive: green\nEnvironments: blue, green\n" {
		t.Fatalf("bravo INFRASTRUCTURE.md changed by alpha's deploy: %q", bb)
	}
	// bravo's panel still reports green (scoped read is unaffected).
	pb := get(t, s, "/submodule/bravo/env").Body.String()
	if !strings.Contains(pb, "active: <b>green</b>") {
		t.Fatalf("bravo panel not green after alpha deploy:\n%s", pb)
	}
	// bravo's dashboard card badge is still its OWN env (green), independent of
	// alpha now also being green: switch alpha to blue and confirm bravo stays.
	if w := postForm(t, s, "/submodule/alpha/env/deploy", url.Values{"target": {"blue"}}); w.Code != 200 {
		t.Fatalf("alpha re-deploy %d: %s", w.Code, w.Body)
	}
	now := time.Date(2026, 6, 30, 11, 30, 0, 0, time.UTC)
	views, err := s.subViews(context.Background(), now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]subView{}
	for _, v := range views {
		by[v.Name] = v
	}
	if by["alpha"].Env != "blue" {
		t.Errorf("alpha card Env = %q, want blue", by["alpha"].Env)
	}
	if by["bravo"].Env != "green" {
		t.Errorf("bravo card Env = %q, want green (unchanged by alpha's deploys)", by["bravo"].Env)
	}
}

func TestSecretsEmpty(t *testing.T) {
	s, _ := setup(t)
	keys, err := listSecretKeys(context.Background(), "x", filepath.Join(t.TempDir(), repo.SecretsFile))
	if err != nil || keys != nil {
		t.Fatalf("want empty: %v %v", keys, err)
	}
	if get(t, s, "/secrets").Code != 200 {
		t.Fatal("secrets get")
	}
}

func TestBranchesStamp(t *testing.T) {
	s, root := setup(t)
	rd := filepath.Join(root, "submodules", "alpha", "repo")
	os.MkdirAll(rd, 0o755)
	for _, a := range [][]string{{"init"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		c := exec.Command("git", a...)
		c.Dir = rd
		c.Run()
	}
	os.WriteFile(filepath.Join(rd, "f.txt"), []byte("x"), 0o644)
	for _, a := range [][]string{{"add", "-A"}, {"commit", "-m", "work\n\nBeehive: t1 br-t1.md"}} {
		c := exec.Command("git", a...)
		c.Dir = rd
		c.Run()
	}
	w := get(t, s, "/submodule/alpha/branches")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "br-t1.md") {
		t.Fatalf("branches %d: %s", w.Code, w.Body)
	}
}

// srcRepo makes a throwaway git repo with one commit on branch main, usable as a
// (file-protocol) submodule url for an offline `git submodule add`.
func srcRepo(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	os.MkdirAll(p, 0o755)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		c := exec.Command("git", a...)
		c.Dir = p
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", a, err, out)
		}
	}
	os.WriteFile(filepath.Join(p, "f.txt"), []byte("x"), 0o644)
	for _, a := range [][]string{{"add", "-A"}, {"commit", "-qm", "init"}} {
		c := exec.Command("git", a...)
		c.Dir = p
		c.Run()
	}
	return p
}

func postForm(t *testing.T, s *Server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.Routes().ServeHTTP(w, req)
	return w
}

// TestSubmoduleAdd proves the web add now creates a real tracked git submodule
// (.gitmodules entry + checked-out repo/), not the old inert bare dir.
func TestSubmoduleAdd(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	s, root := setup(t)
	src := srcRepo(t, "beta")

	w := postForm(t, s, "/submodule/add", url.Values{"url": {src}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("add %d: %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(root, "submodules", "beta", "repo", "f.txt")); err != nil {
		t.Fatalf("submodule repo not checked out: %v", err)
	}
	gm, _ := os.ReadFile(filepath.Join(root, ".gitmodules"))
	if !strings.Contains(string(gm), "submodules/beta/repo") {
		t.Fatalf(".gitmodules missing tracked entry:\n%s", gm)
	}
}

func TestSubmoduleAddRejectsBadInput(t *testing.T) {
	s, _ := setup(t)
	if w := postForm(t, s, "/submodule/add", url.Values{}); w.Code != http.StatusBadRequest {
		t.Fatalf("missing url: %d", w.Code)
	}
	if w := postForm(t, s, "/submodule/add", url.Values{"url": {"git@h:o/r.git"}, "name": {"../evil"}}); w.Code != http.StatusBadRequest {
		t.Fatalf("bad name: %d", w.Code)
	}
}

// TestSubmoduleLink proves the web link now writes schema-valid YAML through the
// cycle-checked links API (not a raw `from: [to]` append).
func TestSubmoduleLink(t *testing.T) {
	s, root := setup(t)
	if w := postForm(t, s, "/submodule/link", url.Values{"from": {"x"}, "to": {"y"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("link %d: %s", w.Code, w.Body)
	}
	l, err := links.Load(filepath.Join(root, repo.LinksFile))
	if err != nil {
		t.Fatalf("links file not schema-valid: %v", err)
	}
	if len(l.Deps) != 1 || l.Deps[0].From != "x" || l.Deps[0].To != "y" {
		t.Fatalf("deps = %v, want one x->y edge", l.Deps)
	}
}

func TestSubmoduleLinkRejectsCycle(t *testing.T) {
	s, root := setup(t)
	if w := postForm(t, s, "/submodule/link", url.Values{"from": {"a"}, "to": {"b"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("a->b: %d", w.Code)
	}
	if w := postForm(t, s, "/submodule/link", url.Values{"from": {"b"}, "to": {"c"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("b->c: %d", w.Code)
	}
	// c->a closes the cycle and must be rejected (not persisted).
	if w := postForm(t, s, "/submodule/link", url.Values{"from": {"c"}, "to": {"a"}}); w.Code < 400 {
		t.Fatalf("cycle c->a should be rejected, got %d", w.Code)
	}
	l, _ := links.Load(filepath.Join(root, repo.LinksFile))
	if l.HasCycle() {
		t.Fatal("rejected cycle edge was persisted")
	}
}

// TestAssetsStyleServed locks the design-system contract: the stylesheet is
// still embedded and served at /assets/style.css, exposes a token root with a
// dark-mode override, and defines a status pill class per task state plus the
// `.active` overlay. Downstream views (dashboard-cards, plan-view-pills) emit
// these exact class names, so a rename here must break this test on purpose.
func TestAssetsStyleServed(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/assets/style.css")
	if w.Code != 200 {
		t.Fatalf("style.css status %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/css") {
		t.Fatalf("content-type = %q, want text/css", ct)
	}
	body := w.Body.String()
	must := []string{
		":root",                      // token root
		"prefers-color-scheme: dark", // dark mode overrides
		".status-todo",
		".status-needs-review",
		".status-needs-arbitration",
		".status-done",
		".status-needs-human",
		".active", // session+heartbeat overlay (no IN-PROGRESS status)
	}
	for _, m := range must {
		if !strings.Contains(body, m) {
			t.Fatalf("style.css missing %q", m)
		}
	}
}

// TestRenderMarkdownSanitized is the core of editor-markdown-render: markdown
// renders to the expected HTML (headings/lists/code/emphasis) AND repo content
// is sanitized — a <script> block is dropped (not passed through) and a
// javascript: link protocol is stripped. The renderer is pure-Go/CGO-free
// (goldmark, no html.WithUnsafe).
func TestRenderMarkdownSanitized(t *testing.T) {
	src := "# Title\n\nIntro **bold** text.\n\n- one\n- two\n\n```\ncode block\n```\n\nInline `x` here.\n\n<script>alert('xss')</script>\n\n[evil](javascript:alert(1))\n\n[ok](https://example.com)\n"
	out := string(renderMarkdown(src))
	for _, want := range []string{"<h1>Title</h1>", "<li>one</li>", "<code>", "<strong>bold</strong>", `href="https://example.com"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered markdown missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<script>") {
		t.Fatalf("script tag was not sanitized out:\n%s", out)
	}
	if strings.Contains(out, "javascript:") {
		t.Fatalf("dangerous link protocol was not stripped:\n%s", out)
	}
}

// TestExplorerRendersMarkdown proves the explorer VIEW pane now renders doc
// markdown to HTML inside a .markdown container instead of dumping raw source
// into a <pre>. The alpha ROI ("# alpha") must surface as an <h1>.
func TestExplorerRendersMarkdown(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/submodule/alpha")
	if w.Code != 200 {
		t.Fatalf("explorer %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, `<div class="markdown">`) {
		t.Fatalf("explorer not rendering into a .markdown container:\n%s", body)
	}
	if !strings.Contains(body, "<h1>alpha</h1>") {
		t.Fatalf("ROI markdown heading not rendered:\n%s", body)
	}
	if strings.Contains(body, "<pre># alpha") {
		t.Fatalf("explorer still dumping raw markdown source:\n%s", body)
	}
}

// TestExplorerShowsAgentsAndRules locks the submodule-rules-md acceptance for the
// explorer: a present AGENTS.md and the beehive-owned RULES.md overlay both render,
// in AGENTS-then-RULES order (the additive-overlay order the agent context also
// applies). The docs map renders by sorted key, so "AGENTS" deterministically
// precedes "RULES".
func TestExplorerShowsAgentsAndRules(t *testing.T) {
	s, root := setup(t)
	sm := filepath.Join(root, "submodules", "alpha")
	if err := os.WriteFile(filepath.Join(sm, repo.AgentsFile), []byte("# alpha agents\nAGENTS-OVERLAY-MARKER\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sm, repo.RulesFile), []byte("# alpha rules\nRULES-OVERLAY-MARKER\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := get(t, s, "/submodule/alpha")
	if w.Code != 200 {
		t.Fatalf("explorer %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	// Both overlays are rendered (as markdown, not raw dumped).
	for _, want := range []string{"<h2>AGENTS</h2>", "AGENTS-OVERLAY-MARKER", "<h2>RULES</h2>", "RULES-OVERLAY-MARKER"} {
		if !strings.Contains(body, want) {
			t.Fatalf("explorer missing %q:\n%s", want, body)
		}
	}
	// AGENTS.md is applied first, then the additive RULES.md: the AGENTS section
	// must precede the RULES section in the rendered page.
	if ai, ri := strings.Index(body, "<h2>AGENTS</h2>"), strings.Index(body, "<h2>RULES</h2>"); ai < 0 || ri < 0 || ai > ri {
		t.Fatalf("explorer order not AGENTS-then-RULES (agents@%d rules@%d):\n%s", ai, ri, body)
	}
}

// TestExplorerRulesAbsentNoOp: with no RULES.md on disk the explorer renders
// normally (200) and simply omits the RULES section — an absent overlay is a safe
// no-op, never an error or an empty placeholder.
func TestExplorerRulesAbsentNoOp(t *testing.T) {
	s, root := setup(t)
	// Guard the fixture: setup writes ROI.md + PLAN.md only, no RULES.md.
	if _, err := os.Stat(filepath.Join(root, "submodules", "alpha", repo.RulesFile)); !os.IsNotExist(err) {
		t.Fatalf("fixture unexpectedly has RULES.md (err=%v)", err)
	}
	w := get(t, s, "/submodule/alpha")
	if w.Code != 200 {
		t.Fatalf("explorer %d: %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "<h2>RULES</h2>") {
		t.Fatalf("explorer showed a RULES section with no RULES.md on disk:\n%s", w.Body)
	}
}

// TestExplorerOptionalFileLinks locks optional-file-links: the explorer renders a
// view/edit (or, when absent, create) link for EVERY member of the declared
// optional-file set repo.OptionalFiles — driven by that SET, not the directory
// listing — so a file that does not exist yet is discoverable. In the fixture
// alpha has ROI.md present but no INFRASTRUCTURE/RULES/ARTIFACTS/AGENTS, yet all
// five links render (four of them for absent files).
func TestExplorerOptionalFileLinks(t *testing.T) {
	s, root := setup(t)
	sm := repo.Submodule{Name: "alpha", Path: filepath.Join(root, "submodules", "alpha")}
	// Guard the fixture: of the optional set only ROI.md exists on disk.
	for _, f := range []string{repo.InfraFile, repo.RulesFile, repo.Artifacts, repo.AgentsFile} {
		if _, err := os.Stat(filepath.Join(sm.Path, f)); !os.IsNotExist(err) {
			t.Fatalf("fixture unexpectedly has %s (err=%v)", f, err)
		}
	}

	// White-box: the index is built from the DECLARED set, one row per member,
	// with Present reflecting disk existence (not driving membership).
	links := optionalFileLinks(sm)
	if len(links) != len(repo.OptionalFiles) {
		t.Fatalf("optionalFileLinks = %d rows, want %d (the declared set)", len(links), len(repo.OptionalFiles))
	}
	present := map[string]bool{}
	for _, l := range links {
		if l.File == "" || l.Label == "" {
			t.Fatalf("row missing File/Label: %+v", l)
		}
		if l.Label != strings.TrimSuffix(l.File, ".md") {
			t.Errorf("row label %q not derived from file %q", l.Label, l.File)
		}
		present[l.File] = l.Present
	}
	if !present[repo.ROIFile] {
		t.Error("ROI.md is on disk but its row is marked absent")
	}
	for _, f := range []string{repo.InfraFile, repo.RulesFile, repo.Artifacts, repo.AgentsFile} {
		if _, ok := present[f]; !ok {
			t.Errorf("declared optional file %q missing a row (set must drive membership)", f)
		}
		if present[f] {
			t.Errorf("%q is absent on disk but its row is marked present", f)
		}
	}

	// Rendered: a link for EVERY optional file (incl. the four absent ones), the
	// present ROI as view/edit and the absent ones as create.
	body := get(t, s, "/submodule/alpha").Body.String()
	for _, f := range repo.OptionalFiles {
		if want := "/edit?path=submodules/alpha/" + f; !strings.Contains(body, want) {
			t.Errorf("explorer missing set-driven optional-file link %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "view / edit") {
		t.Errorf("explorer missing a view/edit link for the present ROI.md:\n%s", body)
	}
	if !strings.Contains(body, "create") || !strings.Contains(body, "not created") {
		t.Errorf("explorer missing a create affordance for the absent optional files:\n%s", body)
	}

	// Present detection follows disk: creating RULES.md flips its row to present.
	if err := os.WriteFile(filepath.Join(sm.Path, repo.RulesFile), []byte("# rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, l := range optionalFileLinks(sm) {
		if l.File == repo.RulesFile && !l.Present {
			t.Error("RULES.md now on disk but its row is still marked absent")
		}
	}
}

// TestExplorerAbsentFileEmptyBaseCreate locks the absent-member create path: the
// optional-file link opens the chat-diff editor on an EMPTY base, seeded with the
// file's rules (chat-diff-file-context), and the new file is written ONLY on
// approval (never auto-generated).
func TestExplorerAbsentFileEmptyBaseCreate(t *testing.T) {
	fc := &fakeChatClient{reply: proposeReply("Drafted infra.", "# Infra\nActive: blue")}
	s, _ := chatFixtureClient(t, fc)
	ctx := context.Background()
	path := "submodules/alpha/" + repo.InfraFile // absent in the fixture

	sess, err := s.chat.open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if base, _ := sess.base(ctx); base != "" {
		t.Fatalf("absent optional file must open on an empty base, got %q", base)
	}
	if sess.sys != chatSystemPrompt(path) {
		t.Fatal("session not seeded from resolveFileContext (chat-diff-file-context)")
	}
	if err := sess.chat(ctx, "draft infra"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	// The opencode session was seeded with the INFRASTRUCTURE.md rules.
	if !strings.Contains(fc.system, "internal/artifacts") {
		t.Fatalf("empty-base create not seeded with INFRASTRUCTURE rules:\n%s", fc.system)
	}
	// Nothing on disk before approval.
	if _, err := os.Stat(filepath.Join(sess.wtPath, filepath.FromSlash(path))); !os.IsNotExist(err) {
		t.Fatalf("file must not exist before approval (err=%v)", err)
	}
	if err := sess.approve(ctx); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if got := gitShow(t, sess.wtPath, "HEAD", path); got != "# Infra\nActive: blue" {
		t.Fatalf("empty-base create did not commit the new file: %q", got)
	}
}

// TestExplorerROICreateThroughEditorNoAutogen locks ROI ownership: the explorer
// routes ROI.md's create/edit through the chat-diff editor (not an auto-
// generator), the seeded context marks it human-owned / honeybee-FORBIDDEN, and a
// proposed ROI change is NEVER written without explicit human approval.
func TestExplorerROICreateThroughEditorNoAutogen(t *testing.T) {
	s0, _ := setup(t)
	body := get(t, s0, "/submodule/alpha").Body.String()
	if !strings.Contains(body, "/edit?path=submodules/alpha/"+repo.ROIFile) {
		t.Fatalf("explorer must route ROI create/edit through the editor:\n%s", body)
	}
	sp := chatSystemPrompt("submodules/alpha/" + repo.ROIFile)
	for _, want := range []string{"human-owned", "FORBIDDEN"} {
		if !strings.Contains(sp, want) {
			t.Errorf("ROI editor context missing ownership token %q:\n%s", want, sp)
		}
	}

	// Never auto-generated: a proposed ROI edit stays unwritten until approval.
	fc := &fakeChatClient{reply: proposeReply("Draft.", "# alpha\nGoals: ship it.")}
	s, _ := chatFixtureClient(t, fc)
	ctx := context.Background()
	path := "submodules/alpha/" + repo.ROIFile
	sess, err := s.chat.open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !strings.Contains(sess.sys, "human-owned") {
		t.Fatal("ROI session not seeded with human-owned rules")
	}
	if err := sess.chat(ctx, "draft"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if _, ok := sess.pending(); !ok {
		t.Fatal("expected a pending ROI proposal")
	}
	if got := gitShow(t, sess.wtPath, "HEAD", path); got != "# alpha" {
		t.Fatalf("ROI must not be auto-generated before approval, got %q", got)
	}
}

// TestDashboardRootInstructionFileLinks locks root-instruction-file-links: the
// dashboard renders a uniform view/edit (or, when absent, create) link for EVERY
// member of the declared repo-ROOT instruction-file set repo.RootInstructionFiles
// — driven by that SET, not the directory listing — with the per-file managed flag
// exposed. In the fixture (repo.Init) AGENTS/HONEYBEE/BOOTSTRAP exist but LOCALS.md
// does not, yet all four links render (LOCALS as a create path), and a plain manual
// LOCALS.md commit picked up off disk flips its row to present on the next render.
func TestDashboardRootInstructionFileLinks(t *testing.T) {
	s, root := setup(t)
	// Guard the fixture: the three managed files are laid down by Init; LOCALS is not.
	for _, f := range []string{repo.AgentsFile, repo.HoneybeeFile, repo.BootstrapFile} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Fatalf("fixture missing managed root file %s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, repo.LocalsFile)); !os.IsNotExist(err) {
		t.Fatalf("fixture unexpectedly has LOCALS.md (err=%v)", err)
	}

	// White-box: one row per declared member, membership set-driven, Present
	// following disk and Managed following the SET (not disk).
	links := s.rootFileLinks()
	if len(links) != len(repo.RootInstructionFiles) {
		t.Fatalf("rootFileLinks = %d rows, want %d (the declared set)", len(links), len(repo.RootInstructionFiles))
	}
	byFile := map[string]rootFileLink{}
	for _, l := range links {
		if l.File == "" || l.Label != strings.TrimSuffix(l.File, ".md") {
			t.Fatalf("row label %q not derived from file %q", l.Label, l.File)
		}
		byFile[l.File] = l
	}
	for _, want := range []struct {
		file    string
		present bool
		managed bool
	}{
		{repo.AgentsFile, true, true},
		{repo.HoneybeeFile, true, true},
		{repo.BootstrapFile, true, true},
		{repo.LocalsFile, false, false},
	} {
		got, ok := byFile[want.file]
		if !ok {
			t.Errorf("root set missing a row for %q (set must drive membership)", want.file)
			continue
		}
		if got.Present != want.present {
			t.Errorf("%s Present = %v, want %v", want.file, got.Present, want.present)
		}
		if got.Managed != want.managed {
			t.Errorf("%s Managed = %v, want %v", want.file, got.Managed, want.managed)
		}
	}

	// Rendered: a root-relative editor link for EVERY member (incl. absent LOCALS),
	// the present ones as view/edit and the absent one as a create path, plus the
	// exposed managed / site-authored flags.
	body := get(t, s, "/").Body.String()
	for _, f := range repo.RootInstructionFiles {
		if want := "/edit?path=" + f.File; !strings.Contains(body, want) {
			t.Errorf("dashboard missing set-driven root-file link %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "view / edit") {
		t.Error("dashboard missing a view/edit link for the present managed root files")
	}
	if !strings.Contains(body, "not created") || !strings.Contains(body, "create") {
		t.Error("dashboard missing a create affordance for the absent LOCALS.md")
	}
	if !strings.Contains(body, ">managed<") || !strings.Contains(body, ">site-authored<") {
		t.Errorf("dashboard did not expose the managed / site-authored flags:\n%s", body)
	}

	// Manual-commit-picked-up: creating LOCALS.md on disk (as a plain manual commit
	// would) flips its row to present on the next render — no special write path.
	if err := os.WriteFile(filepath.Join(root, repo.LocalsFile), []byte("# locals\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var locals rootFileLink
	for _, l := range s.rootFileLinks() {
		if l.File == repo.LocalsFile {
			locals = l
		}
	}
	if !locals.Present {
		t.Error("LOCALS.md now on disk but its row is still marked absent")
	}
	if locals.Managed {
		t.Error("LOCALS.md must stay site-authored (Managed=false) even once present")
	}
}

// TestManagedRootFilesAreInstructManaged guards the two sources that must agree:
// every Managed member of repo.RootInstructionFiles (what the dashboard scopes its
// drift check and update action to) MUST be a file the shared installer
// instruct.Files() actually ships/refreshes a default for — otherwise a "managed"
// badge would promise an update the installer cannot perform. Conversely the
// site-authored LOCALS.md must be in NEITHER set (categorically excluded).
func TestManagedRootFilesAreInstructManaged(t *testing.T) {
	instructNames := map[string]bool{}
	for _, f := range instruct.Files() {
		instructNames[f.Name] = true
	}
	for _, rf := range repo.RootInstructionFiles {
		if rf.Managed && !instructNames[rf.File] {
			t.Errorf("%s is Managed but not in instruct.Files() (drift badge would promise an impossible update)", rf.File)
		}
		if !rf.Managed && instructNames[rf.File] {
			t.Errorf("%s is site-authored but instruct.Files() manages it (should be excluded)", rf.File)
		}
	}
	if instructNames[repo.LocalsFile] {
		t.Error("LOCALS.md must never be in the managed installer set")
	}
}

// TestRootFileLinksDriftStatus locks the read-only drift status rootFileLinks
// attaches to each MANAGED root file (instruction-update-drift): clean when the
// on-disk file is byte-identical to the binary's embedded default, "drift" when it
// exists but differs, "missing" when absent. The site-authored LOCALS.md is
// categorically excluded (empty Drift, no badge) even when present with custom
// content, and rootFilesDrift aggregates only the managed reasons to run update.
func TestRootFileLinksDriftStatus(t *testing.T) {
	s, root := setup(t)

	driftOf := func() map[string]string {
		m := map[string]string{}
		for _, l := range s.rootFileLinks() {
			m[l.File] = l.Drift
		}
		return m
	}

	// Fresh fixture: Init lays the managed files down from the SAME embedded
	// defaults, so all three read clean and nothing has drifted.
	d := driftOf()
	for _, f := range []string{repo.AgentsFile, repo.HoneybeeFile, repo.BootstrapFile} {
		if d[f] != "clean" {
			t.Errorf("%s Drift = %q, want clean on a fresh fixture", f, d[f])
		}
	}
	if d[repo.LocalsFile] != "" {
		t.Errorf("LOCALS.md Drift = %q, want empty (site-authored, excluded)", d[repo.LocalsFile])
	}
	if rootFilesDrift(s.rootFileLinks()) {
		t.Error("rootFilesDrift = true on a clean fixture, want false")
	}

	// A managed file edited on disk drifts, and flips the aggregate.
	if err := os.WriteFile(filepath.Join(root, repo.AgentsFile), []byte("# hand-edited AGENTS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := driftOf(); d[repo.AgentsFile] != "drift" {
		t.Errorf("edited AGENTS.md Drift = %q, want drift", d[repo.AgentsFile])
	}
	if !rootFilesDrift(s.rootFileLinks()) {
		t.Error("rootFilesDrift = false after a managed file drifted, want true")
	}

	// A deleted managed file reads missing (still a reason to run update).
	if err := os.Remove(filepath.Join(root, repo.HoneybeeFile)); err != nil {
		t.Fatal(err)
	}
	if d := driftOf(); d[repo.HoneybeeFile] != "missing" {
		t.Errorf("removed HONEYBEE.md Drift = %q, want missing", d[repo.HoneybeeFile])
	}

	// LOCALS.md present with custom content stays excluded: never a drift badge.
	if err := os.WriteFile(filepath.Join(root, repo.LocalsFile), []byte("# my locals\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := driftOf(); d[repo.LocalsFile] != "" {
		t.Errorf("site-authored LOCALS.md Drift = %q, want empty even when present", d[repo.LocalsFile])
	}
}

// TestDashboardDriftBadgeAndUpdateAction locks the dashboard surface for
// instruction-update-drift: a per-managed-file drift badge and the POST
// /instruction/update action. A clean fixture shows clean badges plus the
// idempotent-action copy; a drifted managed file surfaces a drift badge and the
// "have drifted" emphasis.
func TestDashboardDriftBadgeAndUpdateAction(t *testing.T) {
	s, root := setup(t)

	clean := get(t, s, "/").Body.String()
	if !strings.Contains(clean, `action="/instruction/update"`) {
		t.Errorf("dashboard missing the instruction-update action:\n%s", clean)
	}
	if !strings.Contains(clean, "drift-clean") {
		t.Error("dashboard missing a clean drift badge for the managed files")
	}
	if !strings.Contains(clean, "managed files match the shipped default") {
		t.Error("clean fixture should show the up-to-date instruction-update copy")
	}
	// A clean managed set never renders a drift/missing badge.
	if strings.Contains(clean, "drift-drift") || strings.Contains(clean, "drift-missing") {
		t.Errorf("clean fixture must not render a drift/missing badge:\n%s", clean)
	}

	// Drift a managed file: the badge and the emphasis copy flip.
	if err := os.WriteFile(filepath.Join(root, repo.AgentsFile), []byte("# hand-edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	drifted := get(t, s, "/").Body.String()
	if !strings.Contains(drifted, "drift-drift") {
		t.Errorf("dashboard missing a drift badge after a managed file drifted:\n%s", drifted)
	}
	if !strings.Contains(drifted, "managed files have drifted from the shipped default") {
		t.Error("drifted set should show the drift emphasis copy")
	}
}

// TestInstructionUpdateRestoresManagedFiles drives POST /instruction/update end to
// end: a drifted managed file is backed up to <name>.<epoch>.bak and restored to
// the binary's embedded default, a missing one is recreated, and the site-authored
// LOCALS.md is never touched or backed up. A second run is a no-op (idempotent):
// still exactly one backup and the managed set reads clean.
func TestInstructionUpdateRestoresManagedFiles(t *testing.T) {
	s, root := setup(t)
	// A real hive always has history; seed an initial commit so the installer's
	// scoped CommitPaths runs against a born HEAD (the production path).
	commitAll(t, root, "seed")

	const customAgents = "# hand-edited AGENTS\n"
	const customLocals = "# site locals\nHost: spray\n"
	if err := os.WriteFile(filepath.Join(root, repo.AgentsFile), []byte(customAgents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, repo.BootstrapFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, repo.LocalsFile), []byte(customLocals), 0o644); err != nil {
		t.Fatal(err)
	}

	if w := postForm(t, s, "/instruction/update", url.Values{}); w.Code != http.StatusSeeOther {
		t.Fatalf("instruction update %d: %s", w.Code, w.Body)
	}

	// AGENTS.md restored to the embedded default...
	if got, _ := os.ReadFile(filepath.Join(root, repo.AgentsFile)); string(got) != prompts.Agents {
		t.Error("AGENTS.md not restored to the binary's embedded default")
	}
	// ...with its prior contents preserved in exactly one <name>.<epoch>.bak.
	baks, _ := filepath.Glob(filepath.Join(root, repo.AgentsFile+".*.bak"))
	if len(baks) != 1 {
		t.Fatalf("AGENTS.md backups = %d, want exactly 1: %v", len(baks), baks)
	}
	if b, _ := os.ReadFile(baks[0]); string(b) != customAgents {
		t.Errorf("backup did not preserve the operator's edit, got %q", b)
	}
	// The missing BOOTSTRAP.md was recreated from the default.
	if got, _ := os.ReadFile(filepath.Join(root, repo.BootstrapFile)); string(got) != prompts.BootstrapGuide {
		t.Error("BOOTSTRAP.md not recreated from the embedded default")
	}
	// LOCALS.md was never touched and never backed up (excluded from the set).
	if got, _ := os.ReadFile(filepath.Join(root, repo.LocalsFile)); string(got) != customLocals {
		t.Errorf("site-authored LOCALS.md was modified: %q", got)
	}
	if lbaks, _ := filepath.Glob(filepath.Join(root, repo.LocalsFile+".*.bak")); len(lbaks) != 0 {
		t.Errorf("LOCALS.md must never be backed up, found %v", lbaks)
	}

	// Idempotent: a second run over the now-clean set writes nothing new.
	if w := postForm(t, s, "/instruction/update", url.Values{}); w.Code != http.StatusSeeOther {
		t.Fatalf("second instruction update %d: %s", w.Code, w.Body)
	}
	if baks2, _ := filepath.Glob(filepath.Join(root, repo.AgentsFile+".*.bak")); len(baks2) != 1 {
		t.Fatalf("second run changed backup count to %d, want still 1: %v", len(baks2), baks2)
	}
	if rootFilesDrift(s.rootFileLinks()) {
		t.Error("managed set still drifted after instruction update")
	}
}

// TestRootAbsentFileEmptyBaseCreateSeeded locks the absent root-file create path:
// the site-authored LOCALS.md link opens the chat-diff editor on an EMPTY base,
// seeded with LOCALS.md's site-authored / never-auto-generated rules (chat-diff-
// file-context), and the file is written ONLY on approval (never auto-generated).
func TestRootAbsentFileEmptyBaseCreateSeeded(t *testing.T) {
	fc := &fakeChatClient{reply: proposeReply("Drafted locals.", "# LOCALS\nHost: spray")}
	s, _ := chatFixtureClient(t, fc)
	ctx := context.Background()
	path := repo.LocalsFile // absent in the fixture (Init does not lay it down)

	sess, err := s.chat.open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if base, _ := sess.base(ctx); base != "" {
		t.Fatalf("absent root file must open on an empty base, got %q", base)
	}
	if sess.sys != chatSystemPrompt(path) {
		t.Fatal("session not seeded from resolveFileContext (chat-diff-file-context)")
	}
	if err := sess.chat(ctx, "draft locals"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	// Seeded with the LOCALS.md ownership rules: site-authored, never auto-generated.
	for _, want := range []string{repo.LocalsFile, "auto-generated"} {
		if !strings.Contains(fc.system, want) {
			t.Fatalf("empty-base create not seeded with LOCALS rules (missing %q):\n%s", want, fc.system)
		}
	}
	// Nothing on disk before approval — LOCALS.md is never auto-generated.
	if _, err := os.Stat(filepath.Join(sess.wtPath, filepath.FromSlash(path))); !os.IsNotExist(err) {
		t.Fatalf("LOCALS.md must not exist before approval (err=%v)", err)
	}
	if err := sess.approve(ctx); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if got := gitShow(t, sess.wtPath, "HEAD", path); got != "# LOCALS\nHost: spray" {
		t.Fatalf("empty-base create did not commit the new LOCALS.md: %q", got)
	}
}

// TestROIViewRendersAndRawVerbatim locks the dual contract for the editor: the
// ROI VIEW shows a rendered preview while the editable textarea keeps the RAW
// source verbatim (the edit round-trip must not be lost to rendering).
func TestROIViewRendersAndRawVerbatim(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/roi/alpha")
	if w.Code != 200 {
		t.Fatalf("roi get %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, `class="markdown"`) || !strings.Contains(body, "<h1>alpha</h1>") {
		t.Fatalf("ROI view missing rendered preview:\n%s", body)
	}
	if !strings.Contains(body, "<textarea") || !strings.Contains(body, "# alpha") {
		t.Fatalf("ROI editable textarea missing verbatim raw source:\n%s", body)
	}
}

// commitRepoAt inits a git repo at dir and lays down one commit per message
// (each appended to f.txt so every commit changes the tree, even on repeats).
// A message may carry a stamp body, e.g. "subj\n\nBeehive: <task> <doc>".
func commitRepoAt(t *testing.T, dir string, msgs ...string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	git := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	body := ""
	for _, m := range msgs {
		body += m + "\n"
		os.WriteFile(filepath.Join(dir, "f.txt"), []byte(body), 0o644)
		git("add", "-A")
		git("commit", "-q", "-m", m)
	}
}

// TestSectionByDate is the unit core of the "sectioned" requirement: commits
// (newest-first, as git log emits them) group into one section per date,
// preserving order and membership, with no cross-date bleed.
func TestSectionByDate(t *testing.T) {
	cs := []Commit{
		{SHA: "a", Date: "2026-06-30", Subject: "x"},
		{SHA: "b", Date: "2026-06-30", Subject: "y"},
		{SHA: "c", Date: "2026-06-29", Subject: "z"},
	}
	secs := sectionByDate(cs)
	if len(secs) != 2 {
		t.Fatalf("want 2 date sections, got %d: %+v", len(secs), secs)
	}
	if secs[0].Date != "2026-06-30" || len(secs[0].Commits) != 2 {
		t.Fatalf("section 0 = %+v, want 2026-06-30 with 2 commits", secs[0])
	}
	if secs[1].Date != "2026-06-29" || len(secs[1].Commits) != 1 || secs[1].Commits[0].SHA != "c" {
		t.Fatalf("section 1 = %+v, want 2026-06-29 with only c", secs[1])
	}
}

// TestCommitGraphPagination drives commitGraph against a real repo: it parses
// SHA/subject and the Beehive doc stamp, and offset/limit bound the page
// (newest-first). It reads ONE repoDir, so it can never crawl submodules.
func TestCommitGraphPagination(t *testing.T) {
	rd := filepath.Join(t.TempDir(), "repo")
	commitRepoAt(t, rd, "c1", "c2", "c3 subj\n\nBeehive: task3 docs/bee-c3.md")
	page1, err := commitGraph(context.Background(), rd, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 || page1[0].Subject != "c3 subj" || page1[1].Subject != "c2" {
		t.Fatalf("page1 = %+v, want [c3 subj, c2]", page1)
	}
	if page1[0].SHA == "" || len(page1[0].SHA) > 12 {
		t.Fatalf("SHA not parsed/bounded: %q", page1[0].SHA)
	}
	if page1[0].DocTask != "task3" || page1[0].DocPath != "docs/bee-c3.md" {
		t.Fatalf("doc stamp not split: task=%q path=%q", page1[0].DocTask, page1[0].DocPath)
	}
	page2, err := commitGraph(context.Background(), rd, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || page2[0].Subject != "c1" {
		t.Fatalf("page2 = %+v, want [c1]", page2)
	}
}

// TestBranchesSectionedScoped proves the rendered branch view is sectioned (a
// .card section with a date <h2>) AND scoped to ONE submodule: alpha's page
// shows alpha's commit and never beta's (no cross-submodule crawl).
func TestBranchesSectionedScoped(t *testing.T) {
	s, root := setup(t)
	commitRepoAt(t, filepath.Join(root, "submodules", "alpha", "repo"), "alpha-only-commit")
	commitRepoAt(t, filepath.Join(root, "submodules", "beta", "repo"), "beta-only-commit")

	w := get(t, s, "/submodule/alpha/branches")
	if w.Code != 200 {
		t.Fatalf("branches %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, `<section class="card">`) || !strings.Contains(body, "<h2>") {
		t.Fatalf("branch view is not sectioned:\n%s", body)
	}
	if !strings.Contains(body, "alpha-only-commit") {
		t.Fatalf("alpha commit missing from its own branch view:\n%s", body)
	}
	if strings.Contains(body, "beta-only-commit") {
		t.Fatalf("branch view crawled into another submodule (beta commit leaked):\n%s", body)
	}
}

// TestBranchesDocLink proves the commit-stamp linkage: a stamp whose doc exists
// under the submodule's docs/ renders a link to a doc view that serves the
// rendered markdown; a stamp whose doc is absent renders no (dead) link.
func TestBranchesDocLink(t *testing.T) {
	s, root := setup(t)
	docsDir := filepath.Join(root, "submodules", "alpha", "docs")
	os.MkdirAll(docsDir, 0o755)
	os.WriteFile(filepath.Join(docsDir, "bee-doc.md"), []byte("# Doc Heading\n\nbody text\n"), 0o644)
	commitRepoAt(t, filepath.Join(root, "submodules", "alpha", "repo"),
		"has doc\n\nBeehive: dtask docs/bee-doc.md",
		"no doc\n\nBeehive: mtask docs/missing.md")

	w := get(t, s, "/submodule/alpha/branches")
	body := w.Body.String()
	if !strings.Contains(body, `href="/submodule/alpha/doc/bee-doc.md"`) {
		t.Fatalf("resolved change-doc link missing:\n%s", body)
	}
	if strings.Contains(body, "/doc/missing.md") {
		t.Fatalf("absent change doc must not be linked:\n%s", body)
	}

	d := get(t, s, "/submodule/alpha/doc/bee-doc.md")
	if d.Code != 200 {
		t.Fatalf("doc view %d: %s", d.Code, d.Body)
	}
	if db := d.Body.String(); !strings.Contains(db, "<h1>Doc Heading</h1>") || !strings.Contains(db, `class="markdown"`) {
		t.Fatalf("doc view did not render markdown:\n%s", db)
	}
}

// TestDocViewGuards locks the doc handler's safety: a traversal-unsafe filename
// and a missing doc both 404 (never a read outside submodules/<sm>/docs/).
func TestDocViewGuards(t *testing.T) {
	s, _ := setup(t)
	if w := get(t, s, "/submodule/alpha/doc/a%20b.md"); w.Code != http.StatusNotFound {
		t.Fatalf("unsafe doc name: got %d, want 404", w.Code)
	}
	if w := get(t, s, "/submodule/alpha/doc/nope.md"); w.Code != http.StatusNotFound {
		t.Fatalf("missing doc: got %d, want 404", w.Code)
	}
}

// TestDocExplorerListsWholeTree proves submodule-doc-explorer's core contract:
// every file under docs/ — a top-level change doc, an audit report under
// docs/audit/, and a task design doc under docs/tasks/ — is listed with a
// working link into the existing doc viewer, not just docs reachable from a
// task/branch row.
func TestDocExplorerListsWholeTree(t *testing.T) {
	s, root := setup(t)
	docsDir := filepath.Join(root, "submodules", "alpha", "docs")
	os.MkdirAll(filepath.Join(docsDir, "audit"), 0o755)
	os.MkdirAll(filepath.Join(docsDir, "tasks"), 0o755)
	os.WriteFile(filepath.Join(docsDir, "bee-doc.md"), []byte("# Change Doc\n"), 0o644)
	os.WriteFile(filepath.Join(docsDir, "audit", "session-audit-001.md"), []byte("# Audit\n"), 0o644)
	os.WriteFile(filepath.Join(docsDir, "tasks", "some-task.md"), []byte("# Task Design\n"), 0o644)

	w := get(t, s, "/submodule/alpha/docs")
	if w.Code != 200 {
		t.Fatalf("docs explorer %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	links := []string{
		"/submodule/alpha/doc/bee-doc.md",
		"/submodule/alpha/doc/audit/session-audit-001.md",
		"/submodule/alpha/doc/tasks/some-task.md",
	}
	for _, href := range links {
		if !strings.Contains(body, `href="`+href+`"`) {
			t.Fatalf("doc explorer missing link %q:\n%s", href, body)
		}
	}
	// The links must actually resolve through the existing doc viewer.
	for _, href := range links {
		if d := get(t, s, href); d.Code != 200 {
			t.Fatalf("doc viewer %s: got %d, want 200: %s", href, d.Code, d.Body)
		}
	}
}

// TestDocExplorerNoCrossSubmoduleLeakage proves docExplorer is scoped to ONE
// submodule's docs/ dir: alpha's docs page must never show beta's doc names or
// links, even when both have a docs/ tree.
func TestDocExplorerNoCrossSubmoduleLeakage(t *testing.T) {
	s, root := setup(t)
	alphaDocs := filepath.Join(root, "submodules", "alpha", "docs")
	betaDocs := filepath.Join(root, "submodules", "beta", "docs")
	os.MkdirAll(alphaDocs, 0o755)
	os.MkdirAll(betaDocs, 0o755)
	os.WriteFile(filepath.Join(alphaDocs, "alpha-only.md"), []byte("# Alpha\n"), 0o644)
	os.WriteFile(filepath.Join(betaDocs, "beta-only.md"), []byte("# Beta\n"), 0o644)

	body := get(t, s, "/submodule/alpha/docs").Body.String()
	if !strings.Contains(body, "alpha-only.md") {
		t.Fatalf("alpha's own doc missing from its docs page:\n%s", body)
	}
	if strings.Contains(body, "beta-only.md") || strings.Contains(body, "/submodule/beta/") {
		t.Fatalf("alpha docs page leaked beta's docs:\n%s", body)
	}
}

// TestDocExplorerUnknownSubmodule404s locks that an unregistered submodule name
// 404s rather than rendering an empty/leaky page.
func TestDocExplorerUnknownSubmodule404s(t *testing.T) {
	s, _ := setup(t)
	if w := get(t, s, "/submodule/none/docs"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown submodule docs: got %d, want 404", w.Code)
	}
}

// TestDocExplorerEmptyDocsDir proves an unbootstrapped/docless submodule is a
// safe no-op — a 200 with an empty listing, never an error — matching
// resolveDocHref/changeDocsByTask's "absence is a safe no-op" contract.
func TestDocExplorerEmptyDocsDir(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/submodule/alpha/docs")
	if w.Code != 200 {
		t.Fatalf("docs explorer with no docs/ dir: got %d, want 200: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "No docs yet") {
		t.Fatalf("expected empty-state message:\n%s", w.Body.String())
	}
}

// TestDocPathTraversalGuards locks the {file...} doc route's safety for NESTED
// paths: a percent-encoded traversal segment reaching the handler as an
// already-decoded ".." must 404 (never a read outside submodules/<sm>/docs/),
// while a legitimate nested path (docs/audit/*) resolves.
func TestDocPathTraversalGuards(t *testing.T) {
	s, root := setup(t)
	docsDir := filepath.Join(root, "submodules", "alpha", "docs", "audit")
	os.MkdirAll(docsDir, 0o755)
	os.WriteFile(filepath.Join(docsDir, "ok.md"), []byte("# OK\n"), 0o644)

	if w := get(t, s, "/submodule/alpha/doc/audit/ok.md"); w.Code != 200 {
		t.Fatalf("nested doc: got %d, want 200: %s", w.Code, w.Body)
	}
	for _, bad := range []string{
		"/submodule/alpha/doc/..%2F..%2F..%2Fetc%2Fpasswd",
		"/submodule/alpha/doc/audit%2F..%2F..%2FROI.md",
		"/submodule/alpha/doc/%2e%2e/%2e%2e/ROI.md",
	} {
		if w := get(t, s, bad); w.Code != http.StatusNotFound {
			t.Fatalf("traversal %q: got %d, want 404", bad, w.Code)
		}
	}
}

// TestDocTreeWalksSubdirs is the unit core of docTree: it must find files at
// the docs/ root AND nested under docs/audit/, docs/tasks/, tagging each with
// its parent Dir and a Href through the doc viewer, and a docs/ dir that does
// not exist must yield (nil, nil) rather than an error.
func TestDocTreeWalksSubdirs(t *testing.T) {
	root := t.TempDir()
	sm := repo.Submodule{Name: "alpha", Path: filepath.Join(root, "submodules", "alpha")}
	entries, err := docTree(sm)
	if err != nil || entries != nil {
		t.Fatalf("docTree with no docs/ dir = %v, %v, want nil, nil", entries, err)
	}

	docsDir := filepath.Join(sm.Path, "docs")
	os.MkdirAll(filepath.Join(docsDir, "audit"), 0o755)
	os.MkdirAll(filepath.Join(docsDir, "tasks"), 0o755)
	os.WriteFile(filepath.Join(docsDir, "top.md"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(docsDir, "audit", "a1.md"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(docsDir, "tasks", "t1.md"), []byte("x"), 0o644)

	entries, err = docTree(sm)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("docTree = %d entries, want 3: %+v", len(entries), entries)
	}
	byPath := map[string]DocEntry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}
	if top, ok := byPath["top.md"]; !ok || top.Dir != "" || top.Href != "/submodule/alpha/doc/top.md" {
		t.Fatalf("top-level entry wrong: %+v", top)
	}
	if a1, ok := byPath["audit/a1.md"]; !ok || a1.Dir != "audit" || a1.Name != "a1.md" || a1.Href != "/submodule/alpha/doc/audit/a1.md" {
		t.Fatalf("audit entry wrong: %+v", a1)
	}
	if t1, ok := byPath["tasks/t1.md"]; !ok || t1.Dir != "tasks" || t1.Href != "/submodule/alpha/doc/tasks/t1.md" {
		t.Fatalf("tasks entry wrong: %+v", t1)
	}
}

// TestSectionDocsGroupsByDir proves sectionDocs groups by directory with root
// files leading, THEN alphabetical subdirectories — and that this holds even
// when a root file's name would sort ALPHABETICALLY between two subdirectory
// names (i.e. it cannot rely on the input's flat lexical order keeping a
// directory's members contiguous).
func TestSectionDocsGroupsByDir(t *testing.T) {
	entries := []DocEntry{
		{Path: "audit/a.md", Dir: "audit", Name: "a.md"},
		{Path: "bee-root.md", Dir: "", Name: "bee-root.md"}, // sorts BETWEEN "audit/" and "tasks/"
		{Path: "tasks/t.md", Dir: "tasks", Name: "t.md"},
	}
	secs := sectionDocs(entries)
	if len(secs) != 3 {
		t.Fatalf("want 3 sections, got %d: %+v", len(secs), secs)
	}
	if secs[0].Dir != "" || len(secs[0].Files) != 1 || secs[0].Files[0].Name != "bee-root.md" {
		t.Fatalf("root section not first: %+v", secs[0])
	}
	if secs[1].Dir != "audit" || len(secs[1].Files) != 1 {
		t.Fatalf("audit section wrong: %+v", secs[1])
	}
	if secs[2].Dir != "tasks" || len(secs[2].Files) != 1 {
		t.Fatalf("tasks section wrong: %+v", secs[2])
	}
}

// TestSafeDocPath locks the traversal guard directly: nested segments are
// allowed, but "..", an absolute path, a doubled slash, or an unsafe character
// anywhere is rejected.
func TestSafeDocPath(t *testing.T) {
	ok := []string{"bee-doc.md", "audit/session-audit-001.md", "tasks/some-task.md", "a.b-c_d.md"}
	for _, p := range ok {
		if !safeDocPath(p) {
			t.Errorf("safeDocPath(%q) = false, want true", p)
		}
	}
	bad := []string{"", "/etc/passwd", "../ROI.md", "audit/../ROI.md", "audit/ ok.md", "audit//ok.md"}
	for _, p := range bad {
		if safeDocPath(p) {
			t.Errorf("safeDocPath(%q) = true, want false", p)
		}
	}
}

// writeHivePlan overwrites a submodule's PLAN.md (in the hive checkout, NOT a
// submodule code repo) with a single task at status, for delivery-traceability
// tests that need to commit a real TODO -> NEEDS-REVIEW -> DONE sequence to the
// HIVE superproject's own history.
func writeHivePlan(t *testing.T, root, sub, taskID, status string) {
	t.Helper()
	planPath := filepath.Join(root, "submodules", sub, repo.PlanFile)
	if err := os.WriteFile(planPath, []byte(
		"<!-- Beehive-ROI: abc123 -->\n# Plan\n\n"+
			"## "+taskID+" ["+status+"] <!-- attempts=0 deps= weight=1 -->\n"+
			"build it\nDoc: bee-"+taskID+".md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestHiveDoneFlips locks hiveDoneFlips' core contract directly (white-box):
// scanning the HIVE repo's own PLAN.md history, it returns the commit that
// FIRST shows a task's status line as [DONE] — not an earlier TODO/
// NEEDS-REVIEW commit, and not a wholly unrelated task that never went DONE.
func TestHiveDoneFlips(t *testing.T) {
	root := t.TempDir()
	hygGit(t, root, "init", "-q")
	hygGit(t, root, "config", "user.email", "t@t")
	hygGit(t, root, "config", "user.name", "t")
	os.MkdirAll(filepath.Join(root, "submodules", "alpha"), 0o755)

	writeHivePlan(t, root, "alpha", "dt1", "TODO")
	hygGit(t, root, "add", "-A")
	hygGit(t, root, "commit", "-q", "-m", "c1 todo")
	writeHivePlan(t, root, "alpha", "dt1", "NEEDS-REVIEW")
	hygGit(t, root, "add", "-A")
	hygGit(t, root, "commit", "-q", "-m", "c2 review")
	writeHivePlan(t, root, "alpha", "dt1", "DONE")
	hygGit(t, root, "add", "-A")
	hygGit(t, root, "commit", "-q", "-m", "c3 done")
	want := hygGit(t, root, "rev-parse", "HEAD")

	planRel := filepath.Join("submodules", "alpha", repo.PlanFile)
	flips := hiveDoneFlips(context.Background(), git.New(root), planRel,
		map[string]bool{"dt1": true, "never-done": true})
	if got := flips["dt1"]; got == "" || !strings.HasPrefix(want, got) {
		t.Fatalf("hiveDoneFlips[dt1] = %q, want prefix of %q", got, want)
	}
	if got, ok := flips["never-done"]; ok {
		t.Fatalf("never-done task must not appear in the result, got %q", got)
	}
}

// TestComputeStatsDeliveryLinks is delivery-traceability's core end-to-end
// assertion: computeStats (and the /stats page it feeds) links a DONE task to
// BOTH the hive commit that flipped its PLAN.md status to DONE (half a) and
// its submodule change doc (half b, branch-graph-sectioned's unchanged
// resolveDocHref/changeDocsByTask). The negative control (submodule "beta")
// has a DONE task whose PLAN.md was never committed to the hive and whose
// submodule repo/ doesn't exist at all — both links must degrade to "" with no
// error, never a dead href.
func TestComputeStatsDeliveryLinks(t *testing.T) {
	s, root := setup(t)

	writeHivePlan(t, root, "alpha", "dt1", "TODO")
	commitAll(t, root, "dt1-todo")
	writeHivePlan(t, root, "alpha", "dt1", "NEEDS-REVIEW")
	commitAll(t, root, "dt1-review")
	writeHivePlan(t, root, "alpha", "dt1", "DONE")
	commitAll(t, root, "dt1-done")
	flipSHA := hygGit(t, root, "rev-parse", "--short=12", "HEAD")

	docsDir := filepath.Join(root, "submodules", "alpha", "docs")
	os.MkdirAll(docsDir, 0o755)
	os.WriteFile(filepath.Join(docsDir, "bee-dt1.md"), []byte("# dt1 change doc\n"), 0o644)
	commitRepoAt(t, filepath.Join(root, "submodules", "alpha", "repo"),
		"impl dt1\n\nBeehive: dt1 bee-dt1.md")

	// Negative control: a DONE task in an UNCOMMITTED PLAN.md (no hive commit
	// ever touches it) with no submodule repo/ at all (no code stamp either).
	os.MkdirAll(filepath.Join(root, "submodules", "beta"), 0o755)
	writeHivePlan(t, root, "beta", "dt2", "DONE")

	subs, _, err := s.computeStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	find := func(name string) subStat {
		for _, st := range subs {
			if st.Name == name {
				return st
			}
		}
		t.Fatalf("submodule %q missing from computeStats: %+v", name, subs)
		return subStat{}
	}
	findDelivery := func(st subStat, id string) DeliveryLink {
		for _, d := range st.Deliveries {
			if d.TaskID == id {
				return d
			}
		}
		t.Fatalf("task %q missing from %s.Deliveries: %+v", id, st.Name, st.Deliveries)
		return DeliveryLink{}
	}

	dt1 := findDelivery(find("alpha"), "dt1")
	if dt1.FlipHref != "/submodule/alpha/commit/"+flipSHA {
		t.Fatalf("dt1 FlipHref = %q, want hive commit %q", dt1.FlipHref, flipSHA)
	}
	if dt1.DocHref != "/submodule/alpha/doc/bee-dt1.md" {
		t.Fatalf("dt1 DocHref = %q", dt1.DocHref)
	}

	dt2 := findDelivery(find("beta"), "dt2")
	if dt2.FlipHref != "" || dt2.DocHref != "" {
		t.Fatalf("dt2 (negative control) should have no locatable links, got %+v", dt2)
	}

	// End-to-end: /stats renders both links for dt1 and never a dead href for
	// dt2 (no error, no /submodule/beta/commit/... or doc href in the body).
	w := get(t, s, "/stats")
	if w.Code != 200 {
		t.Fatalf("stats %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, `href="/submodule/alpha/commit/`+flipSHA+`"`) {
		t.Fatalf("stats missing hive flip link:\n%s", body)
	}
	if !strings.Contains(body, `href="/submodule/alpha/doc/bee-dt1.md"`) {
		t.Fatalf("stats missing change-doc link:\n%s", body)
	}
	if strings.Contains(body, "/submodule/beta/commit/") {
		t.Fatalf("stats must never link an unlocated hive flip:\n%s", body)
	}
}

// TestBranchesDeliveryLink checks the OTHER surface the ROI names: the
// per-submodule branches view links a stamped commit's row to the hive commit
// that flipped ITS task to DONE (half a), alongside the unchanged change-doc
// link (half b).
func TestBranchesDeliveryLink(t *testing.T) {
	s, root := setup(t)

	writeHivePlan(t, root, "alpha", "dt1", "TODO")
	commitAll(t, root, "dt1-todo")
	writeHivePlan(t, root, "alpha", "dt1", "NEEDS-REVIEW")
	commitAll(t, root, "dt1-review")
	writeHivePlan(t, root, "alpha", "dt1", "DONE")
	commitAll(t, root, "dt1-done")
	flipSHA := hygGit(t, root, "rev-parse", "--short=12", "HEAD")

	commitRepoAt(t, filepath.Join(root, "submodules", "alpha", "repo"),
		"impl dt1\n\nBeehive: dt1 bee-dt1.md")

	w := get(t, s, "/submodule/alpha/branches")
	if w.Code != 200 {
		t.Fatalf("branches %d: %s", w.Code, w.Body)
	}
	if body := w.Body.String(); !strings.Contains(body, `href="/submodule/alpha/commit/`+flipSHA+`"`) {
		t.Fatalf("branches missing hive flip link for dt1:\n%s", body)
	}
}

// TestCommitView covers the delivery-traceability commit page a FlipHref
// resolves to: 200 with the commit's PLAN.md diff and metadata for a real,
// well-formed sha; 404 (never an error page) for an unresolvable-but-hex sha, a
// non-hex sha, and an unknown submodule.
func TestCommitView(t *testing.T) {
	s, root := setup(t)
	writeHivePlan(t, root, "alpha", "dt1", "TODO")
	commitAll(t, root, "dt1-todo")
	writeHivePlan(t, root, "alpha", "dt1", "DONE")
	commitAll(t, root, "dt1-done")
	sha := hygGit(t, root, "rev-parse", "--short=12", "HEAD")

	w := get(t, s, "/submodule/alpha/commit/"+sha)
	if w.Code != 200 {
		t.Fatalf("commit view %d: %s", w.Code, w.Body)
	}
	if body := w.Body.String(); !strings.Contains(body, "dt1-done") || !strings.Contains(body, "[DONE]") {
		t.Fatalf("commit view missing subject/diff:\n%s", body)
	}

	if w := get(t, s, "/submodule/alpha/commit/"+strings.Repeat("0", 12)); w.Code != http.StatusNotFound {
		t.Fatalf("unresolvable sha: got %d, want 404", w.Code)
	}
	if w := get(t, s, "/submodule/alpha/commit/zzzzzzzzzzzz"); w.Code != http.StatusNotFound {
		t.Fatalf("non-hex sha: got %d, want 404", w.Code)
	}
	if w := get(t, s, "/submodule/none/commit/"+sha); w.Code != http.StatusNotFound {
		t.Fatalf("unknown submodule: got %d, want 404", w.Code)
	}
}

// TestEditorDiffAddDelClasses confirms the chat-diff pane renders unified-diff
// rows with add/del/eq classes (styled by the design-system --diff-* tokens).
func TestEditorDiffAddDelClasses(t *testing.T) {
	s, _ := setup(t)
	rows := []editor.DiffRow{
		{Kind: "eq", HTML: template.HTML("context")},
		{Kind: "del", HTML: template.HTML("old")},
		{Kind: "add", HTML: template.HTML("new")},
	}
	out := renderTmpl(t, s, "editor_panel.html", map[string]interface{}{"ID": "e1", "File": "ROI.md", "Rows": rows})
	for _, want := range []string{`class="ln eq"`, `class="ln del"`, `class="ln add"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("editor diff missing %q:\n%s", want, out)
		}
	}
}

// TestEditorDeleteGuardConfirmButton locks the destructive-deletion UI guard
// (editor-safety-guards): when the pending proposal would wipe a human-owned
// file (DeleteRisk), the panel surfaces a warning and a DISTINCT confirm control
// that posts the explicit confirm=delete authorization plus a browser confirm
// prompt — never the plain one-click Merge. A normal dirty diff keeps the plain
// Merge button with no deletion confirmation.
func TestEditorDeleteGuardConfirmButton(t *testing.T) {
	s, _ := setup(t)

	// Protected whole-file deletion: warning + a confirm button carrying the
	// explicit confirm=delete value and an extra browser confirm prompt.
	risky := renderTmpl(t, s, "editor_panel.html", map[string]interface{}{
		"ID": "e1", "File": "ROI.md", "DeleteRisk": true,
	})
	for _, want := range []string{
		`hx-vals='{"confirm":"delete"}'`, // explicit server-side authorization
		"hx-confirm=",                    // extra browser confirmation prompt
		"human-owned",                    // the warning naming why it is blocked
		`hx-post="/editor/e1/merge"`,     // still the merge endpoint
	} {
		if !strings.Contains(risky, want) {
			t.Fatalf("delete-risk panel missing %q:\n%s", want, risky)
		}
	}

	// A normal dirty diff: plain Merge, NOT the deletion-confirm control.
	normal := renderTmpl(t, s, "editor_panel.html", map[string]interface{}{
		"ID": "e1", "File": "ROI.md",
	})
	if !strings.Contains(normal, `hx-post="/editor/e1/merge"`) {
		t.Fatalf("normal panel missing the plain merge control:\n%s", normal)
	}
	if strings.Contains(normal, `"confirm":"delete"`) || strings.Contains(normal, "human-owned") {
		t.Fatalf("normal (non-deletion) merge must not carry a delete confirmation:\n%s", normal)
	}
}

// commitAndNormalizeMain gives root an initial commit of whatever setup(t) (or
// the caller) has already written, on a branch normalized to "main" regardless
// of the host's init.defaultBranch — the precondition internal/editor.Manager.
// Open needs (it resolves a "main" ref) that plain setup(t) does not provide.
func commitAndNormalizeMain(t *testing.T, root string) {
	t.Helper()
	commitAll(t, root, "seed")
	hygGit(t, root, "branch", "-M", "main")
}

// openEditorSession drives GET /edit?path=file exactly as an "edit with AI"
// link does, and returns the opened session's id (== its worktree branch) so a
// test can simulate an agent's edit directly in that worktree.
func openEditorSession(t *testing.T, s *Server, file string) (id string) {
	t.Helper()
	w := get(t, s, "/edit?path="+file)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("open %q %d: %s", file, w.Code, w.Body)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/editor/") {
		t.Fatalf("edit-with-AI must open the publish-capable /editor surface, got redirect %q", loc)
	}
	return strings.TrimPrefix(loc, "/editor/")
}

// writeInSession overwrites file's content directly in an opened session's
// worktree — standing in for what a real agent turn would write via
// editor.Session.Chat/StartChat — so a test can drive Merge without an opencode
// backend. The worktree lives at .worktrees/<id> off root (Manager.Open's own
// layout), which the test can compute because Session.ID/Branch and root are
// both already in hand.
func writeInSession(t *testing.T, root, id, file, content string) {
	t.Helper()
	p := filepath.Join(root, ".worktrees", id, filepath.FromSlash(file))
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write in session worktree: %v", err)
	}
}

// TestEditEntryOpensPublishCapableEditor is the routing half of
// ai-edit-publish-to-main: an edit-with-AI request carrying a coordination-file
// path (?path=, exactly what every real link in the UI sends) opens through
// editNew (internal/editor.Manager) and lands on the /editor/{id} page — the
// SAME publish-capable surface merge-button-wire/publish-main-writes wired a
// real Merge button onto — never chatManager's throwaway /edit/{id} chat page.
func TestEditEntryOpensPublishCapableEditor(t *testing.T) {
	s, root := setup(t)
	commitAndNormalizeMain(t, root)

	id := openEditorSession(t, s, "submodules/alpha/"+repo.ROIFile)
	page := get(t, s, "/editor/"+id).Body.String()
	if !strings.Contains(page, "edit · submodules/alpha/"+repo.ROIFile) {
		t.Fatalf("editor page did not render for the opened session:\n%s", page)
	}
	// A stale/never-registered chatManager-style id must not resolve here —
	// this really is the internal/editor Manager's session table, not
	// chatManager's.
	if w := get(t, s, "/editor/edit-bogus-1"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown /editor id should 404, got %d", w.Code)
	}
}

// TestChatManagerEditRoutesRetired locks the "gone/redirected" half of the
// accept contract: the OLD generic per-path HTTP entry into chatManager
// (POST /edit -> chatOpen, GET /edit/{id} -> chatPage — the publish-less
// approve dead end) no longer exists. Only GET /edit remains, and it is
// editEntry (-> editNew), never chatOpen.
func TestChatManagerEditRoutesRetired(t *testing.T) {
	s, _ := setup(t)
	if w := postForm(t, s, "/edit", url.Values{"path": {"submodules/alpha/" + repo.ROIFile}}); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /edit (the retired chatManager open) should be gone, got %d: %s", w.Code, w.Body)
	}
	if w := get(t, s, "/edit/some-id"); w.Code != http.StatusNotFound {
		t.Fatalf("GET /edit/{id} (the retired chatManager page) should be gone, got %d: %s", w.Code, w.Body)
	}
}

// TestEditEntryRejectsNonCoordinationFile proves the consolidated edit-with-AI
// entry point is NOT chatManager: chatManager edited any repo path, but
// internal/editor (what editEntry now always opens) only ever touches the
// declared coordination-file allowlist. A plain repo file 400s instead of
// silently opening a session that could never publish anyway.
func TestEditEntryRejectsNonCoordinationFile(t *testing.T) {
	s, _ := setup(t)
	if w := get(t, s, "/edit?path=submodules/alpha/notes.md"); w.Code != http.StatusBadRequest {
		t.Fatalf("an arbitrary non-coordination file must be rejected, got %d: %s", w.Code, w.Body)
	}
}

// TestEditWithAIMergePublishesToOrigin is the core ai-edit-publish-to-main
// acceptance: approving an edit-with-AI session (opened exactly as the
// dashboard/explorer/roi_editor links do, via GET /edit?path=) and merging it
// through the HTTP /editor/{id}/merge endpoint lands the change on LOCAL main
// (the primary checkout's working tree advances) AND on the repo-own remote
// (origin main carries it) — never stranding it on a dangling edit-* branch.
// This is a genuine negative control: if the merge/publish step were ever
// dropped, main would stay at its old content and this test would fail — a
// green run is not just a 200 status code.
func TestEditWithAIMergePublishesToOrigin(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	s, root := setup(t)
	origin := seedRootOrigin(t, root)
	file := "submodules/alpha/" + repo.ROIFile

	id := openEditorSession(t, s, file)
	writeInSession(t, root, id, file, "# alpha\n\npublished intent\n")

	if w := postForm(t, s, "/editor/"+id+"/merge", url.Values{}); w.Code != http.StatusOK {
		t.Fatalf("merge %d: %s", w.Code, w.Body)
	}

	// Local main advanced: the primary checkout's working tree carries it.
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil || !strings.Contains(string(got), "published intent") {
		t.Fatalf("approved edit did not land on local main: content=%q err=%v", got, err)
	}
	// Pushed to the repo-own remote — not a dangling edit-* branch main (and
	// every other host's honeybees) never sees.
	if got := hygGit(t, origin, "show", "main:"+file); !strings.Contains(got, "published intent") {
		t.Fatalf("approved edit did not reach the repo-own remote main: %q", got)
	}
	assertOriginAtLocalHead(t, root, origin)
}

// TestEditWithAIMergeDeleteGuardHolds proves the editor-safety guards survive
// the new routing end to end over HTTP: a whole-file deletion of the
// human-owned ROI.md opened via the ordinary edit-with-AI entry point is
// default-BLOCKED on plain Merge (main/origin untouched) and requires the
// separate, explicit confirm=delete before it can land.
func TestEditWithAIMergeDeleteGuardHolds(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	s, root := setup(t)
	origin := seedRootOrigin(t, root)
	file := "submodules/alpha/" + repo.ROIFile

	id := openEditorSession(t, s, file)
	writeInSession(t, root, id, file, "") // wipes a non-empty base: the guarded shape

	blocked := postForm(t, s, "/editor/"+id+"/merge", url.Values{})
	if blocked.Code != http.StatusOK {
		t.Fatalf("a blocked merge still renders the panel (200 w/ Error), got %d", blocked.Code)
	}
	if !strings.Contains(blocked.Body.String(), "human-owned") {
		t.Fatalf("panel must surface the delete-guard block:\n%s", blocked.Body)
	}
	if got := hygGit(t, origin, "show", "main:"+file); !strings.Contains(got, "# alpha") {
		t.Fatalf("blocked merge must not reach origin main: %q", got)
	}

	confirmed := postForm(t, s, "/editor/"+id+"/merge", url.Values{"confirm": {"delete"}})
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirmed merge %d: %s", confirmed.Code, confirmed.Body)
	}
	if got := hygGit(t, origin, "show", "main:"+file); strings.TrimSpace(got) != "" {
		t.Fatalf("confirmed deletion should empty the file on origin main, got %q", got)
	}
	assertOriginAtLocalHead(t, root, origin)
}

// TestSessionLivenessBranchGone is the regression for sessions that kept showing
// "running" / "(waiting for session output…)" long after the honeybee exited. A
// stub whose stream branch is gone is an ended session (its finalize never
// replaced the stub), not a live one.
func TestSessionLivenessBranchGone(t *testing.T) {
	s, root := setup(t)
	ctx := context.Background()
	gitRun := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// A base commit so we have something to branch from.
	os.WriteFile(filepath.Join(root, "seed"), []byte("x"), 0o644)
	gitRun("add", "-A")
	gitRun("commit", "-q", "-m", "seed")
	// Fresh stream branch for the "live" session (tip time ~now).
	gitRun("branch", "bee-live-stream")
	// A stream branch whose tip is OLD (a running session mid-quiet-turn). Liveness
	// must come from the branch existing, not from recent transcript writes.
	cs := exec.Command("git", "commit", "-q", "--allow-empty", "-m", "stale tip")
	cs.Dir = root
	cs.Env = append(os.Environ(),
		"GIT_COMMITTER_DATE=2001-01-01T00:00:00", "GIT_AUTHOR_DATE=2001-01-01T00:00:00")
	if out, err := cs.CombinedOutput(); err != nil {
		t.Fatalf("stale commit: %v\n%s", err, out)
	}
	gitRun("branch", "bee-stale-stream")
	gitRun("reset", "-q", "--hard", "HEAD~1") // working tree back at seed

	sessDir := filepath.Join(root, "submodules", "alpha", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(sessDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("bee-live.md", repo.SessionStub("bee-live-stream"))   // branch exists, fresh -> live
	write("bee-stale.md", repo.SessionStub("bee-stale-stream")) // branch exists, OLD tip -> still live
	write("bee-dead.md", repo.SessionStub("bee-dead-stream"))   // branch gone -> NOT live
	write("bee-final.md", "# final transcript\nall done.\n")    // non-stub -> NOT live

	infos := s.sessionInfos(ctx, sessDir, time.Now())
	got := map[string]bool{}
	for _, in := range infos {
		got[in.ID] = in.Live
	}
	if !got["bee-live"] {
		t.Errorf("bee-live: want Live=true (fresh stream branch)")
	}
	if !got["bee-stale"] {
		t.Errorf("bee-stale: want Live=true (branch exists though tip is old) — the false-idle bug")
	}
	if got["bee-dead"] {
		t.Errorf("bee-dead: want Live=false (stream branch gone), got true — the orphaned-stub bug")
	}
	if got["bee-final"] {
		t.Errorf("bee-final: want Live=false (finished non-stub), got true")
	}

	// Body of the ended session must say so, not pretend it is still starting up.
	w := get(t, s, "/submodule/alpha/session/bee-dead/body")
	if w.Code != 200 {
		t.Fatalf("body status %d", w.Code)
	}
	if b := w.Body.String(); !strings.Contains(b, "session ended") {
		t.Errorf("ended-session body should explain the branch is gone, got: %q", b)
	}

	// Full session page badge must use the same liveness semantics as the list:
	// running only while the stream branch exists; ended for final transcripts and
	// orphaned stubs. This catches the old hard-coded "live" badge on every page.
	livePage := get(t, s, "/submodule/alpha/session/bee-live")
	if livePage.Code != 200 {
		t.Fatalf("live page status %d", livePage.Code)
	}
	if b := livePage.Body.String(); !strings.Contains(b, `class="badge live">running`) || strings.Contains(b, `>ended</span>`) {
		t.Errorf("live session page should show running badge, got: %q", b)
	}
	deadPage := get(t, s, "/submodule/alpha/session/bee-dead")
	if deadPage.Code != 200 {
		t.Fatalf("dead page status %d", deadPage.Code)
	}
	if b := deadPage.Body.String(); !strings.Contains(b, `class="badge">ended`) || strings.Contains(b, `>running</span>`) || strings.Contains(b, `>live</span>`) {
		t.Errorf("ended session page should show ended badge only, got: %q", b)
	}
	finalPage := get(t, s, "/submodule/alpha/session/bee-final")
	if finalPage.Code != 200 {
		t.Fatalf("final page status %d", finalPage.Code)
	}
	if b := finalPage.Body.String(); !strings.Contains(b, `class="badge">ended`) || strings.Contains(b, `>running</span>`) || strings.Contains(b, `>live</span>`) {
		t.Errorf("final session page should show ended badge only, got: %q", b)
	}
}

// TestAssetsHtmxServed locks the single-binary embed contract for htmx itself:
// the library is vendored under assets/ and served at /assets/htmx.min.js (no
// CDN), so the frontend works offline/air-gapped. It must be the real,
// version-stamped library, not a stub (htmx-polish embeds htmx, drops the unpkg
// CDN <script>).
func TestAssetsHtmxServed(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/assets/htmx.min.js")
	if w.Code != 200 {
		t.Fatalf("htmx.min.js status %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("content-type = %q, want javascript", ct)
	}
	body := w.Body.String()
	if len(body) < 10000 {
		t.Fatalf("htmx.min.js is %d bytes — too small to be the real library", len(body))
	}
	if !strings.Contains(body, "1.9.10") {
		t.Fatalf("htmx.min.js missing the expected version marker 1.9.10")
	}
}

// TestLayoutEmbedsHtmxNoCDN proves the layout loads the EMBEDDED htmx asset and
// no longer reaches out to a CDN, and that the global loading indicator + error
// toast hooks ship on every page (so swaps show a loading state and a failed
// destructive action is not silent).
func TestLayoutEmbedsHtmxNoCDN(t *testing.T) {
	s, _ := setup(t)
	page := get(t, s, "/").Body.String()
	if !strings.Contains(page, `src="/assets/htmx.min.js"`) {
		t.Fatalf("layout does not reference the embedded htmx asset:\n%s", page)
	}
	if strings.Contains(page, "unpkg.com") || strings.Contains(page, "//cdn.") {
		t.Fatalf("layout still references a CDN (must be a single-binary embed):\n%s", page)
	}
	for _, want := range []string{
		`id="htmx-progress"`, "htmx-indicator", // global loading indicator
		`id="htmx-toast"`, "htmx:responseError", // failed-request toast
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("layout missing htmx-polish hook %q:\n%s", want, page)
		}
	}
}

// TestMergePanelConfirmAndIndicator locks the destructive-merge polish: the merge
// control swaps the #merge-panel fragment with a loading indicator AND asks for
// confirmation before mergePost runs the real git merge. The handler still
// returns the merge fragment (Files: templates only; behavior unchanged).
func TestMergePanelConfirmAndIndicator(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/merge")
	if w.Code != 200 {
		t.Fatalf("merge get %d: %s", w.Code, w.Body)
	}
	b := w.Body.String()
	for _, want := range []string{
		`id="merge-panel"`,              // swappable fragment wrapper
		`hx-post="/merge"`,              // htmx swap, not a full reload
		`hx-select="#merge-panel"`,      // extract just the fragment from the page response
		`hx-indicator="#htmx-progress"`, // visible loading state on the swap
		"hx-confirm=",                   // confirm prompt on the destructive merge
	} {
		if !strings.Contains(b, want) {
			t.Fatalf("merge panel missing %q:\n%s", want, b)
		}
	}
}

// TestEnvDeployConfirmAndIndicator: switching the active environment is impactful,
// so the deploy control also confirms and shows a loading indicator. The panel is
// scoped to one submodule, so its form posts to that submodule's deploy route.
func TestEnvDeployConfirmAndIndicator(t *testing.T) {
	s, _ := setup(t)
	b := get(t, s, "/submodule/alpha/env").Body.String()
	for _, want := range []string{
		`id="env-panel"`, `hx-post="/submodule/alpha/env/deploy"`, `hx-indicator="#htmx-progress"`, "hx-confirm=",
	} {
		if !strings.Contains(b, want) {
			t.Fatalf("env panel missing %q:\n%s", want, b)
		}
	}
}

// TestRoiInlineEditAndIndicator: ROI save is an htmx fragment swap (#roi-panel)
// with a loading indicator, behind an inline-edit affordance (a <details>
// toggle), while the editable raw source stays present verbatim (round-trip).
func TestRoiInlineEditAndIndicator(t *testing.T) {
	s, _ := setup(t)
	b := get(t, s, "/roi/alpha").Body.String()
	for _, want := range []string{
		`id="roi-panel"`,
		`class="inline-edit"`, // edit-in-place affordance
		`hx-post="/roi/alpha"`,
		`hx-select="#roi-panel"`,
		`hx-indicator="#htmx-progress"`,
		"<textarea", // raw source still editable verbatim
	} {
		if !strings.Contains(b, want) {
			t.Fatalf("roi editor missing %q:\n%s", want, b)
		}
	}
}

// TestSecretsInlineEditAndIndicator: secrets get inline-edit affordances (an add
// form plus a per-key set form) that swap the #secrets-panel fragment with a
// loading indicator.
func TestSecretsInlineEditAndIndicator(t *testing.T) {
	s, _ := setup(t)
	b := get(t, s, "/secrets").Body.String()
	for _, want := range []string{
		`id="secrets-panel"`,
		`class="inline-edit"`,
		`hx-post="/secrets"`,
		`hx-select="#secrets-panel"`,
		`hx-indicator="#htmx-progress"`,
	} {
		if !strings.Contains(b, want) {
			t.Fatalf("secrets panel missing %q:\n%s", want, b)
		}
	}
}

// TestAssetsStyleHtmxPolish locks the htmx-polish CSS contract: the embedded
// stylesheet defines the indicator visibility rule, the global progress/spinner +
// toast, and the inline-edit affordance the templates rely on, with motion gated
// behind prefers-reduced-motion. Kept separate from TestAssetsStyleServed (the
// design-system contract) so each task owns its own assertions.
func TestAssetsStyleHtmxPolish(t *testing.T) {
	s, _ := setup(t)
	body := get(t, s, "/assets/style.css").Body.String()
	for _, want := range []string{
		".htmx-indicator",
		".htmx-request",
		"#htmx-progress",
		"#htmx-toast",
		"details.inline-edit",
		"prefers-reduced-motion",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("style.css missing htmx-polish rule %q", want)
		}
	}
}

// hygGit runs git in dir, failing the test on error. Used to seed cruft and to
// snapshot repo state for the read-only assertion.
func hygGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// seedHygieneCruft plants one instance of every cruft class in the beehive root
// and returns the real HEAD of the drifted submodule checkout. It seeds:
//   - two stale worktree dirs (edit-* and beehive-*) under .worktrees plus a
//     genuinely-registered live worktree and a non-matching dir (both ignored);
//   - an orphan gitlink (tracked 160000 with no .gitmodules entry);
//   - a declared submodule whose checkout HEAD differs from its recorded gitlink;
//   - an unexpected remote (alongside origin, which must not be flagged).
//
// gitlink SHAs are staged via `update-index --cacheinfo` LAST so a stray add -A
// cannot overwrite the drift gitlink with the checkout's real SHA.
func seedHygieneCruft(t *testing.T, root string) (driftHEAD string) {
	t.Helper()
	// A base commit so `git worktree add` (the live registered worktree) works.
	os.WriteFile(filepath.Join(root, "seed"), []byte("x"), 0o644)
	hygGit(t, root, "add", "-A")
	hygGit(t, root, "commit", "-q", "-m", "seed")

	// Stale worktree dirs: unregistered edit-*/beehive-* under .worktrees.
	wtDir := filepath.Join(root, ".worktrees")
	for _, n := range []string{"edit-roi-alpha-123", "beehive-1782800000-111"} {
		if err := os.MkdirAll(filepath.Join(wtDir, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A non-matching dir (must be ignored) and a genuinely registered live
	// worktree (must NOT be flagged — it has a live git worktree entry).
	os.MkdirAll(filepath.Join(wtDir, "random-keep"), 0o755)
	hygGit(t, root, "worktree", "add", "--detach", filepath.Join(wtDir, "beehive-live-1"), "HEAD")

	// A real submodule checkout whose HEAD will diverge from the recorded gitlink.
	driftRepo := filepath.Join(root, "submodules", "drift", "repo")
	os.MkdirAll(driftRepo, 0o755)
	hygGit(t, driftRepo, "init", "-q")
	hygGit(t, driftRepo, "config", "user.email", "t@t")
	hygGit(t, driftRepo, "config", "user.name", "t")
	os.WriteFile(filepath.Join(driftRepo, "f"), []byte("y"), 0o644)
	hygGit(t, driftRepo, "add", "-A")
	hygGit(t, driftRepo, "commit", "-q", "-m", "drift")
	driftHEAD = hygGit(t, driftRepo, "rev-parse", "HEAD")

	// Declare the drift submodule so its gitlink is a real submodule (drift class),
	// not an orphan.
	os.WriteFile(filepath.Join(root, ".gitmodules"), []byte(
		"[submodule \"drift\"]\n\tpath = submodules/drift/repo\n\turl = ./x\n"), 0o644)

	// Unexpected remote (origin alongside must NOT be flagged).
	hygGit(t, root, "remote", "add", "origin", "https://example.invalid/origin.git")
	hygGit(t, root, "remote", "add", "weird", "https://example.invalid/weird.git")

	// Stage gitlinks LAST: a fake recorded SHA for the drift checkout (!= real
	// HEAD => stale) and an orphan gitlink with no .gitmodules entry.
	recorded := strings.Repeat("2", 40)
	orphan := strings.Repeat("3", 40)
	hygGit(t, root, "update-index", "--add", "--cacheinfo", "160000,"+recorded+",submodules/drift/repo")
	hygGit(t, root, "update-index", "--add", "--cacheinfo", "160000,"+orphan+",submodules/orphan/worktrees/bee-x")
	return driftHEAD
}

// TestHygieneScanAllClasses seeds one instance of every cruft class and asserts
// the read-only scan reports the right per-class count and lists each item in the
// drill-down (the core of the hive-hygiene-panel requirement).
func TestHygieneScanAllClasses(t *testing.T) {
	s, root := setup(t)
	seedHygieneCruft(t, root)

	h, err := scanHygiene(context.Background(), root, s.git)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if h.Clean() {
		t.Fatal("scan reported clean despite seeded cruft")
	}
	byKey := map[string]CruftClass{}
	for _, c := range h.Classes {
		byKey[c.Key] = c
	}
	// Each class: expected count + the identifiers the drill-down must list.
	cases := []struct {
		key   string
		count int
		names []string
	}{
		{"worktrees", 2, []string{"edit-roi-alpha-123", "beehive-1782800000-111"}},
		{"gitlinks", 1, []string{"submodules/orphan/worktrees/bee-x"}},
		{"checkouts", 1, []string{"submodules/drift/repo"}},
		{"remotes", 1, []string{"weird"}},
	}
	for _, c := range cases {
		cl, ok := byKey[c.key]
		if !ok {
			t.Fatalf("class %q missing from scan", c.key)
		}
		if cl.Count() != c.count {
			t.Fatalf("class %q count = %d, want %d: %+v", c.key, cl.Count(), c.count, cl.Items)
		}
		for _, name := range c.names {
			found := false
			for _, it := range cl.Items {
				if it.Name == name {
					found = true
				}
			}
			if !found {
				t.Fatalf("class %q drill-down missing %q: %+v", c.key, name, cl.Items)
			}
		}
	}
	// The live registered worktree and the non-matching dir must NOT be flagged.
	for _, it := range byKey["worktrees"].Items {
		if it.Name == "beehive-live-1" || it.Name == "random-keep" {
			t.Fatalf("stale-worktree scan flagged a non-stale entry %q", it.Name)
		}
	}
	// origin must NOT be flagged as unexpected.
	for _, it := range byKey["remotes"].Items {
		if it.Name == "origin" {
			t.Fatal("origin was flagged as an unexpected remote")
		}
	}
}

// TestHygienePanelReadOnly drives GET /hygiene end-to-end: the page renders the
// counts, the drill-down identifiers, and the beehive-hygiene remediation
// pointer — and the handler MUTATES NOTHING (no worktree removed, no remote or
// config touched, the index and the drifted checkout left exactly as found).
func TestHygienePanelReadOnly(t *testing.T) {
	s, root := setup(t)
	driftHEAD := seedHygieneCruft(t, root)

	// Snapshot the mutable repo state before the read-only handler runs.
	lsBefore := hygGit(t, root, "ls-files", "-s")
	remotesBefore := hygGit(t, root, "remote")
	cfgBefore := hygGit(t, root, "config", "--get-regexp", "^remote\\.")
	gmBefore, _ := os.ReadFile(filepath.Join(root, ".gitmodules"))
	staleWT := filepath.Join(root, ".worktrees", "edit-roi-alpha-123")

	w := get(t, s, "/hygiene")
	if w.Code != 200 {
		t.Fatalf("hygiene %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, want := range []string{
		"hive hygiene",
		"Stale worktrees", "edit-roi-alpha-123", "beehive-1782800000-111",
		"Orphan submodule gitlinks", "submodules/orphan/worktrees/bee-x",
		"Stale submodule checkouts", "submodules/drift/repo",
		"Unexpected remotes", "weird",
		"beehive-hygiene", // the remediation skill pointer
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("hygiene page missing %q:\n%s", want, body)
		}
	}

	// Read-only: nothing the scan touched may have changed.
	if _, err := os.Stat(staleWT); err != nil {
		t.Fatalf("stale worktree dir was removed by a read-only scan: %v", err)
	}
	if got := hygGit(t, root, "ls-files", "-s"); got != lsBefore {
		t.Fatalf("index changed:\nbefore:\n%s\nafter:\n%s", lsBefore, got)
	}
	if got := hygGit(t, root, "remote"); got != remotesBefore {
		t.Fatalf("remotes changed: before %q after %q", remotesBefore, got)
	}
	if got := hygGit(t, root, "config", "--get-regexp", "^remote\\."); got != cfgBefore {
		t.Fatalf("remote config changed:\nbefore:\n%s\nafter:\n%s", cfgBefore, got)
	}
	if gm, _ := os.ReadFile(filepath.Join(root, ".gitmodules")); string(gm) != string(gmBefore) {
		t.Fatalf(".gitmodules changed:\nbefore:\n%s\nafter:\n%s", gmBefore, gm)
	}
	if got := hygGit(t, filepath.Join(root, "submodules", "drift", "repo"), "rev-parse", "HEAD"); got != driftHEAD {
		t.Fatalf("drift checkout HEAD changed: before %q after %q", driftHEAD, got)
	}
}

// TestHygieneCleanAndWidget proves the clean state and the dashboard embed: a
// fresh repo (no cruft) reports clean on /hygiene, and the dashboard carries the
// shared hygiene widget so the summary is visible without leaving the home page.
func TestHygieneCleanAndWidget(t *testing.T) {
	s, _ := setup(t)
	hp := get(t, s, "/hygiene")
	if hp.Code != 200 {
		t.Fatalf("hygiene %d: %s", hp.Code, hp.Body)
	}
	if b := hp.Body.String(); !strings.Contains(b, "clean") || !strings.Contains(b, "no git cruft detected") {
		t.Fatalf("fresh repo should report clean:\n%s", b)
	}
	dash := get(t, s, "/")
	if !strings.Contains(dash.Body.String(), `id="hygiene-widget"`) {
		t.Fatalf("dashboard does not embed the hygiene widget:\n%s", dash.Body)
	}
}

// TestComputeStats checks the git-derived honeybee-performance figures behind the
// /stats view: delivered = PLAN [DONE], sessions = transcript files, distinct
// tasks and the derived ratios — all read live, nothing stored.
func TestComputeStats(t *testing.T) {
	s, root := setup(t)
	sessDir := filepath.Join(root, "submodules", "alpha", "sessions")
	os.MkdirAll(sessDir, 0o755)
	for _, n := range []string{"bee-t1-100-1.md", "bee-t1-200-2.md", "bee-t3-300-3.md", "not-a-session.md"} {
		os.WriteFile(filepath.Join(sessDir, n), []byte("x"), 0o644)
	}
	subs, total, err := s.computeStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 {
		t.Fatalf("want 1 submodule, got %d", len(subs))
	}
	a := subs[0]
	if a.DeliveredTasks != 1 { // only t3 is DONE
		t.Errorf("deliveredTasks=%d, want 1", a.DeliveredTasks)
	}
	if a.Honeybees != 3 { // the 3 bee-*.md, not the non-session file
		t.Errorf("honeybees=%d, want 3", a.Honeybees)
	}
	if want := 100.0 / 3.0; a.DeliveredPerBeePct < want-0.01 || a.DeliveredPerBeePct > want+0.01 {
		t.Errorf("delivered/bee=%v, want ~%v", a.DeliveredPerBeePct, want)
	}
	if total.DeliveredTasks != 1 || total.Honeybees != 3 {
		t.Errorf("total=%+v, want delivered 1 honeybees 3", total)
	}
	if w := get(t, s, "/stats"); w.Code != 200 || !strings.Contains(w.Body.String(), "✅/🐝") {
		t.Fatalf("GET /stats: code=%d, ✅/🐝 present=%v", w.Code, strings.Contains(w.Body.String(), "✅/🐝"))
	}
}

// seedTrackedSubmodule stands up a bare origin seeded with a single base commit
// on the given tracked branch, clones it into submodules/<name>/repo, and
// registers the gitlink + a .gitmodules entry (branch=tracked) in the beehive
// root with an initial pointer commit. It returns the bare origin path, the
// submodule checkout dir, and the base commit SHA — the realistic branch-tracking
// setup the merge publish path operates on. Callers add feature/conflict branches
// on top. A non-"main" tracked branch also proves the tracked branch is read from
// .gitmodules, never hardcoded.
func seedTrackedSubmodule(t *testing.T, root, name, tracked string) (origin, repoDir, base string) {
	t.Helper()
	origin = filepath.Join(t.TempDir(), name+"-origin.git")
	hygGit(t, root, "init", "--bare", "-b", tracked, origin)

	seed := filepath.Join(t.TempDir(), name+"-seed")
	hygGit(t, root, "init", "-q", "-b", tracked, seed)
	hygGit(t, seed, "config", "user.email", "t@t")
	hygGit(t, seed, "config", "user.name", "t")
	os.WriteFile(filepath.Join(seed, "base.txt"), []byte("base\n"), 0o644)
	hygGit(t, seed, "add", "-A")
	hygGit(t, seed, "commit", "-q", "-m", "base")
	hygGit(t, seed, "remote", "add", "origin", origin)
	hygGit(t, seed, "push", "-q", "-u", "origin", tracked)

	repoDir = filepath.Join(root, "submodules", name, "repo")
	if err := os.MkdirAll(filepath.Dir(repoDir), 0o755); err != nil {
		t.Fatal(err)
	}
	hygGit(t, root, "clone", "-q", "-b", tracked, origin, repoDir)
	hygGit(t, repoDir, "config", "user.email", "t@t")
	hygGit(t, repoDir, "config", "user.name", "t")
	base = hygGit(t, repoDir, "rev-parse", "HEAD")

	rel := filepath.Join("submodules", name, "repo")
	os.WriteFile(filepath.Join(root, ".gitmodules"),
		[]byte("[submodule \""+rel+"\"]\n\tpath = "+rel+"\n\turl = "+origin+"\n\tbranch = "+tracked+"\n"), 0o644)
	// Stage the gitlink at the checkout's real HEAD and the .gitmodules entry, then
	// commit ONLY those so the baseline pointer records the submodule at base.
	hygGit(t, root, "update-index", "--add", "--cacheinfo", "160000,"+base+","+rel)
	hygGit(t, root, "add", ".gitmodules")
	hygGit(t, root, "commit", "-q", "-m", "register "+name)
	return origin, repoDir, base
}

// TestMergePublishes is the core of merge-button-wire: POST /merge fast-forwards
// the submodule's tracked branch, PUSHES it to origin, and ADVANCES + commits the
// beehive pointer — the three steps the old local-only merge skipped. The tracked
// branch is "trunk" (not "main") to prove it is read from .gitmodules. A second
// identical POST is a no-op success (idempotent): no spurious pointer commit, no
// origin movement.
func TestMergePublishes(t *testing.T) {
	s, root := setup(t)
	name, tracked := "merger", "trunk"
	origin, repoDir, _ := seedTrackedSubmodule(t, root, name, tracked)

	// A fast-forwardable feature branch: base + one commit, local and on origin.
	hygGit(t, repoDir, "checkout", "-q", "-b", "feature")
	os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("feat\n"), 0o644)
	hygGit(t, repoDir, "add", "-A")
	hygGit(t, repoDir, "commit", "-q", "-m", "feature work")
	featTip := hygGit(t, repoDir, "rev-parse", "HEAD")
	hygGit(t, repoDir, "push", "-q", "origin", "feature")
	hygGit(t, repoDir, "checkout", "-q", tracked)

	rel := filepath.Join("submodules", name, "repo")
	rootBefore := hygGit(t, root, "rev-parse", "HEAD")

	w := postForm(t, s, "/merge", url.Values{"name": {name}, "branch": {"feature"}})
	if w.Code != 200 {
		t.Fatalf("merge %d: %s", w.Code, w.Body)
	}

	// (a) tracked branch fast-forwarded to the feature tip in the local checkout.
	if got := hygGit(t, repoDir, "rev-parse", tracked); got != featTip {
		t.Fatalf("tracked branch not fast-forwarded: got %s want %s", got, featTip)
	}
	// (b) the merge was PUSHED: origin's tracked branch advanced to the feature tip.
	if got := hygGit(t, origin, "rev-parse", tracked); got != featTip {
		t.Fatalf("origin %s not advanced (merge not published): got %s want %s", tracked, got, featTip)
	}
	// (c) the beehive pointer advanced: a new root commit records the new gitlink.
	if got := hygGit(t, root, "rev-parse", "HEAD"); got == rootBefore {
		t.Fatalf("beehive pointer commit not created (root HEAD unchanged)")
	}
	if got := hygGit(t, root, "rev-parse", "HEAD:"+rel); got != featTip {
		t.Fatalf("pointer gitlink = %s, want feature tip %s", got, featTip)
	}

	// Idempotent: a second identical merge is a no-op success — no new pointer
	// commit and origin unmoved.
	rootAfter := hygGit(t, root, "rev-parse", "HEAD")
	w2 := postForm(t, s, "/merge", url.Values{"name": {name}, "branch": {"feature"}})
	if w2.Code != 200 {
		t.Fatalf("idempotent merge %d: %s", w2.Code, w2.Body)
	}
	if got := hygGit(t, root, "rev-parse", "HEAD"); got != rootAfter {
		t.Fatalf("idempotent merge created a spurious pointer commit: %s -> %s", rootAfter, got)
	}
	if got := hygGit(t, origin, "rev-parse", tracked); got != featTip {
		t.Fatalf("idempotent merge moved origin: got %s", got)
	}
}

// TestMergeConflict proves the destructive path is safe: a branch that conflicts
// with the tracked branch returns 409, the merge is ABORTED (the checkout is left
// clean on the tracked tip), and NOTHING is published — origin and the beehive
// pointer are untouched (no partial publish).
func TestMergeConflict(t *testing.T) {
	s, root := setup(t)
	name, tracked := "conf", "trunk"
	origin, repoDir, _ := seedTrackedSubmodule(t, root, name, tracked)

	// A conflict branch edits the same file the tracked branch will also edit, so
	// the merge can neither fast-forward nor auto-merge.
	hygGit(t, repoDir, "checkout", "-q", "-b", "conflict")
	os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("theirs\n"), 0o644)
	hygGit(t, repoDir, "add", "-A")
	hygGit(t, repoDir, "commit", "-q", "-m", "their edit")
	hygGit(t, repoDir, "push", "-q", "origin", "conflict")
	hygGit(t, repoDir, "checkout", "-q", tracked)
	os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("ours\n"), 0o644)
	hygGit(t, repoDir, "add", "-A")
	hygGit(t, repoDir, "commit", "-q", "-m", "our edit")
	hygGit(t, repoDir, "push", "-q", "origin", tracked)
	trackedTip := hygGit(t, repoDir, "rev-parse", tracked)
	originBefore := hygGit(t, origin, "rev-parse", tracked)
	rootBefore := hygGit(t, root, "rev-parse", "HEAD")

	w := postForm(t, s, "/merge", url.Values{"name": {name}, "branch": {"conflict"}})
	if w.Code != http.StatusConflict {
		t.Fatalf("merge conflict: got %d, want 409: %s", w.Code, w.Body)
	}
	// Merge aborted: the checkout is clean and still on the tracked tip.
	if out := hygGit(t, repoDir, "status", "--porcelain"); out != "" {
		t.Fatalf("submodule left mid-conflict (dirty): %q", out)
	}
	if got := hygGit(t, repoDir, "rev-parse", "HEAD"); got != trackedTip {
		t.Fatalf("tracked HEAD moved on a conflict: got %s want %s", got, trackedTip)
	}
	// Origin untouched — the failed merge published nothing.
	if got := hygGit(t, origin, "rev-parse", tracked); got != originBefore {
		t.Fatalf("origin advanced on a conflict: got %s want %s", got, originBefore)
	}
	// Beehive pointer untouched.
	if got := hygGit(t, root, "rev-parse", "HEAD"); got != rootBefore {
		t.Fatalf("beehive pointer commit created on a conflict")
	}
}

// TestTrackedBranchDefault locks the "read the tracked branch from .gitmodules,
// never hardcode main" contract directly: absent config falls back to main, and
// an explicit submodule.<path>.branch entry is honored.
func TestTrackedBranchDefault(t *testing.T) {
	s, root := setup(t)
	sm := repo.Submodule{Name: "alpha", Path: filepath.Join(root, "submodules", "alpha")}
	if got := s.trackedBranch(context.Background(), sm); got != "main" {
		t.Fatalf("no .gitmodules: tracked = %q, want main (default)", got)
	}
	rel := filepath.Join("submodules", "alpha", "repo")
	os.WriteFile(filepath.Join(root, ".gitmodules"),
		[]byte("[submodule \""+rel+"\"]\n\tpath = "+rel+"\n\turl = ./x\n\tbranch = release\n"), 0o644)
	if got := s.trackedBranch(context.Background(), sm); got != "release" {
		t.Fatalf("explicit branch: tracked = %q, want release (from .gitmodules)", got)
	}
}

// TestMergePanelSuccessAndPreselect locks the post-merge UI: the success banner
// names the merged branch and tracked branch, and the submodule dropdown
// preselects the one just merged (so the panel reflects what happened).
func TestMergePanelSuccessAndPreselect(t *testing.T) {
	s, _ := setup(t)
	out := renderTmpl(t, s, "merge_panel.html", map[string]interface{}{
		"Subs":     []repo.Submodule{{Name: "alpha"}, {Name: "beta"}},
		"Merged":   true,
		"Branch":   "bee-x",
		"Tracked":  "trunk",
		"Selected": "beta",
	})
	for _, want := range []string{`id="merge-panel"`, "bee-x", "trunk", `<option selected>beta</option>`} {
		if !strings.Contains(out, want) {
			t.Fatalf("merge success panel missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `<option selected>alpha</option>`) {
		t.Fatalf("wrong submodule preselected (alpha, not beta):\n%s", out)
	}
}

// TestBranchViewMergeControl proves the once-inert branch view now carries a live,
// submodule-scoped publish control: a form that POSTs /merge with the submodule
// name fixed, a destructive confirm, a loading indicator, and an inline result
// target for the htmx swap.
func TestBranchViewMergeControl(t *testing.T) {
	s, _ := setup(t)
	out := renderTmpl(t, s, "branch_view.html", map[string]interface{}{
		"Name": "alpha", "Sections": nil, "HasPrev": false, "HasNext": false,
	})
	for _, want := range []string{
		`hx-post="/merge"`,              // wired to the publish endpoint
		`name="name" value="alpha"`,     // scoped to THIS submodule
		`id="branch-merge-result"`,      // inline success/failure target
		"hx-confirm=",                   // destructive-action confirm
		`hx-indicator="#htmx-progress"`, // loading state on the swap
		`hx-target="#branch-merge-result"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("branch view merge control missing %q:\n%s", want, out)
		}
	}
}

// TestViewerFollowsOffBoxSessions is the consumer half of remote-host-session-
// view: a beehived that shares no filesystem with a honeybee must still show
// that agent's session. The viewer fast-forwards local main from the remote so a
// session stub + transcript another host pushed become visible; and when local
// main has diverged from the remote it must NOT merge (ff-only) — it renders the
// last good state and surfaces the stall in the pane, never authoring a merge
// commit or moving HEAD. Deterministic: a bare origin plus an off-box "producer"
// clone stand in for the other host — no network, no sleeps.
func TestViewerFollowsOffBoxSessions(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file") // clone/push over local file paths
	s, root := setup(t)
	s.pullIvl = 0 // no coalescing: every polled request actually pulls (determinism)

	gitOK := func(dir string, args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	writeAt := func(dir, rel, content string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Root beehive checkout: seed main (setup only `git init`ed it) and normalize
	// the branch name to main regardless of the host's init.defaultBranch.
	writeAt(root, "seed", "x")
	gitOK(root, "add", "-A")
	gitOK(root, "commit", "-q", "-m", "seed")
	gitOK(root, "branch", "-M", "main")

	// Bare origin = the shared remote both hosts converge on; publish root main.
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitOK(filepath.Dir(origin), "init", "-q", "--bare", "-b", "main", "origin.git")
	gitOK(root, "remote", "add", "origin", origin)
	gitOK(root, "push", "-q", "origin", "main")

	sessRel := "submodules/alpha/sessions/bee-remote.md"
	localSess := filepath.Join(root, filepath.FromSlash(sessRel))

	// Off-box honeybee: a clone of origin on "another host". It streams its
	// transcript to an isolated session branch and plants the stub on main —
	// exactly what the streaming producer does — then pushes both.
	work := t.TempDir()
	gitOK(work, "clone", "-q", origin, "producer")
	prod := filepath.Join(work, "producer")
	gitOK(prod, "config", "user.email", "b@b")
	gitOK(prod, "config", "user.name", "bee")

	// Transcript lives on the stream branch at the session path.
	gitOK(prod, "checkout", "-q", "-b", "bee-remote-stream")
	writeAt(prod, sessRel, "# session bee-remote\n\nhello from off-box\n")
	gitOK(prod, "add", "-A")
	gitOK(prod, "commit", "-q", "-m", "stream turn 1")
	gitOK(prod, "push", "-q", "origin", "bee-remote-stream")
	// Stub (naming the stream branch) lives on main at the same path.
	gitOK(prod, "checkout", "-q", "main")
	writeAt(prod, sessRel, repo.SessionStub("bee-remote-stream"))
	gitOK(prod, "add", "-A")
	gitOK(prod, "commit", "-q", "-m", "publish session stub")
	gitOK(prod, "push", "-q", "origin", "main")

	// Before any request the off-box session is invisible here: local main is
	// still at seed, so the stub is not on disk.
	if _, err := os.Stat(localSess); !os.IsNotExist(err) {
		t.Fatalf("precondition: off-box session must be absent before a follow, stat err=%v", err)
	}

	// The session pane pulls, so the stub arrives and the transcript (resolved
	// from the fetched stream branch) renders — proving the viewer followed a run
	// it never shared a filesystem with.
	wb := get(t, s, "/submodule/alpha/session/bee-remote/body")
	if wb.Code != 200 {
		t.Fatalf("body status %d: %s", wb.Code, wb.Body)
	}
	body := wb.Body.String()
	if !strings.Contains(body, "hello from off-box") {
		t.Fatalf("followed transcript not rendered from the stream branch:\n%s", body)
	}
	if !strings.Contains(body, "followed") {
		t.Fatalf("session pane missing the freshness banner:\n%s", body)
	}
	if strings.Contains(body, "pull stalled") {
		t.Fatalf("a clean fast-forward must not report a stall:\n%s", body)
	}
	if _, err := os.Stat(localSess); err != nil {
		t.Fatalf("follow did not fast-forward the stub onto local main: %v", err)
	}
	// The list view surfaces the followed session too.
	if lb := get(t, s, "/submodule/alpha/sessions/body"); !strings.Contains(lb.Body.String(), "bee-remote") {
		t.Fatalf("followed session missing from the list:\n%s", lb.Body)
	}

	// --- Divergence: local main and the remote both advance independently. ---
	// The remote gains a second off-box session...
	writeAt(prod, "submodules/alpha/sessions/bee-remote2.md", repo.SessionStub("bee-remote2-stream"))
	gitOK(prod, "add", "-A")
	gitOK(prod, "commit", "-q", "-m", "publish second session stub")
	gitOK(prod, "push", "-q", "origin", "main")
	// ...while THIS host makes a local-only main commit (e.g. a frontend edit), so
	// local main is neither ahead of nor behind the remote — it has diverged.
	writeAt(root, "local-only", "y")
	gitOK(root, "add", "-A")
	gitOK(root, "commit", "-q", "-m", "local frontend edit")

	headBefore := gitOK(root, "rev-parse", "HEAD")
	wd := get(t, s, "/submodule/alpha/sessions/body")
	if wd.Code != 200 {
		t.Fatalf("a diverged follow must still serve the pane, got %d: %s", wd.Code, wd.Body)
	}
	db := wd.Body.String()
	if !strings.Contains(db, "pull stalled") {
		t.Fatalf("a non-fast-forward divergence must surface as a stall:\n%s", db)
	}
	if strings.Contains(db, "bee-remote2") {
		t.Fatalf("the un-fast-forwardable off-box session must NOT appear locally:\n%s", db)
	}
	if got := gitOK(root, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("ff-only pull moved local HEAD on divergence: %s -> %s", headBefore, got)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("ff-only pull must never start a merge (MERGE_HEAD present): err=%v", err)
	}
}

// seedRootOrigin gives the beehive root an initial commit on a normalized "main"
// branch and a bare origin it publishes to — the two-host convergence point the
// frontend write path pushes onto. The branch is forced to main (regardless of
// the host's init.defaultBranch) so publishMain, which pushes the checkout's
// CURRENT branch, targets origin main. Returns the bare origin path.
func seedRootOrigin(t *testing.T, root string) (origin string) {
	t.Helper()
	hygGit(t, root, "add", "-A")
	hygGit(t, root, "commit", "-q", "-m", "root base")
	hygGit(t, root, "branch", "-M", "main")
	origin = filepath.Join(t.TempDir(), "root-origin.git")
	hygGit(t, root, "init", "-q", "--bare", "-b", "main", origin)
	hygGit(t, root, "remote", "add", "origin", origin)
	hygGit(t, root, "push", "-q", "-u", "origin", "main")
	return origin
}

// assertOriginAtLocalHead fails unless the bare origin's main is exactly the
// root's local HEAD — i.e. the last frontend write was actually pushed, not just
// committed locally.
func assertOriginAtLocalHead(t *testing.T, root, origin string) {
	t.Helper()
	local := hygGit(t, root, "rev-parse", "HEAD")
	if got := hygGit(t, origin, "rev-parse", "main"); got != local {
		t.Fatalf("origin main=%s not at local HEAD=%s (write not published)", got, local)
	}
}

// TestFrontendWritesReachOrigin is the core of publish-main-writes: a frontend
// write does not merely commit locally, it PUSHES to origin main so other hosts'
// honeybees (which branch off origin/main) see it. Two distinct handlers — a ROI
// edit and an env deploy — both route through the shared publishMain path; after
// each, origin main is at the local HEAD and carries the committed content.
func TestFrontendWritesReachOrigin(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	s, root := setup(t)
	origin := seedRootOrigin(t, root)

	if w := postForm(t, s, "/roi/alpha", url.Values{"body": {"# new intent\n"}}); w.Code != 200 {
		t.Fatalf("roi post %d: %s", w.Code, w.Body)
	}
	assertOriginAtLocalHead(t, root, origin)
	if got := hygGit(t, origin, "show", "main:submodules/alpha/"+repo.ROIFile); !strings.Contains(got, "new intent") {
		t.Fatalf("ROI edit did not reach origin main: %q", got)
	}

	if w := postForm(t, s, "/submodule/alpha/env/deploy", url.Values{"target": {"green"}}); w.Code != 200 {
		t.Fatalf("deploy %d: %s", w.Code, w.Body)
	}
	assertOriginAtLocalHead(t, root, origin)
	if got := hygGit(t, origin, "show", "main:submodules/alpha/"+repo.InfraFile); !strings.Contains(got, "Active: green") {
		t.Fatalf("env deploy did not reach origin main: %q", got)
	}
}

// TestFrontendWriteRetriesOnConcurrentAdvance proves the non-fast-forward retry:
// when a peer advances origin main between our last sync and our write, the push
// is rejected; publishMain fetches, merges the peer's commit in, and retries — so
// our write lands WITHOUT dropping the peer's commit (no lost write).
func TestFrontendWriteRetriesOnConcurrentAdvance(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	s, root := setup(t)
	origin := seedRootOrigin(t, root)

	// A peer on another host advances origin main out-of-band.
	peer := filepath.Join(t.TempDir(), "peer")
	hygGit(t, root, "clone", "-q", origin, peer)
	hygGit(t, peer, "config", "user.email", "p@p")
	hygGit(t, peer, "config", "user.name", "peer")
	os.WriteFile(filepath.Join(peer, "peer.txt"), []byte("peer work\n"), 0o644)
	hygGit(t, peer, "add", "-A")
	hygGit(t, peer, "commit", "-q", "-m", "peer advance")
	peerHead := hygGit(t, peer, "rev-parse", "HEAD")
	hygGit(t, peer, "push", "-q", "origin", "main")

	// Our write commits on the now-stale base, so the push is a non-ff. It must
	// still succeed by fetch+merge+retry (the ROI edit and the peer's new file
	// touch different paths, so the merge is clean).
	if w := postForm(t, s, "/roi/alpha", url.Values{"body": {"# local intent\n"}}); w.Code != 200 {
		t.Fatalf("roi post %d: %s", w.Code, w.Body)
	}

	originMain := hygGit(t, origin, "rev-parse", "main")
	if originMain == peerHead {
		t.Fatalf("our write never reached origin (still at peer head %s)", peerHead)
	}
	assertOriginAtLocalHead(t, root, origin)
	// No lost write: BOTH the peer's file and our ROI edit are on origin main.
	if got := hygGit(t, origin, "show", "main:peer.txt"); !strings.Contains(got, "peer work") {
		t.Fatalf("peer commit lost from origin main: %q", got)
	}
	if got := hygGit(t, origin, "show", "main:submodules/alpha/"+repo.ROIFile); !strings.Contains(got, "local intent") {
		t.Fatalf("local write lost from origin main: %q", got)
	}
	// The peer commit is an ANCESTOR of origin main (merged in, never clobbered).
	anc := exec.Command("git", "merge-base", "--is-ancestor", peerHead, originMain)
	anc.Dir = origin
	if err := anc.Run(); err != nil {
		t.Fatalf("peer commit %s not an ancestor of origin main %s (history clobbered)", peerHead, originMain)
	}
}

// TestFrontendWriteNoOriginCommitsLocally proves the single-host path: with no
// remote configured, a write still COMMITS locally (honeybees branch off local
// main) and does not error trying to push.
func TestFrontendWriteNoOriginCommitsLocally(t *testing.T) {
	s, root := setup(t)
	if r := hygGit(t, root, "remote"); r != "" {
		t.Fatalf("precondition: root unexpectedly has a remote %q", r)
	}
	if w := postForm(t, s, "/roi/alpha", url.Values{"body": {"# solo\n"}}); w.Code != 200 {
		t.Fatalf("roi post %d: %s", w.Code, w.Body)
	}
	// A real local commit carrying the edit landed on HEAD (no push, no error).
	if got := hygGit(t, root, "show", "HEAD:submodules/alpha/"+repo.ROIFile); !strings.Contains(got, "solo") {
		t.Fatalf("no-origin write not committed locally: %q", got)
	}
}

// --- multi-repo routing (multi-repo-web-routing) ------------------------------

// initBeehiveRepo scaffolds a standalone beehive repo root — its own git repo on
// main, a committer identity (so the write path can commit), and exactly one
// submodule (ROI.md + a stamped PLAN.md) — and returns the root. Each entry in a
// multi-repo test registry points at a SEPARATE such root, so a request routed to
// one repo can be proven to operate on that repo's submodule and never crawl into
// another's.
func initBeehiveRepo(t *testing.T, submodule string) string {
	t.Helper()
	root := t.TempDir()
	if err := repo.Init(root); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][]string{{"user.email", "t@t"}, {"user.name", "t"}} {
		c := exec.Command("git", "config", kv[0], kv[1])
		c.Dir = root
		if err := c.Run(); err != nil {
			t.Fatalf("git config %v: %v", kv, err)
		}
	}
	sm := filepath.Join(root, "submodules", submodule)
	if err := os.MkdirAll(sm, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sm, repo.ROIFile), []byte("# "+submodule+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sm, repo.PlanFile),
		[]byte("<!-- Beehive-ROI: stamp-"+submodule+" -->\n# Plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// twoRepoRegistry wires the multi-repo frontend over two standalone beehive repos
// with DISTINCT per-repo keyrings: registry "alpha" -> submodule "redteam", and
// registry "bravo" -> submodule "bluecrew". A hermetic BEEHIVE_CONFIG_DIR keeps
// config.Resolve off any host/user config. It returns the wired container (whose
// default active repo is the sorted-first name, "alpha") and both roots.
func twoRepoRegistry(t *testing.T) (s *Server, rootAlpha, rootBravo string) {
	t.Helper()
	t.Setenv("BEEHIVE_CONFIG_DIR", t.TempDir())
	rootAlpha = initBeehiveRepo(t, "redteam")
	rootBravo = initBeehiveRepo(t, "bluecrew")
	reg := config.Registry{Repos: []config.RepoEntry{
		{Name: "alpha", Root: rootAlpha, GPGHome: filepath.Join(rootAlpha, "gnupg"), GPGRecipient: "alpha@example.com"},
		{Name: "bravo", Root: rootBravo, GPGHome: filepath.Join(rootBravo, "gnupg"), GPGRecipient: "bravo@example.com"},
	}}
	if err := reg.Validate(); err != nil {
		t.Fatalf("fixture registry invalid: %v", err)
	}
	s, err := NewRegistry(reg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return s, rootAlpha, rootBravo
}

// getRepo issues GET path against h, optionally carrying the repo-selection cookie
// (an empty repoName sends no cookie, exercising the default active repo).
func getRepo(t *testing.T, h http.Handler, path, repoName string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	if repoName != "" {
		req.AddCookie(&http.Cookie{Name: repoCookie, Value: repoName})
	}
	h.ServeHTTP(w, req)
	return w
}

// postRepo issues a form POST against h, optionally carrying the selection cookie.
func postRepo(t *testing.T, h http.Handler, path, repoName string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if repoName != "" {
		req.AddCookie(&http.Cookie{Name: repoCookie, Value: repoName})
	}
	h.ServeHTTP(w, req)
	return w
}

// TestNewRegistryEmptyErrors: an empty registry has no repo to serve and must be
// a construction error (never a nil-serving daemon). ResolveRegistry never hands
// NewRegistry an empty set, but the guard is explicit.
func TestNewRegistryEmptyErrors(t *testing.T) {
	if _, err := NewRegistry(config.Registry{}); err == nil {
		t.Fatal("NewRegistry(empty) should error, got nil")
	}
}

// TestNewRegistrySingleEntryFlatRoutes locks the no-regression path: a one-entry
// registry is NOT multi — it keeps today's flat routes, exposes no /repo switch,
// and serves its single repo exactly like New. (Byte-identity of the projected
// config is the config layer's TestSingleEntryRegistryRoundTrip.)
func TestNewRegistrySingleEntryFlatRoutes(t *testing.T) {
	t.Setenv("BEEHIVE_CONFIG_DIR", t.TempDir())
	root := initBeehiveRepo(t, "onlysub")
	reg := config.Registry{Repos: []config.RepoEntry{
		{Name: "solo", Root: root, GPGHome: filepath.Join(root, "gnupg"), GPGRecipient: "solo@example.com"},
	}}
	s, err := NewRegistry(reg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if s.multi() {
		t.Fatal("single-entry registry must not be multi")
	}
	mux := s.Routes()
	// Flat routes still serve the one repo.
	if w := getRepo(t, mux, "/", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "onlysub") {
		t.Fatalf("single-entry dashboard %d: %s", w.Code, w.Body)
	}
	// No /repo switch endpoint in single-repo mode.
	if w := postRepo(t, mux, "/repo/solo", "", url.Values{}); w.Code != http.StatusNotFound {
		t.Fatalf("single-repo mode must not register /repo (got %d)", w.Code)
	}
}

// TestRegistryDefaultActiveSortedFirstOwnKeyring proves selection defaults to the
// sorted-first registry name and that an UNKNOWN cookie falls back to it (never a
// nil or arbitrary repo), and that each per-repo server carries its OWN keyring —
// the web-layer half of the per-repo isolation the daemon must preserve.
func TestRegistryDefaultActiveSortedFirstOwnKeyring(t *testing.T) {
	s, _, _ := twoRepoRegistry(t)
	if !s.multi() {
		t.Fatal("two-entry registry must be multi")
	}
	if s.order[0] != "alpha" || s.name != "alpha" {
		t.Fatalf("default active = %q (order[0]=%q), want alpha (sorted first)", s.name, s.order[0])
	}
	// No cookie -> the default active repo (the container itself).
	if got := s.active(httptest.NewRequest("GET", "/", nil)); got != s {
		t.Fatal("no-cookie request did not resolve to the default active repo")
	}
	// An unknown/unregistered cookie also falls back to the default, never nil.
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: repoCookie, Value: "ghost"})
	if got := s.active(req); got != s {
		t.Fatal("unknown cookie must fall back to the default active repo")
	}
	// Per-repo keyring isolation: each server owns its own GPGHome + recipient,
	// and the two never coincide (no shared keyring across routed repos).
	a, b := s.siblings["alpha"], s.siblings["bravo"]
	if a.cfg.GPGRecipient != "alpha@example.com" || b.cfg.GPGRecipient != "bravo@example.com" {
		t.Fatalf("keyring recipients not per-repo: a=%q b=%q", a.cfg.GPGRecipient, b.cfg.GPGRecipient)
	}
	if a.cfg.GPGHome == b.cfg.GPGHome || a.cfg.GPGRecipient == b.cfg.GPGRecipient {
		t.Fatalf("routed repos must not share a keyring: a=%q/%q b=%q/%q",
			a.cfg.GPGHome, a.cfg.GPGRecipient, b.cfg.GPGHome, b.cfg.GPGRecipient)
	}
}

// TestRegistryReadRoutingByCookie proves a read is dispatched to the repo named by
// the selection cookie: the dashboard shows the ACTIVE repo's submodule and never
// the other's. The default (no cookie) is the sorted-first repo.
func TestRegistryReadRoutingByCookie(t *testing.T) {
	s, _, _ := twoRepoRegistry(t)
	mux := s.Routes()

	def := getRepo(t, mux, "/", "").Body.String()
	if !strings.Contains(def, "redteam") || strings.Contains(def, "bluecrew") {
		t.Fatalf("default dashboard must show alpha's submodule only:\n%s", def)
	}
	alpha := getRepo(t, mux, "/", "alpha").Body.String()
	if !strings.Contains(alpha, "redteam") || strings.Contains(alpha, "bluecrew") {
		t.Fatalf("alpha dashboard must show redteam only:\n%s", alpha)
	}
	bravo := getRepo(t, mux, "/", "bravo").Body.String()
	if !strings.Contains(bravo, "bluecrew") || strings.Contains(bravo, "redteam") {
		t.Fatalf("bravo dashboard must show bluecrew only (routing leaked):\n%s", bravo)
	}
}

// TestRegistryWriteRoutingAndCrossRepo404 proves a write lands in the ACTIVE
// repo's submodule and that a submodule belonging to ANOTHER registered repo is
// unreachable (404) — the write path never crawls across repos. With bravo
// selected the same name resolves and the write reaches bravo's root.
func TestRegistryWriteRoutingAndCrossRepo404(t *testing.T) {
	s, rootAlpha, rootBravo := twoRepoRegistry(t)
	mux := s.Routes()

	// Default active = alpha: writing alpha's submodule succeeds and lands in rootAlpha.
	if w := postRepo(t, mux, "/roi/redteam", "", url.Values{"body": {"# red\n"}}); w.Code != 200 {
		t.Fatalf("write to active repo submodule %d: %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(rootAlpha, "submodules", "redteam", repo.ROIFile)); string(b) != "# red\n" {
		t.Fatalf("active-repo write not persisted to rootAlpha: %q", b)
	}
	// bluecrew belongs to bravo; with alpha active it must 404 (no cross-repo crawl)
	// and bravo's file must be untouched.
	if w := postRepo(t, mux, "/roi/bluecrew", "", url.Values{"body": {"# leak\n"}}); w.Code != http.StatusNotFound {
		t.Fatalf("cross-repo submodule must 404 under the active repo, got %d", w.Code)
	}
	if b, _ := os.ReadFile(filepath.Join(rootBravo, "submodules", "bluecrew", repo.ROIFile)); string(b) != "# bluecrew\n" {
		t.Fatalf("cross-repo write leaked into rootBravo: %q", b)
	}
	// Select bravo: now bluecrew resolves and the write reaches rootBravo.
	if w := postRepo(t, mux, "/roi/bluecrew", "bravo", url.Values{"body": {"# blue\n"}}); w.Code != 200 {
		t.Fatalf("write to bravo submodule %d: %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(rootBravo, "submodules", "bluecrew", repo.ROIFile)); string(b) != "# blue\n" {
		t.Fatalf("bravo write not persisted to rootBravo: %q", b)
	}
}

// TestRegistryRepoSwitch locks the switch endpoint: POST /repo/{name} for a
// REGISTERED repo sets the selection cookie and redirects, while a switch to an
// unregistered handle is refused with 404 (an arbitrary name is never trusted).
func TestRegistryRepoSwitch(t *testing.T) {
	s, _, _ := twoRepoRegistry(t)
	mux := s.Routes()

	w := postRepo(t, mux, "/repo/bravo", "", url.Values{})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("switch to bravo: got %d, want 303", w.Code)
	}
	var set *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == repoCookie {
			set = c
		}
	}
	if set == nil || set.Value != "bravo" {
		t.Fatalf("switch did not set %s=bravo cookie: %+v", repoCookie, w.Result().Cookies())
	}
	if u := postRepo(t, mux, "/repo/ghost", "", url.Values{}); u.Code != http.StatusNotFound {
		t.Fatalf("switch to unregistered repo must 404, got %d", u.Code)
	}
}

// TestRegistryConcurrentNoLeak is the core concurrency guarantee: selection lives
// entirely in the per-request cookie, never a shared server field, so many
// simultaneous requests each resolve their OWN active repo. Under -race, if a
// selection ever leaked across requests a response would show the wrong repo's
// submodule and the test fails.
func TestRegistryConcurrentNoLeak(t *testing.T) {
	s, _, _ := twoRepoRegistry(t)
	mux := s.Routes()

	cases := []struct{ cookie, want, notWant string }{
		{"alpha", "redteam", "bluecrew"},
		{"bravo", "bluecrew", "redteam"},
	}
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		c := cases[i%len(cases)]
		wg.Add(1)
		go func(cookie, want, notWant string) {
			defer wg.Done()
			body := getRepo(t, mux, "/", cookie).Body.String()
			if !strings.Contains(body, want) || strings.Contains(body, notWant) {
				t.Errorf("cookie %q routed wrong: want %q, must not contain %q:\n%s", cookie, want, notWant, body)
			}
		}(c.cookie, c.want, c.notWant)
	}
	wg.Wait()
}

// newGPGKeyring generates a self-contained gpg keyring in home with its OWN
// keypair (recipient email); it skips the test when gpg is unavailable or key
// generation fails. Two such keyrings share NO key material, so a document
// encrypted to one can never be decrypted by the other — the substrate for the
// cross-repo isolation proof.
func newGPGKeyring(t *testing.T, home, recipient string) {
	t.Helper()
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg not installed")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	os.Chmod(home, 0o700)
	batch := "Key-Type: RSA\nKey-Length: 2048\nName-Real: bh\nName-Email: " +
		recipient + "\nExpire-Date: 0\n%no-protection\n%commit\n"
	cmd := exec.Command("gpg", "--homedir", home, "--batch", "--gen-key")
	cmd.Stdin = strings.NewReader(batch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("gpg gen-key failed: %v: %s", err, out)
	}
}

// twoRealKeyringRegistry wires a two-repo frontend whose entries carry REAL,
// distinct gpg keyrings (unlike twoRepoRegistry, which only sets keyring paths for
// routing tests). It is the fixture for the end-to-end secret-isolation proof.
func twoRealKeyringRegistry(t *testing.T) (s *Server, rootAlpha, rootBravo string) {
	t.Helper()
	t.Setenv("BEEHIVE_CONFIG_DIR", t.TempDir())
	rootAlpha = initBeehiveRepo(t, "redteam")
	rootBravo = initBeehiveRepo(t, "bluecrew")
	// Keep the keyrings OUTSIDE the repo roots so publishMain's `git add -A` never
	// stages gpg keyring/agent files, and each repo's SECRETS.yaml.gpg is the only
	// secret artifact under its root.
	keyDir := t.TempDir()
	homeA := filepath.Join(keyDir, "alpha")
	homeB := filepath.Join(keyDir, "bravo")
	rcptA, rcptB := "alpha@example.com", "bravo@example.com"
	newGPGKeyring(t, homeA, rcptA)
	newGPGKeyring(t, homeB, rcptB)
	reg := config.Registry{Repos: []config.RepoEntry{
		{Name: "alpha", Root: rootAlpha, GPGHome: homeA, GPGRecipient: rcptA},
		{Name: "bravo", Root: rootBravo, GPGHome: homeB, GPGRecipient: rcptB},
	}}
	if err := reg.Validate(); err != nil {
		t.Fatalf("fixture registry invalid: %v", err)
	}
	s, err := NewRegistry(reg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return s, rootAlpha, rootBravo
}

// TestRegistrySecretKeyringIsolation is the acceptance isolation proof: with two
// registered repos and two real gpg homes, a secret written through repo alpha's
// handler is encrypted to alpha's OWN keyring, alpha can read it back, and repo
// bravo's keyring CANNOT decrypt alpha's SECRETS.yaml.gpg. The write also never
// touches bravo's root — the handler uses the active repo's keyring only.
func TestRegistrySecretKeyringIsolation(t *testing.T) {
	s, rootAlpha, rootBravo := twoRealKeyringRegistry(t)
	mux := s.Routes()
	ctx := context.Background()

	// Write a secret via alpha's handler (alpha is the default active repo).
	if w := postRepo(t, mux, "/secrets", "alpha", url.Values{"key": {"db_pw"}, "value": {"hunter2"}}); w.Code != 200 {
		t.Fatalf("alpha secret write: %d %s", w.Code, w.Body)
	}
	alphaPath := filepath.Join(rootAlpha, repo.SecretsFile)
	if _, err := os.Stat(alphaPath); err != nil {
		t.Fatalf("alpha SECRETS.yaml.gpg not written: %v", err)
	}
	homeA := s.siblings["alpha"].cfg.GPGHome
	homeB := s.siblings["bravo"].cfg.GPGHome
	if homeA == homeB {
		t.Fatal("fixture bug: alpha and bravo share a keyring home")
	}

	// Alpha's OWN keyring reads its OWN secret's key back.
	keys, err := listSecretKeys(ctx, homeA, alphaPath)
	if err != nil {
		t.Fatalf("alpha keyring must read its own secret: %v", err)
	}
	if len(keys) != 1 || keys[0] != "db_pw" {
		t.Fatalf("alpha keys = %v, want [db_pw]", keys)
	}

	// THE PROOF: bravo's keyring CANNOT decrypt alpha's SECRETS.yaml.gpg. A nil
	// error here would mean bravo could read alpha's secrets — isolation broken.
	if _, err := listSecretKeys(ctx, homeB, alphaPath); err == nil {
		t.Fatal("SECURITY: bravo's keyring decrypted alpha's secrets — per-repo isolation broken")
	}

	// The write landed only in alpha's root; bravo's has no secrets file.
	if _, err := os.Stat(filepath.Join(rootBravo, repo.SecretsFile)); !os.IsNotExist(err) {
		t.Fatalf("alpha secret write leaked into bravo root (stat err=%v)", err)
	}

	// Symmetric direction: bravo's handler writes under bravo's keyring, and alpha
	// cannot decrypt it either.
	if w := postRepo(t, mux, "/secrets", "bravo", url.Values{"key": {"api_key"}, "value": {"s3cr3t"}}); w.Code != 200 {
		t.Fatalf("bravo secret write: %d %s", w.Code, w.Body)
	}
	bravoPath := filepath.Join(rootBravo, repo.SecretsFile)
	if bkeys, err := listSecretKeys(ctx, homeB, bravoPath); err != nil || len(bkeys) != 1 || bkeys[0] != "api_key" {
		t.Fatalf("bravo keyring must read its own secret: keys=%v err=%v", bkeys, err)
	}
	if _, err := listSecretKeys(ctx, homeA, bravoPath); err == nil {
		t.Fatal("SECURITY: alpha's keyring decrypted bravo's secrets — per-repo isolation broken")
	}
}

// TestSecretsNoKeyringFailsLoudly confirms the web secrets primitives refuse to
// operate with no keyring configured (empty gpgHome) instead of silently falling
// through to gpg's process-default keyring — the "no shared-keyring fallback"
// rule, enforced independent of whether gpg is installed.
func TestSecretsNoKeyringFailsLoudly(t *testing.T) {
	ctx := context.Background()
	// A present, non-empty ciphertext file so listSecretKeys does not take the
	// missing-file fast path before the keyring guard.
	p := filepath.Join(t.TempDir(), repo.SecretsFile)
	if err := os.WriteFile(p, []byte("ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listSecretKeys(ctx, "", p); err == nil {
		t.Fatal("listSecretKeys with empty gpgHome must fail loudly")
	}
	if err := setSecret(ctx, "", p, "x@example.com", "k", "v"); err == nil {
		t.Fatal("setSecret with empty gpgHome must fail loudly")
	}
}

// TestSessionTags locks the stateless session->tag accessor (stats-tag-model): the
// built-in tags {submodule,kind,branch,model} are derived from git ALONE — the
// transcript header, cross-checked against the file name, reusing the SAME
// audit.ParseFile the audit engine uses — and config-declared tags layer on by
// facet value. It also pins the two behaviours the by-model stats view depends on:
// an absent model header OMITS `model` (the accessor never guesses a default), and
// a config tag keyed on an absent facet value simply does not attach.
func TestSessionTags(t *testing.T) {
	s, root := setup(t)
	const opus = "github-copilot/claude-opus-4.8"

	// Config tags keyed off two DIFFERENT built-in facets, proving the schema is
	// open (arbitrary facet + label keys, no fixed set): a cohort keyed on the
	// submodule facet, a tier keyed on the model facet. Set white-box (same
	// package) — the layering itself is covered in config's own test.
	s.cfg.Tags = map[string]map[string]map[string]string{
		"submodule": {"alpha": {"cohort": "A"}},
		"model":     {opus: {"tier": "frontier"}},
	}
	sessDir := filepath.Join(root, "submodules", "alpha", "sessions")

	// Fully-stamped session: header carries submodule/kind/branch + model and the
	// file name agrees with the branch, so every built-in derives and both config
	// tags attach.
	writeTranscript(t, root, "bee-tagme-100-1", "work", opus)
	got := s.sessionTags(sessionRef{submodule: "alpha", path: filepath.Join(sessDir, "bee-tagme-100-1.md")})
	want := map[string]string{
		"submodule": "alpha",
		"kind":      "work",
		"branch":    "bee-tagme",
		"model":     opus,
		"cohort":    "A",        // config tag keyed on submodule=alpha
		"tier":      "frontier", // config tag keyed on model=opus
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("full session tags:\n got %v\nwant %v", got, want)
	}

	// Negative control: no model header. `model` is omitted (NOT defaulted — that
	// policy lives in computeStats, not the accessor), and so is the tier tag
	// keyed on it; the submodule-keyed cohort still attaches.
	writeTranscript(t, root, "bee-nomodel-200-2", "review", "")
	got = s.sessionTags(sessionRef{submodule: "alpha", path: filepath.Join(sessDir, "bee-nomodel-200-2.md")})
	want = map[string]string{
		"submodule": "alpha",
		"kind":      "review",
		"branch":    "bee-nomodel",
		"cohort":    "A",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("no-model session tags:\n got %v\nwant %v", got, want)
	}
	if _, ok := got["model"]; ok {
		t.Fatal("model must be OMITTED when the header carries none (no default in the accessor)")
	}
	if _, ok := got["tier"]; ok {
		t.Fatal("a config tag keyed on an absent facet value must not attach")
	}

	// Stateless: a malformed/headerless transcript that fails the audit parse
	// derives no built-ins and attaches no facet-keyed config tags (empty set, no
	// error/panic) — the leniency the rest of /stats relies on.
	if err := os.WriteFile(filepath.Join(sessDir, "bee-bad-300-3.md"), []byte("no header here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s.sessionTags(sessionRef{submodule: "alpha", path: filepath.Join(sessDir, "bee-bad-300-3.md")}); len(got) != 0 {
		t.Fatalf("unparsable transcript must yield no tags, got %v", got)
	}
}

// writeSubTranscript is writeTranscript generalized to an ARBITRARY submodule
// name (writeTranscript itself is hardwired to "alpha") — stats-filter-groupby
// needs multiple submodules in scope at once so `group-by=submodule` and a
// `filter=submodule=...` chip have more than one distinct value to prove out.
func writeSubTranscript(t *testing.T, root, sub, stem, kind, model string) {
	t.Helper()
	dir := filepath.Join(root, "submodules", sub, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	branch := stem
	if m := sessionNameRE.FindStringSubmatch(stem); m != nil {
		branch = "bee-" + m[1]
	}
	tag := ""
	if model != "" {
		tag = " · model: " + model
	}
	body := "# session " + stem + "\n\nsubmodule: " + sub + " · kind: " + kind +
		" · branch: " + branch + tag + "\n\n## turn 1\nwork\n"
	if err := os.WriteFile(filepath.Join(dir, stem+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeStatsPlan overwrites sub's PLAN.md with one task per (id, status) pair
// — writeHivePlan's single-task form doesn't fit stats-filter-groupby's tests,
// which need several DONE/TODO tasks in the same submodule at once.
func writeStatsPlan(t *testing.T, root, sub string, tasks [][2]string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("<!-- Beehive-ROI: abc123 -->\n# Plan\n\n")
	for _, kv := range tasks {
		b.WriteString("## " + kv[0] + " [" + kv[1] + "] <!-- attempts=0 deps= weight=1 -->\nbuild it\nDoc: bee-" + kv[0] + ".md\n\n")
	}
	dir := filepath.Join(root, "submodules", sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, repo.PlanFile), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStatsGroupedFilterAndGroupByModel is stats-filter-groupby's core
// acceptance case: GET /stats?filter=kind=review&group-by=model computes each
// metric over ONLY kind=review sessions, one row per distinct model. A
// kind=work session on the same submodule (opus) must NOT be counted, proving
// the filter — not just the group-by — actually restricts the session pool.
func TestStatsGroupedFilterAndGroupByModel(t *testing.T) {
	s, root := setup(t)
	const opus = "github-copilot/claude-opus-4.8"
	const sonnet = "github-copilot/claude-sonnet-5"

	writeStatsPlan(t, root, "alpha", [][2]string{{"r1", "DONE"}, {"r2", "DONE"}, {"w1", "TODO"}})
	writeSubTranscript(t, root, "alpha", "bee-r1-100-1", "review", opus)
	writeSubTranscript(t, root, "alpha", "bee-r2-200-2", "review", sonnet)
	// Same submodule, kind=work: must be excluded by filter=kind=review even
	// though it shares the opus model with r1.
	writeSubTranscript(t, root, "alpha", "bee-w1-300-3", "work", opus)

	filters := []tagFilter{{Key: "kind", Value: "review"}}
	rows, err := s.computeGroupedStats(context.Background(), filters, []string{"model"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows (opus, sonnet), got %d: %+v", len(rows), rows)
	}
	byModel := map[string]groupStat{}
	for _, row := range rows {
		byModel[row.Values[0]] = row
	}
	if got := byModel[opus]; got.Honeybees != 1 || got.DeliveredTasks != 1 {
		t.Fatalf("opus row: %+v, want Honeybees=1 Delivered=1 (the work session must be filtered out)", got)
	}
	if got := byModel[sonnet]; got.Honeybees != 1 || got.DeliveredTasks != 1 {
		t.Fatalf("sonnet row: %+v, want Honeybees=1 Delivered=1", got)
	}
	if got := byModel[opus].DeliveredPerBeePct; got != 100 {
		t.Fatalf("opus yield: got %.1f want 100", got)
	}

	// End-to-end over the real handler + template.
	w := get(t, s, "/stats?filter=kind=review&group-by=model")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /stats?filter=kind=review&group-by=model: code=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{opus, sonnet, "kind=review"} {
		if !strings.Contains(body, want) {
			t.Fatalf("response missing %q:\n%s", want, body)
		}
	}
}

// TestStatsGroupedTwoFilterAND checks that two filter chips AND (never OR):
// filter=kind=review&filter=submodule=alpha must intersect to alpha's review
// sessions only — excluding alpha's work session AND beta's review session,
// each of which matches only ONE of the two chips.
func TestStatsGroupedTwoFilterAND(t *testing.T) {
	s, root := setup(t)
	const opus = "github-copilot/claude-opus-4.8"

	writeStatsPlan(t, root, "alpha", [][2]string{{"a-review", "DONE"}, {"a-work", "TODO"}})
	writeSubTranscript(t, root, "alpha", "bee-a-review-100-1", "review", opus)
	writeSubTranscript(t, root, "alpha", "bee-a-work-200-2", "work", opus) // matches submodule=alpha but NOT kind=review

	writeStatsPlan(t, root, "beta", [][2]string{{"b-review", "DONE"}})
	writeSubTranscript(t, root, "beta", "bee-b-review-300-3", "review", opus) // matches kind=review but NOT submodule=alpha

	filters := []tagFilter{{Key: "kind", Value: "review"}, {Key: "submodule", Value: "alpha"}}
	rows, err := s.computeGroupedStats(context.Background(), filters, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want exactly 1 aggregate row (no group-by), got %d: %+v", len(rows), rows)
	}
	if got := rows[0]; got.Honeybees != 1 || got.DeliveredTasks != 1 {
		t.Fatalf("AND-filtered row: %+v, want Honeybees=1 Delivered=1 (only alpha's review session)", got)
	}
}

// TestStatsGroupedTwoKeyGroupBy checks group-by over MULTIPLE keys
// (model,submodule): one row per distinct (model, submodule) TUPLE, not one
// row per key.
func TestStatsGroupedTwoKeyGroupBy(t *testing.T) {
	s, root := setup(t)
	const opus = "github-copilot/claude-opus-4.8"
	const sonnet = "github-copilot/claude-sonnet-5"

	writeStatsPlan(t, root, "alpha", [][2]string{{"t1", "DONE"}})
	writeSubTranscript(t, root, "alpha", "bee-t1-100-1", "work", opus)
	writeStatsPlan(t, root, "beta", [][2]string{{"t2", "DONE"}, {"t3", "TODO"}})
	writeSubTranscript(t, root, "beta", "bee-t2-200-2", "work", opus)
	writeSubTranscript(t, root, "beta", "bee-t3-300-3", "work", sonnet)

	rows, err := s.computeGroupedStats(context.Background(), nil, []string{"model", "submodule"})
	if err != nil {
		t.Fatal(err)
	}
	// 3 distinct (model, submodule) tuples: (opus,alpha) (opus,beta) (sonnet,beta).
	if len(rows) != 3 {
		t.Fatalf("want 3 tuples, got %d: %+v", len(rows), rows)
	}
	tuples := map[string]groupStat{}
	for _, row := range rows {
		tuples[strings.Join(row.Values, "/")] = row
	}
	if got := tuples[opus+"/alpha"]; got.Honeybees != 1 || got.DeliveredTasks != 1 {
		t.Fatalf("opus/alpha: %+v", got)
	}
	if got := tuples[opus+"/beta"]; got.Honeybees != 1 || got.DeliveredTasks != 1 {
		t.Fatalf("opus/beta: %+v", got)
	}
	if got := tuples[sonnet+"/beta"]; got.Honeybees != 1 || got.DeliveredTasks != 0 {
		t.Fatalf("sonnet/beta: %+v, want Delivered=0 (t3 is still TODO)", got)
	}

	w := get(t, s, "/stats?group-by=model,submodule")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /stats?group-by=model,submodule: code=%d body=%s", w.Code, w.Body.String())
	}
}

// TestStatsDefaultUnchanged locks stats-filter-groupby's "empty filter set + no
// group-by == today's default view (unchanged)" contract: with no query
// params, /stats must still serve the exact pre-existing per-submodule+total
// view (computeStats), not the new grouped aggregation.
func TestStatsDefaultUnchanged(t *testing.T) {
	s, root := setup(t)
	sessDir := filepath.Join(root, "submodules", "alpha", "sessions")
	os.MkdirAll(sessDir, 0o755)
	for _, n := range []string{"bee-t1-100-1.md", "bee-t1-200-2.md", "bee-t3-300-3.md"} {
		os.WriteFile(filepath.Join(sessDir, n), []byte("x"), 0o644)
	}
	subs, total, err := s.computeStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	w := get(t, s, "/stats")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /stats: code=%d", w.Code)
	}
	body := w.Body.String()
	// The pre-existing per-submodule table (delivered/honeybees exactly as
	// computeStats reports) must still render.
	if !strings.Contains(body, fmt.Sprintf(">%d<", subs[0].DeliveredTasks)) {
		t.Fatalf("default view missing subs[0].DeliveredTasks=%d:\n%s", subs[0].DeliveredTasks, body)
	}
	if !strings.Contains(body, fmt.Sprintf("<b>%d</b>", total.Honeybees)) {
		t.Fatalf("default view missing total honeybees=%d:\n%s", total.Honeybees, body)
	}
	// The FILTER BAR always renders (it is the entry point for filtering), but
	// with zero active chips, and the grouped-view's empty-result copy must be
	// ABSENT (that only ever renders once a filter/group-by is actually active).
	if !strings.Contains(body, "no filters") {
		t.Fatalf("filter bar must render even in the default view:\n%s", body)
	}
	if strings.Contains(body, "no sessions match every filter") {
		t.Fatalf("grouped-view empty-state copy must not render in the default view:\n%s", body)
	}
}

// TestStatsGroupedUnknownTagFilter is stats-filter-groupby's negative control:
// an unknown tag=value filter must yield an EMPTY result set with NO error —
// never a 500, never a Go error return.
func TestStatsGroupedUnknownTagFilter(t *testing.T) {
	s, root := setup(t)
	const opus = "github-copilot/claude-opus-4.8"
	writeStatsPlan(t, root, "alpha", [][2]string{{"t1", "DONE"}})
	writeSubTranscript(t, root, "alpha", "bee-t1-100-1", "work", opus)

	rows, err := s.computeGroupedStats(context.Background(), []tagFilter{{Key: "submodule", Value: "doesnotexist"}}, []string{"model"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("unknown submodule filter must yield zero rows, got %+v", rows)
	}
	// Same for an unknown KEY entirely (not just an unknown value).
	rows, err = s.computeGroupedStats(context.Background(), []tagFilter{{Key: "no-such-tag", Value: "x"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("unknown tag key must yield zero rows, got %+v", rows)
	}

	w := get(t, s, "/stats?filter=submodule=doesnotexist")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /stats?filter=submodule=doesnotexist: code=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no sessions match every filter") {
		t.Fatalf("empty grouped result must render its no-match copy, not an error:\n%s", w.Body.String())
	}
}

// TestParseFiltersAndGroupBy unit-tests the query-param parsing directly: a
// malformed filter chip (no `=`) is dropped rather than erroring, and
// group-by accepts EITHER the canonical single comma-separated param or
// repeated params (a plain HTML checkbox group's shape), de-duplicated.
func TestParseFiltersAndGroupBy(t *testing.T) {
	r := httptest.NewRequest("GET", "/stats?filter=kind=review&filter=malformed&filter=submodule=alpha", nil)
	got := parseFilters(r)
	want := []tagFilter{{Key: "kind", Value: "review"}, {Key: "submodule", Value: "alpha"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseFilters: got %+v want %+v", got, want)
	}

	r = httptest.NewRequest("GET", "/stats?group-by=model,submodule", nil)
	if got := parseGroupBy(r); !reflect.DeepEqual(got, []string{"model", "submodule"}) {
		t.Fatalf("parseGroupBy (csv): got %v", got)
	}
	r = httptest.NewRequest("GET", "/stats?group-by=model&group-by=submodule&group-by=model", nil)
	if got := parseGroupBy(r); !reflect.DeepEqual(got, []string{"model", "submodule"}) {
		t.Fatalf("parseGroupBy (repeated, deduped): got %v", got)
	}
}

// TestStatsAddFilterRedirect checks the add-filter control's canonicalization:
// posting the two plain fkey/fval inputs (never a pre-joined query string)
// 303-redirects to the SAME `filter=key=value` URL shape a hand-built link or
// a chip removal would use, preserving any already-active filter/group-by.
func TestStatsAddFilterRedirect(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/stats?filter=kind=review&group-by=model&fkey=submodule&fval=alpha")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("add-filter: code=%d, want %d", w.Code, http.StatusSeeOther)
	}
	loc := w.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	got := u.Query()["filter"]
	want := []string{"kind=review", "submodule=alpha"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("redirect filter params: got %v want %v (location=%q)", got, want, loc)
	}
	if got := u.Query().Get("group-by"); got != "model" {
		t.Fatalf("redirect must preserve group-by=model, got %q (location=%q)", got, loc)
	}
}

// TestParseFiltersOperators is tag-filter-operators' direct parseFilters unit
// test: `!=` (a trailing `!` on the key) and `=~` (a leading `~` on the value)
// both parse off the raw `filter=key<op>value` chip, while a bare
// `filter=key=value` chip keeps parsing to the "" zero-value Op exactly as
// before operators existed (so every pre-existing chip/URL is unaffected).
func TestParseFiltersOperators(t *testing.T) {
	r := httptest.NewRequest("GET", "/stats?filter=kind=review&filter=model!=sonnet&filter=branch=~%5Ebee-&filter=malformed", nil)
	got := parseFilters(r)
	want := []tagFilter{
		{Key: "kind", Op: "", Value: "review"},
		{Key: "model", Op: "!=", Value: "sonnet"},
		{Key: "branch", Op: "=~", Value: "^bee-"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseFilters: got %+v want %+v", got, want)
	}

	// A chip that is JUST an operator marker with no real key (e.g. a
	// hand-built `filter=!=x` or `filter==~x`) drops as malformed, same as
	// the pre-existing empty-key case.
	r = httptest.NewRequest("GET", "/stats?filter=!=x&filter="+url.QueryEscape("=~x"), nil)
	if got := parseFilters(r); len(got) != 0 {
		t.Fatalf("operator-only chips (no key) must be dropped, got %+v", got)
	}
}

// TestMatchesFiltersOperators is tag-filter-operators' direct matchesFilters
// unit test, exercising all three operators plus the two properties the ROI
// specifically calls out: an absent tag key reads as "" for `!=` exactly like
// it already did for `=` (missing-tag-reads-as-empty-string), and an
// unparsable `=~` pattern degrades to "matches nothing" rather than a panic
// or error return (matchesFilters has no error return at all).
func TestMatchesFiltersOperators(t *testing.T) {
	tags := map[string]string{"kind": "review", "model": "opus"}

	// "=" (and its "" zero value) unchanged.
	if !matchesFilters(tags, []tagFilter{{Key: "kind", Value: "review"}}) {
		t.Fatal(`kind=review must match`)
	}
	if matchesFilters(tags, []tagFilter{{Key: "kind", Value: "work"}}) {
		t.Fatal(`kind=work must NOT match`)
	}

	// "!=" excludes an equal value, includes everything else, and includes a
	// tag the session's set OMITS entirely (reads as "" — never equal to a
	// non-empty Value).
	if matchesFilters(tags, []tagFilter{{Key: "kind", Op: "!=", Value: "review"}}) {
		t.Fatal(`kind!=review must NOT match a review session`)
	}
	if !matchesFilters(tags, []tagFilter{{Key: "kind", Op: "!=", Value: "work"}}) {
		t.Fatal(`kind!=work must match a review session`)
	}
	if !matchesFilters(tags, []tagFilter{{Key: "cohort", Op: "!=", Value: "A"}}) {
		t.Fatal(`cohort!=A must match a session with no cohort tag at all (missing reads as "")`)
	}
	// The empty-value edge: a tag the session omits reads as "", so
	// `cohort!=""` (an empty Value) must EXCLUDE it ("" == "").
	if matchesFilters(tags, []tagFilter{{Key: "cohort", Op: "!=", Value: ""}}) {
		t.Fatal(`cohort!= (empty value) must NOT match a session missing the cohort tag ("" == "")`)
	}

	// "=~" matches by regex; a pattern that fails to compile matches NOTHING,
	// never panics, and matchesFilters still just returns a plain bool.
	if !matchesFilters(tags, []tagFilter{{Key: "model", Op: "=~", Value: "^op"}}) {
		t.Fatal("model=~^op must match model=opus")
	}
	if matchesFilters(tags, []tagFilter{{Key: "model", Op: "=~", Value: "^son"}}) {
		t.Fatal("model=~^son must NOT match model=opus")
	}
	if matchesFilters(tags, []tagFilter{{Key: "model", Op: "=~", Value: "("}}) {
		t.Fatal("an invalid regex must degrade to no match, not match everything")
	}

	// AND across mixed operators: every chip must hold.
	mixed := []tagFilter{
		{Key: "kind", Value: "review"},
		{Key: "model", Op: "!=", Value: "sonnet"},
		{Key: "model", Op: "=~", Value: "^op"},
	}
	if !matchesFilters(tags, mixed) {
		t.Fatal("mixed operator set should ALL hold for this session")
	}
	mixed[1].Value = "opus" // now model!=opus fails, even though the other two chips still hold
	if matchesFilters(tags, mixed) {
		t.Fatal("mixed operator set must fail as soon as ONE chip fails (AND, never OR)")
	}
}

// TestStatsGroupedNotEqualOperator is tag-filter-operators' `!=` acceptance
// case end-to-end: `model!=sonnet` excludes ONLY the sonnet session — a
// same-kind opus session AND a session with no model header at all (which
// reads as "" per the pre-existing missing-tag semantics) both survive.
// Flipping to `model!=` (empty value) then excludes exactly the no-model
// session instead ("" == "").
func TestStatsGroupedNotEqualOperator(t *testing.T) {
	s, root := setup(t)
	const opus = "github-copilot/claude-opus-4.8"
	const sonnet = "github-copilot/claude-sonnet-5"

	writeStatsPlan(t, root, "alpha", [][2]string{{"t1", "DONE"}, {"t2", "DONE"}, {"t3", "DONE"}})
	writeSubTranscript(t, root, "alpha", "bee-t1-100-1", "work", opus)
	writeSubTranscript(t, root, "alpha", "bee-t2-200-2", "work", sonnet)
	writeSubTranscript(t, root, "alpha", "bee-t3-300-3", "work", "") // no model header

	rows, err := s.computeGroupedStats(context.Background(), []tagFilter{{Key: "model", Op: "!=", Value: sonnet}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Honeybees != 2 || rows[0].DeliveredTasks != 2 {
		t.Fatalf("model!=%s: got %+v, want 1 row Honeybees=2 Delivered=2 (opus + no-model session; sonnet excluded)", sonnet, rows)
	}

	rows, err = s.computeGroupedStats(context.Background(), []tagFilter{{Key: "model", Op: "!=", Value: ""}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Honeybees != 2 || rows[0].DeliveredTasks != 2 {
		t.Fatalf(`model!= (empty value): got %+v, want 1 row Honeybees=2 Delivered=2 (opus + sonnet; no-model session excluded)`, rows)
	}

	// End-to-end through the real handler; the URL stays a plain, shareable
	// GET (filter=key<op>value, here percent-encoded by the query encoder).
	w := get(t, s, "/stats?filter="+url.QueryEscape("model!="+sonnet))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /stats?filter=model!=%s: code=%d body=%s", sonnet, w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "model!="+sonnet) {
		t.Fatalf("response missing rendered chip %q:\n%s", "model!="+sonnet, body)
	}
	// BOTH the add-filter form's and the group-by form's hidden re-post
	// inputs must carry the FULL operator-encoded value, not just the bare
	// key=value shape — otherwise submitting the group-by checkboxes (a
	// plain GET on a separate <form>) would silently drop this chip's `!=`
	// back to an unqualified equality filter.
	hidden := `value="model!=` + sonnet + `"`
	if n := strings.Count(body, hidden); n != 2 {
		t.Fatalf("want the hidden filter re-post input %q exactly twice (filter-add form + group-by form), got %d:\n%s", hidden, n, body)
	}
}

// TestStatsGroupedRegexOperator is tag-filter-operators' `=~` acceptance case
// end-to-end: `model=~^github-copilot/claude-opus` matches only the opus
// session, excluding sonnet even though both models share a common prefix
// beyond what the anchored pattern allows.
func TestStatsGroupedRegexOperator(t *testing.T) {
	s, root := setup(t)
	const opus = "github-copilot/claude-opus-4.8"
	const sonnet = "github-copilot/claude-sonnet-5"

	writeStatsPlan(t, root, "alpha", [][2]string{{"t1", "DONE"}, {"t2", "TODO"}})
	writeSubTranscript(t, root, "alpha", "bee-t1-100-1", "work", opus)
	writeSubTranscript(t, root, "alpha", "bee-t2-200-2", "work", sonnet)

	rows, err := s.computeGroupedStats(context.Background(), []tagFilter{{Key: "model", Op: "=~", Value: `^github-copilot/claude-opus`}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Honeybees != 1 || rows[0].DeliveredTasks != 1 {
		t.Fatalf("model=~^...opus: got %+v, want 1 row Honeybees=1 Delivered=1 (only the opus session)", rows)
	}

	w := get(t, s, "/stats?filter="+url.QueryEscape("model=~^github-copilot/claude-opus"))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /stats?filter=model=~...: code=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "model=~^github-copilot/claude-opus") {
		t.Fatalf("response missing rendered regex chip:\n%s", w.Body.String())
	}
}

// TestStatsGroupedInvalidRegexNoMatches is tag-filter-operators' negative
// control for `=~`: an unparsable pattern degrades to matching NOTHING (zero
// rows) — never a Go error from computeGroupedStats and never a 500 through
// the real handler, same "unknown filter yields an empty group" contract
// stats-filter-groupby already established for equality.
func TestStatsGroupedInvalidRegexNoMatches(t *testing.T) {
	s, root := setup(t)
	writeStatsPlan(t, root, "alpha", [][2]string{{"t1", "DONE"}})
	writeSubTranscript(t, root, "alpha", "bee-t1-100-1", "work", "github-copilot/claude-opus-4.8")

	rows, err := s.computeGroupedStats(context.Background(), []tagFilter{{Key: "model", Op: "=~", Value: "("}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("invalid regex must yield zero rows, got %+v", rows)
	}

	w := get(t, s, "/stats?filter="+url.QueryEscape("model=~("))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /stats with an invalid =~ pattern must still be 200, got code=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no sessions match every filter") {
		t.Fatalf("invalid-regex filter must render the empty-match copy, not an error:\n%s", w.Body.String())
	}
}

// TestStatsGroupedMixedOperatorAND is tag-filter-operators' core acceptance
// case: three chips, one per operator (`kind=review` equality, `model!=
// sonnet` not-equal, `branch=~^bee-keep` regex), ANDed together. Each of the
// three "wrong" sessions fails EXACTLY one chip (proving every operator, not
// just one of them, actually restricts the pool) while only the session
// passing all three survives.
func TestStatsGroupedMixedOperatorAND(t *testing.T) {
	s, root := setup(t)
	const opus = "github-copilot/claude-opus-4.8"
	const sonnet = "github-copilot/claude-sonnet-5"

	writeStatsPlan(t, root, "alpha", [][2]string{
		{"keepme", "DONE"}, {"keepbutwrongkind", "DONE"}, {"keepbutwrongmodel", "DONE"}, {"dropbranch", "DONE"},
	})
	writeSubTranscript(t, root, "alpha", "bee-keepme-100-1", "review", opus)              // passes all 3
	writeSubTranscript(t, root, "alpha", "bee-keepbutwrongkind-200-2", "work", opus)      // fails kind= ONLY
	writeSubTranscript(t, root, "alpha", "bee-keepbutwrongmodel-300-3", "review", sonnet) // fails model!= ONLY
	writeSubTranscript(t, root, "alpha", "bee-dropbranch-400-4", "review", opus)          // fails branch=~ ONLY

	filters := []tagFilter{
		{Key: "kind", Value: "review"},
		{Key: "model", Op: "!=", Value: sonnet},
		{Key: "branch", Op: "=~", Value: "^bee-keep"},
	}
	rows, err := s.computeGroupedStats(context.Background(), filters, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Honeybees != 1 || rows[0].DeliveredTasks != 1 {
		t.Fatalf("mixed-operator AND: got %+v, want exactly 1 row Honeybees=1 Delivered=1 (only bee-keepme survives every chip)", rows)
	}

	// End-to-end, all three operators in the one shareable URL.
	q := url.Values{}
	q.Add("filter", "kind=review")
	q.Add("filter", "model!="+sonnet)
	q.Add("filter", "branch=~^bee-keep")
	w := get(t, s, "/stats?"+q.Encode())
	if w.Code != http.StatusOK {
		t.Fatalf("GET /stats with mixed operators: code=%d body=%s", w.Code, w.Body.String())
	}
}

// TestStatsAddFilterRedirectWithOperator extends TestStatsAddFilterRedirect to
// the add-filter form's operator selector (`fop`): posting fkey/fop/fval with
// fop=!= (or =~) must 303-redirect to a `filter=key<op>value` URL carrying
// that SAME operator, not silently downgrading it to equality.
func TestStatsAddFilterRedirectWithOperator(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/stats?fkey=model&fop=!=&fval=sonnet")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("add-filter with fop=!=: code=%d, want %d", w.Code, http.StatusSeeOther)
	}
	loc := w.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.Query()["filter"], []string{"model!=sonnet"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("redirect filter params: got %v want %v (location=%q)", got, want, loc)
	}

	w = get(t, s, "/stats?fkey=model&fop="+url.QueryEscape("=~")+"&fval=%5Eopus")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("add-filter with fop==~: code=%d, want %d", w.Code, http.StatusSeeOther)
	}
	u, err = url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.Query()["filter"], []string{"model=~^opus"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("redirect filter params: got %v want %v", got, want)
	}

	// An unrecognized/garbage fop (never posted by the form itself, but a
	// hand-built request might try) normalizes to plain equality rather than
	// producing a bogus operator in the URL.
	w = get(t, s, "/stats?fkey=model&fop=bogus&fval=opus")
	u, err = url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.Query()["filter"], []string{"model=opus"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("redirect filter params (garbage fop): got %v want %v", got, want)
	}
}

// --- status-badge-contrast-fix -----------------------------------------------
//
// ui-audit-001 flagged several solid-fill status/state badges (white text over
// an opaque single-hue background) under the WCAG 2.x 4.5:1 normal-text
// contrast threshold, measured against the DECLARED hsl() values via the
// standard relative-luminance formula (not eyeballed). The helpers below
// recompute that same formula from whatever internal/web/assets/style.css
// ACTUALLY declares today, so a future edit that quietly drags one of these
// fills back under 4.5:1 fails TestBadgeSolidFillsMeetWCAGAA instead of
// shipping a silent regression.

// hslToRGB converts an hsl(h, s, l) triple (h in degrees; s, l in 0..1) to
// sRGB components in 0..1, using the same conversion the CSS Color spec (and
// every browser) applies to hsl().
func hslToRGB(h, s, l float64) (r, g, b float64) {
	h = math.Mod(h, 360)
	if h < 0 {
		h += 360
	}
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r1, g1, b1 float64
	switch {
	case h < 60:
		r1, g1, b1 = c, x, 0
	case h < 120:
		r1, g1, b1 = x, c, 0
	case h < 180:
		r1, g1, b1 = 0, c, x
	case h < 240:
		r1, g1, b1 = 0, x, c
	case h < 300:
		r1, g1, b1 = x, 0, c
	default:
		r1, g1, b1 = c, 0, x
	}
	return r1 + m, g1 + m, b1 + m
}

// srgbChannelToLinear applies the WCAG 2.x relative-luminance transfer
// function to a single sRGB channel in 0..1.
func srgbChannelToLinear(c float64) float64 {
	if c <= 0.03928 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// relativeLuminance is the WCAG 2.x relative luminance of an sRGB color whose
// channels are each in 0..1.
func relativeLuminance(r, g, b float64) float64 {
	return 0.2126*srgbChannelToLinear(r) + 0.7152*srgbChannelToLinear(g) + 0.0722*srgbChannelToLinear(b)
}

// contrastRatio is the WCAG 2.x contrast ratio between two relative
// luminances: (lighter+0.05)/(darker+0.05).
func contrastRatio(l1, l2 float64) float64 {
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05)
}

// hslWhiteContrast is the WCAG contrast ratio between an hsl(h, s%, l%)
// background (h in degrees; s, l in PERCENT, 0..100) and opaque white (#fff)
// foreground text — the pairing every badge/state pill under test uses.
func hslWhiteContrast(h, s, l float64) float64 {
	r, g, b := hslToRGB(h, s/100, l/100)
	return contrastRatio(1.0, relativeLuminance(r, g, b))
}

// cssHueDegrees extracts the numeric degrees of a `--hue-x: N;` custom
// property declared in style.css (e.g. "hue-done" -> 146), so the test
// verifies against whatever hue is ACTUALLY in force rather than a duplicated
// literal.
func cssHueDegrees(t *testing.T, css, hueVar string) float64 {
	t.Helper()
	re := regexp.MustCompile(`--` + regexp.QuoteMeta(hueVar) + `:\s*([\d.]+)\s*;`)
	m := re.FindStringSubmatch(css)
	if m == nil {
		t.Fatalf("style.css: no `--%s: <degrees>;` declaration found", hueVar)
	}
	deg, err := parseFloatT(t, m[1])
	if err != nil {
		t.Fatalf("style.css: --%s value %q: %v", hueVar, m[1], err)
	}
	return deg
}

// cssSelectorHueSL extracts the saturation/lightness percentages a CSS rule's
// `background: hsl(var(--hueVar) S% L%)` declaration uses (e.g. `.badge.live`
// borrowing `--hue-done`) — the mode-invariant badges that hardcode S/L
// directly rather than going through a --badge-solid-* token.
func cssSelectorHueSL(t *testing.T, css, selector, hueVar string) (s, l float64) {
	t.Helper()
	re := regexp.MustCompile(regexp.QuoteMeta(selector) + `\s*\{[^}]*background:\s*hsl\(\s*var\(--` + regexp.QuoteMeta(hueVar) + `\)\s*([\d.]+)%\s*([\d.]+)%\s*\)`)
	m := re.FindStringSubmatch(css)
	if m == nil {
		t.Fatalf("style.css: %s has no `background: hsl(var(--%s) S%% L%%)` declaration", selector, hueVar)
	}
	return parseFloat2T(t, m[1], m[2])
}

// cssTokenHueSLAll extracts every `--tokenName: hsl(var(--hueVar) S% L%);`
// declaration for tokenName, in source order (the :root/light declaration
// first, the `prefers-color-scheme: dark` override second) — the
// --badge-solid-green/--badge-solid-red tokens, each declared once per mode.
func cssTokenHueSLAll(t *testing.T, css, tokenName, hueVar string) [][2]float64 {
	t.Helper()
	re := regexp.MustCompile(`--` + regexp.QuoteMeta(tokenName) + `:\s*hsl\(\s*var\(--` + regexp.QuoteMeta(hueVar) + `\)\s*([\d.]+)%\s*([\d.]+)%\s*\)`)
	ms := re.FindAllStringSubmatch(css, -1)
	out := make([][2]float64, 0, len(ms))
	for _, m := range ms {
		s, l := parseFloat2T(t, m[1], m[2])
		out = append(out, [2]float64{s, l})
	}
	return out
}

func parseFloatT(t *testing.T, s string) (float64, error) {
	t.Helper()
	return strconv.ParseFloat(s, 64)
}

func parseFloat2T(t *testing.T, a, b string) (float64, float64) {
	t.Helper()
	av, err := parseFloatT(t, a)
	if err != nil {
		t.Fatalf("parse %q: %v", a, err)
	}
	bv, err := parseFloatT(t, b)
	if err != nil {
		t.Fatalf("parse %q: %v", b, err)
	}
	return av, bv
}

// TestBadgeSolidFillsMeetWCAGAA locks the status-badge-contrast-fix acceptance:
// every enumerated solid-fill badge/state pill reaches >=4.5:1 contrast (WCAG
// AA, normal text) against its white foreground, in BOTH light and dark, using
// the values ACTUALLY declared in the embedded stylesheet (computed, not
// eyeballed). It also locks that each pill's background still routes through
// the dedicated --badge-solid-green/--badge-solid-red tokens (not directly
// through --success/--danger, which serve other, contrast-incompatible
// purposes elsewhere — see the :root comment in style.css) so a future edit
// can't silently revert the wiring while leaving the tokens themselves fixed.
func TestBadgeSolidFillsMeetWCAGAA(t *testing.T) {
	raw, err := assetFS.ReadFile("assets/style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	const minRatio = 4.5

	hueDone := cssHueDegrees(t, css, "hue-done")
	hueReview := cssHueDegrees(t, css, "hue-review")
	hueArbitration := cssHueDegrees(t, css, "hue-arbitration")

	assertMeets := func(label string, h, s, l float64) {
		t.Helper()
		got := hslWhiteContrast(h, s, l)
		if got < minRatio {
			t.Errorf("%s: hsl(%g %g%% %g%%) vs white = %.3f:1, want >= %.1f:1", label, h, s, l, got, minRatio)
		}
	}

	// Mode-invariant pills: a single hardcoded S/L borrowing a shared hue var,
	// so one declaration covers both light and dark.
	liveS, liveL := cssSelectorHueSL(t, css, ".badge.live", "hue-done")
	assertMeets(".badge.live (mode-invariant)", hueDone, liveS, liveL)
	driftDriftS, driftDriftL := cssSelectorHueSL(t, css, ".badge.drift-drift", "hue-review")
	assertMeets(".badge.drift-drift (mode-invariant)", hueReview, driftDriftS, driftDriftL)

	// Mode-varying pills driven by the dedicated --badge-solid-green /
	// --badge-solid-red tokens: each token must declare exactly one light
	// (:root) and one dark (prefers-color-scheme: dark) value, and BOTH must
	// independently clear the threshold.
	greenSL := cssTokenHueSLAll(t, css, "badge-solid-green", "hue-done")
	if len(greenSL) != 2 {
		t.Fatalf("style.css: expected 2 declarations of --badge-solid-green (light+dark), found %d", len(greenSL))
	}
	assertMeets("--badge-solid-green (light)", hueDone, greenSL[0][0], greenSL[0][1])
	assertMeets("--badge-solid-green (dark)", hueDone, greenSL[1][0], greenSL[1][1])

	redSL := cssTokenHueSLAll(t, css, "badge-solid-red", "hue-arbitration")
	if len(redSL) != 2 {
		t.Fatalf("style.css: expected 2 declarations of --badge-solid-red (light+dark), found %d", len(redSL))
	}
	assertMeets("--badge-solid-red (light)", hueArbitration, redSL[0][0], redSL[0][1])
	assertMeets("--badge-solid-red (dark)", hueArbitration, redSL[1][0], redSL[1][1])

	// Lock the wiring: every enumerated consumer routes through the
	// dedicated token, not the shared --success/--danger it used to.
	for _, want := range []string{
		".badge.env-green { background: var(--badge-solid-green);",
		".badge.drift-clean { background: var(--badge-solid-green);",
		".state.live { background: var(--badge-solid-green);",
		".badge.drift-missing { background: var(--badge-solid-red);",
		".state.dirty { background: var(--badge-solid-red);",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css: missing expected wiring %q (badge no longer routes through the accessible token?)", want)
		}
	}
	for _, unwanted := range []string{
		".badge.env-green { background: var(--success);",
		".badge.drift-clean { background: var(--success);",
		".state.live { background: var(--success);",
		".badge.drift-missing { background: var(--danger);",
		".state.dirty { background: var(--danger);",
	} {
		if strings.Contains(css, unwanted) {
			t.Errorf("style.css: still has pre-fix wiring %q (--success/--danger do not clear 4.5:1 with white text here)", unwanted)
		}
	}

	// The per-status hue IDENTITY must be untouched: --hue-done/--hue-review/
	// --hue-arbitration still drive .status-done/.status-needs-review/
	// .status-needs-arbitration (the translucent status pills), so this fix
	// only retuned lightness on the solid fills, never the hue palette.
	for _, want := range []string{
		".status-done { --sh: var(--hue-done); }",
		".status-needs-review { --sh: var(--hue-review); }",
		".status-needs-arbitration { --sh: var(--hue-arbitration); }",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css: status pill hue wiring changed unexpectedly, missing %q", want)
		}
	}
}

// TestBadgeContrastHelpersKnownValues sanity-checks the WCAG helpers
// themselves against well-known reference ratios (black/white on white is
// 1:1/21:1) so a bug in hslToRGB/relativeLuminance/contrastRatio can't
// silently rubber-stamp TestBadgeSolidFillsMeetWCAGAA above.
func TestBadgeContrastHelpersKnownValues(t *testing.T) {
	if got := hslWhiteContrast(0, 0, 100); math.Abs(got-1.0) > 0.001 {
		t.Errorf("white-on-white = %.4f, want 1.0", got)
	}
	if got := hslWhiteContrast(0, 0, 0); math.Abs(got-21.0) > 0.01 {
		t.Errorf("black-on-white = %.4f, want 21.0", got)
	}
	// A pure, fully-saturated mid-lightness red (hsl(0 100% 50%) = #ff0000)
	// against white is a widely-published reference value (~3.998:1).
	if got := hslWhiteContrast(0, 100, 50); math.Abs(got-3.998) > 0.01 {
		t.Errorf("pure red vs white = %.4f, want ~3.998", got)
	}
}

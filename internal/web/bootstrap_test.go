package web

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spencerharmon/beehive/prompts"
)

// bootSub creates submodules/<name> under root with the requested ROI.md / PLAN.md
// pair and returns the matching repo.Submodule, so detectBootstrap sees the same
// on-disk files repo.Submodule.NeedsBootstrap stats. roi+plan => a ready target,
// roi only => a bootstrap-pending target.
func bootSub(t *testing.T, root, name string, roi, plan bool) repo.Submodule {
	t.Helper()
	p := filepath.Join(root, "submodules", name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if roi {
		write(t, filepath.Join(p, repo.ROIFile), "# "+name+"\n")
	}
	if plan {
		write(t, filepath.Join(p, repo.PlanFile), "<!-- Beehive-ROI: x -->\n# Plan\n")
	}
	return repo.Submodule{Name: name, Path: p}
}

// unmetKeys projects a detection result to the ordered list of step Keys, the
// stable identity the banner/handler routing and these tests assert against.
func unmetKeys(st bootstrapState) []string {
	var k []string
	for _, s := range st.Unmet {
		k = append(k, s.Key)
	}
	return k
}

// editWorktrees lists the .worktrees entries under root whose name carries prefix
// (the ephemeral chat-edit worktrees). Absent .worktrees is zero, not an error —
// that is exactly the read-only case (nothing was ever cut).
func editWorktrees(t *testing.T, root, prefix string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(root, ".worktrees"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), prefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestDetectBootstrapSignals is the core of the detector: each unmet-signal
// fixture (missing LOCALS.md / missing runner config / no submodules /
// ROI-without-PLAN) classifies UNBOOTSTRAPPED with exactly the right unmet step,
// a fully set-up repo classifies BOOTSTRAPPED, and when several signals are unmet
// the steps come back in the documented setup order (locals, submodules, plan,
// config). cfgDir == root here (config.Defaults(root).Dir), so runnerConfigured
// keys off <root>/config.yaml.
func TestDetectBootstrapSignals(t *testing.T) {
	// subKind selects the on-disk submodule shape a case is built with.
	const (
		subReady     = "ready"     // one target with ROI + PLAN
		subBootstrap = "bootstrap" // one target with ROI only (NeedsBootstrap)
		subNone      = "none"      // no registered targets
	)
	cases := []struct {
		name           string
		locals, config bool
		sub            string
		want           []string // ordered unmet keys; nil => bootstrapped
	}{
		{"fully configured", true, true, subReady, nil},
		{"missing locals", false, true, subReady, []string{"locals"}},
		{"missing config", true, false, subReady, []string{"config"}},
		{"no submodules", true, true, subNone, []string{"submodules"}},
		{"roi without plan", true, true, subBootstrap, []string{"plan"}},
		// A blank install trips every step: locals, then submodules (empty set),
		// then config. No "plan" — there is no submodule to derive one for.
		{"blank install order", false, false, subNone, []string{"locals", "submodules", "config"}},
		// locals + config gone but a ready target present: only the two root-level
		// steps, in setup order (locals before config).
		{"locals and config order", false, false, subReady, []string{"locals", "config"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			if c.locals {
				write(t, filepath.Join(root, repo.LocalsFile), "# site facts\n")
			}
			if c.config {
				write(t, filepath.Join(root, config.FileName), "model: x\n")
			}
			var subs []repo.Submodule
			switch c.sub {
			case subReady:
				subs = []repo.Submodule{bootSub(t, root, "alpha", true, true)}
			case subBootstrap:
				subs = []repo.Submodule{bootSub(t, root, "alpha", true, false)}
			case subNone:
				// no targets registered
			}

			st := detectBootstrap(root, root, subs)
			if got := unmetKeys(st); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("unmet keys = %v, want %v", got, c.want)
			}
			if got, want := st.Bootstrapped(), len(c.want) == 0; got != want {
				t.Fatalf("Bootstrapped() = %v, want %v (unmet %v)", got, want, c.want)
			}
		})
	}
}

// TestRunnerConfigured proves "configured" means the same layer paths config
// resolves: the host file (<cfgDir>/config.yaml) OR the in-repo global
// (<root>/config.yaml). Either present => configured; neither => not; a directory
// named config.yaml does not count as a file.
func TestRunnerConfigured(t *testing.T) {
	// Neither layer present.
	root := t.TempDir()
	cfgDir := t.TempDir()
	if runnerConfigured(root, cfgDir) {
		t.Fatal("no config file anywhere should be unconfigured")
	}
	// Host layer only.
	write(t, filepath.Join(cfgDir, config.FileName), "model: x\n")
	if !runnerConfigured(root, cfgDir) {
		t.Fatal("host <cfgDir>/config.yaml should count as configured")
	}
	// In-repo global only (fresh dirs, empty host dir).
	root2 := t.TempDir()
	cfgDir2 := t.TempDir()
	write(t, filepath.Join(root2, config.FileName), "model: x\n")
	if !runnerConfigured(root2, cfgDir2) {
		t.Fatal("in-repo <root>/config.yaml should count as configured")
	}
	// A directory named config.yaml is not a config file.
	root3 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root3, config.FileName), 0o755); err != nil {
		t.Fatal(err)
	}
	if runnerConfigured(root3, "") {
		t.Fatal("a directory named config.yaml must not read as configured")
	}
}

// TestReadBootstrapGuide locks the seed source: the repo's own on-disk
// BOOTSTRAP.md is preferred (the copy an instruction update maintains), and when
// it is absent the binary's embedded default is used so seeding still works.
func TestReadBootstrapGuide(t *testing.T) {
	// Absent on disk: fall back to the embedded guide (non-empty).
	root := t.TempDir()
	if prompts.BootstrapGuide == "" {
		t.Fatal("embedded BootstrapGuide must not be empty")
	}
	if got := readBootstrapGuide(root); got != prompts.BootstrapGuide {
		t.Fatalf("no on-disk BOOTSTRAP.md should fall back to the embedded guide")
	}
	// Present on disk: prefer it verbatim.
	want := "# custom bootstrap\nsentinel-guide-line\n"
	write(t, filepath.Join(root, repo.BootstrapFile), want)
	if got := readBootstrapGuide(root); got != want {
		t.Fatalf("on-disk BOOTSTRAP.md not preferred: got %q", got)
	}
}

// TestBootstrapSystemPrompt proves the setup agent's system prompt is the
// chat-diff editor contract over LOCALS.md PLUS a bootstrap preamble: it carries
// the editor role (over LOCALS.md), the CLI-only submodule-add rule, every
// detected unmet step title, and the full guide fenced between the BEGIN/END
// markers.
func TestBootstrapSystemPrompt(t *testing.T) {
	st := bootstrapState{Unmet: []bootstrapStep{
		{Key: "locals", Title: "Author LOCALS.md", Detail: "d1"},
		{Key: "config", Title: "Configure the runner", Detail: "d2"},
	}}
	guide := "sentinel-guide-line uniquely-marks-the-embedded-guide\n"
	sys := bootstrapSystemPrompt(guide, st)

	for _, want := range []string{
		// the chat-diff editor contract, bound to LOCALS.md
		repo.LocalsFile,
		"do NOT have permission to modify files",
		// bootstrap preamble
		"setup guide",
		"beehive submodule add",
		"NEVER create submodule directories by hand",
		// the detected unmet steps, by title
		"Author LOCALS.md",
		"Configure the runner",
		// the guide, fenced
		"---- BEGIN BOOTSTRAP.md ----",
		"sentinel-guide-line uniquely-marks-the-embedded-guide",
		"---- END BOOTSTRAP.md ----",
	} {
		if !strings.Contains(sys, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, sys)
		}
	}
}

// TestOpenBootstrapIdempotentReadOnly is the singleton + read-only core: opening
// the setup agent yields ONE chat-edit session over LOCALS.md seeded with a
// visible system intro; a second open REUSES that session (same ID, no second
// worktree, no re-seed); and nothing is written to LOCALS.md or committed to main
// — only one ephemeral edit worktree is cut. chatFixture seeds a committed repo so
// a worktree can be cut from main.
func TestOpenBootstrapIdempotentReadOnly(t *testing.T) {
	s, root := chatFixture(t, "")
	ctx := context.Background()
	headBefore := s.headSHA(ctx)

	const sysSentinel = "SYS-SENTINEL"
	const introSentinel = "INTRO-SENTINEL: repo not bootstrapped"

	sess, err := s.chat.openBootstrap(ctx, sysSentinel, introSentinel)
	if err != nil {
		t.Fatalf("openBootstrap: %v", err)
	}
	if sess.Path != repo.LocalsFile {
		t.Fatalf("bootstrap session path = %q, want %q", sess.Path, repo.LocalsFile)
	}
	if sess.sys != sysSentinel {
		t.Fatalf("bootstrap session sys = %q, want the supplied prompt", sess.sys)
	}
	// The intro is seeded as a single visible system turn.
	log := sess.logCopy()
	if len(log) != 1 || log[0].Role != "system" || log[0].Text != introSentinel {
		t.Fatalf("intro not seeded as one system turn: %+v", log)
	}

	// Second open: same singleton session, no re-seed, no extra worktree.
	sess2, err := s.chat.openBootstrap(ctx, "OTHER-SYS", "OTHER-INTRO")
	if err != nil {
		t.Fatalf("openBootstrap (reuse): %v", err)
	}
	if sess2.ID != sess.ID {
		t.Fatalf("openBootstrap not idempotent: ids %q != %q", sess2.ID, sess.ID)
	}
	if l := sess2.logCopy(); len(l) != 1 {
		t.Fatalf("reuse must not re-seed the intro: log = %+v", l)
	}
	if wts := editWorktrees(t, root, "edit-LOCALS-md-"); len(wts) != 1 {
		t.Fatalf("want exactly one LOCALS.md edit worktree, got %v", wts)
	}

	// Read-only: the real LOCALS.md is untouched and main HEAD did not move.
	if _, err := os.Stat(filepath.Join(root, repo.LocalsFile)); !os.IsNotExist(err) {
		t.Fatalf("bootstrap open must not write LOCALS.md (stat err=%v)", err)
	}
	if got := s.headSHA(ctx); got != headBefore {
		t.Fatalf("bootstrap open must not move main HEAD: %q -> %q", headBefore, got)
	}
}

// TestDashboardBannerWhenUnbootstrapped proves the auto-detect surfaces on the
// real dashboard: setup() has a ready alpha but no LOCALS.md and no config, so the
// advisory banner renders with its unmet steps and the htmx hook that lazily loads
// the agent — and the dashboard render itself stays READ-ONLY (no worktree cut).
func TestDashboardBannerWhenUnbootstrapped(t *testing.T) {
	s, root := setup(t)
	body := get(t, s, "/").Body.String()
	for _, want := range []string{
		"Finish setting up this beehive",
		`id="bootstrap-agent"`,
		`hx-get="/bootstrap"`,
		"Author LOCALS.md",     // unmet: locals
		"Configure the runner", // unmet: config
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard banner missing %q:\n%s", want, body)
		}
	}
	// Detection + banner render must not cut any worktree.
	if _, err := os.Stat(filepath.Join(root, ".worktrees")); !os.IsNotExist(err) {
		t.Fatalf("dashboard render must not cut a worktree (stat err=%v)", err)
	}
}

// TestDashboardBannerHiddenWhenBootstrapped proves the banner is advisory and
// idempotent: once the signals clear (LOCALS.md + config present, alpha already
// ROI+PLAN), a fresh dashboard render shows no banner and no auto-open hook.
func TestDashboardBannerHiddenWhenBootstrapped(t *testing.T) {
	s, root := setup(t)
	write(t, filepath.Join(root, repo.LocalsFile), "# site facts\n")
	write(t, filepath.Join(root, config.FileName), "model: x\n")

	body := get(t, s, "/").Body.String()
	for _, absent := range []string{
		"Finish setting up this beehive",
		`id="bootstrap-agent"`,
		`hx-get="/bootstrap"`,
	} {
		if strings.Contains(body, absent) {
			t.Fatalf("bootstrapped dashboard must not show %q:\n%s", absent, body)
		}
	}
}

// TestBootstrapAgentHandlerUnbootstrapped drives GET /bootstrap on an
// unbootstrapped repo: it opens the singleton agent and returns the fragment
// wired to the shared chat-edit panel/message endpoints (chat-diff-editor-core).
func TestBootstrapAgentHandlerUnbootstrapped(t *testing.T) {
	s, _ := chatFixture(t, "")
	w := get(t, s, "/bootstrap")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /bootstrap = %d, want 200: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, want := range []string{
		`class="bootstrap-agent"`,
		`/panel"`,   // hx-get="/edit/<id>/panel"
		`/message"`, // hx-post="/edit/<id>/message"
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("bootstrap agent fragment missing %q:\n%s", want, body)
		}
	}
}

// TestBootstrapAgentShellPollsOnceOnLoad locks the served /bootstrap shell to the
// idle poll backoff (bootstrap-chat-poll-backoff): the #chatedit wrapper fetches
// the panel ONCE on load, never on an unconditional interval. This supersedes the
// old chat-editor-snappy-polish "load, every 1500ms" wrapper — a working turn is
// still followed, but by chatedit_panel.html re-arming the poll ONLY while .Busy
// (the idle-vs-busy re-arm is locked template-level in TestPollBackoffWhenEndedOrIdle),
// so an idle wizard tab no longer re-fetches an unchanging transcript every 1.5s —
// the same backoff editor.html / human_resolve.html already ship. Mirrors
// editor.html's "load" shell.
func TestBootstrapAgentShellPollsOnceOnLoad(t *testing.T) {
	s, _ := chatFixture(t, "")
	body := get(t, s, "/bootstrap").Body.String()
	if !strings.Contains(body, `hx-trigger="load"`) || strings.Contains(body, "every") {
		t.Fatalf("#chatedit must poll once on load, not on an interval:\n%s", body)
	}
}

// TestBootstrapAgentHandlerBootstrapped proves the handler re-detects and stays
// read-only when the repo is already set up: GET /bootstrap returns 204 with an
// empty body and cuts NO worktree (the banner clears on its own).
func TestBootstrapAgentHandlerBootstrapped(t *testing.T) {
	s, root := chatFixture(t, "")
	write(t, filepath.Join(root, repo.LocalsFile), "# site facts\n")
	write(t, filepath.Join(root, config.FileName), "model: x\n")

	w := get(t, s, "/bootstrap")
	if w.Code != http.StatusNoContent {
		t.Fatalf("GET /bootstrap on a bootstrapped repo = %d, want 204: %s", w.Code, w.Body)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("204 response must have an empty body, got %q", w.Body.String())
	}
	if wts := editWorktrees(t, root, "edit-LOCALS-md-"); len(wts) != 0 {
		t.Fatalf("bootstrapped /bootstrap must not open an agent worktree, got %v", wts)
	}
}

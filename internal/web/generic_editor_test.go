package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spencerharmon/beehive/internal/swarm"
)

// fakeEditorClient is an editor.AgentClient that, on each turn, runs editFn
// against the session worktree (the "edit") and returns reply. It lets the web
// tests drive the real editor.Manager/Session without a live opencode.
type fakeEditorClient struct {
	editFn func(dir string)
	reply  string
}

func (f *fakeEditorClient) NewSession(ctx context.Context, dir, system, first string) (swarm.Session, string, error) {
	if f.editFn != nil {
		f.editFn(dir)
	}
	return &fakeEditorSession{f: f, dir: dir}, f.reply, nil
}

type fakeEditorSession struct {
	f   *fakeEditorClient
	dir string
}

func (s *fakeEditorSession) Prompt(ctx context.Context, text string) (string, error) {
	if s.f.editFn != nil {
		s.f.editFn(s.dir)
	}
	return s.f.reply, nil
}
func (s *fakeEditorSession) Messages(ctx context.Context) ([]swarm.Message, error) { return nil, nil }
func (s *fakeEditorSession) Close() error                                          { return nil }

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setupGenericEditor builds a committed beehive repo carrying one arbitrary
// (non-coordination) file and returns a Server wired to an injectable fake agent
// client, so the generic chat-diff surface can be driven end-to-end over HTTP.
func setupGenericEditor(t *testing.T) (*Server, string, *fakeEditorClient) {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.email", "t@t")
	runGit(t, root, "config", "user.name", "t")
	runGit(t, root, "config", "receive.denyCurrentBranch", "updateInstead")
	if err := repo.Init(root); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(root, "submodules", "alpha", "repo", "app.py")
	if err := os.MkdirAll(filepath.Dir(app), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app, []byte("alpha one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "seed")

	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(r, config.Defaults(root))
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeEditorClient{}
	s.editors.SetClient(fc)
	return s, root, fc
}

func postJSON(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.Routes().ServeHTTP(w, req)
	return w
}

// openGeneric opens a generic chat-diff session over path via GET /edit?path=
// and returns its session id (parsed from the redirect).
func openGeneric(t *testing.T, s *Server, path string) string {
	t.Helper()
	w := get(t, s, "/edit?path="+path)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("GET /edit?path=%s = %d, want 303\n%s", path, w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/editor/") {
		t.Fatalf("unexpected redirect %q", loc)
	}
	return strings.TrimPrefix(loc, "/editor/")
}

const genericAdd = "SECONDLINE" // plain token so it survives HTML escaping in the diff

func appendSecondLine(file string) func(dir string) {
	return func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte(genericAdd+"\n")...), 0o644)
	}
}

// TestEditGenericPathProposesDiff is the core acceptance: a chat turn over an
// ARBITRARY repo path yields a proposed diff in the panel, with Approve/Reject
// controls, and does NOT touch main (the change is only proposed).
func TestEditGenericPathProposesDiff(t *testing.T) {
	s, root, fc := setupGenericEditor(t)
	file := "submodules/alpha/repo/app.py"
	fc.reply = "I added a line."
	fc.editFn = appendSecondLine(file)

	id := openGeneric(t, s, file)
	base := gitOut(t, root, "rev-parse", "main")

	if w := postJSON(t, s, "/api/editor/"+id+"/chat", `{"message":"add a line"}`); w.Code != 200 {
		t.Fatalf("chat = %d\n%s", w.Code, w.Body.String())
	}

	panel := get(t, s, "/editor/"+id+"/panel").Body.String()
	if !strings.Contains(panel, genericAdd) {
		t.Fatalf("panel missing proposed diff line %q:\n%s", genericAdd, panel)
	}
	for _, want := range []string{
		`hx-post="/editor/` + id + `/approve"`,
		`hx-post="/editor/` + id + `/reject"`,
		"State: proposed",
	} {
		if !strings.Contains(panel, want) {
			t.Fatalf("proposed panel missing %q:\n%s", want, panel)
		}
	}
	// A generic proposal must never be a one-click Merge (that is the restricted flow).
	if strings.Contains(panel, `/editor/`+id+`/merge`) {
		t.Fatalf("generic panel must not offer the restricted merge control:\n%s", panel)
	}
	// The edit branch is uncommitted and main is untouched.
	if tip := gitOut(t, root, "rev-parse", id); tip != base {
		t.Fatalf("generic turn must not commit: tip=%s base=%s", tip, base)
	}
	onMain, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if strings.Contains(string(onMain), genericAdd) {
		t.Fatalf("generic turn must not change main: %q", string(onMain))
	}
}

// TestEditGenericApproveCommits is the approve acceptance: POST approve commits
// the proposal onto the edit branch (panel flips out of "proposed") while leaving
// main untouched.
func TestEditGenericApproveCommits(t *testing.T) {
	s, root, fc := setupGenericEditor(t)
	file := "submodules/alpha/repo/app.py"
	fc.reply = "added."
	fc.editFn = appendSecondLine(file)

	id := openGeneric(t, s, file)
	base := gitOut(t, root, "rev-parse", "main")
	if w := postJSON(t, s, "/api/editor/"+id+"/chat", `{"message":"add"}`); w.Code != 200 {
		t.Fatalf("chat = %d\n%s", w.Code, w.Body.String())
	}

	w := postForm(t, s, "/editor/"+id+"/approve", nil)
	if w.Code != 200 {
		t.Fatalf("approve = %d\n%s", w.Code, w.Body.String())
	}
	panel := w.Body.String()
	if strings.Contains(panel, "State: proposed") {
		t.Fatalf("approved panel must no longer be proposed:\n%s", panel)
	}
	// A commit now carries the change on the branch; main is NOT updated.
	if tip := gitOut(t, root, "rev-parse", id); tip == base {
		t.Fatal("approve must create a commit on the edit branch")
	}
	committed := gitOut(t, root, "show", id+":"+file)
	if !strings.Contains(committed, genericAdd) {
		t.Fatalf("committed branch file missing the change:\n%s", committed)
	}
	onMain, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if strings.Contains(string(onMain), genericAdd) {
		t.Fatalf("approve must not publish to main: %q", string(onMain))
	}
}

// TestEditGenericRejectNoop is the reject acceptance: POST reject discards the
// proposal (panel leaves "proposed", no Approve control), makes no commit, and
// never touches main.
func TestEditGenericRejectNoop(t *testing.T) {
	s, root, fc := setupGenericEditor(t)
	file := "submodules/alpha/repo/app.py"
	fc.reply = "rewrote."
	fc.editFn = appendSecondLine(file)

	id := openGeneric(t, s, file)
	base := gitOut(t, root, "rev-parse", "main")
	if w := postJSON(t, s, "/api/editor/"+id+"/chat", `{"message":"change it"}`); w.Code != 200 {
		t.Fatalf("chat = %d\n%s", w.Code, w.Body.String())
	}

	w := postForm(t, s, "/editor/"+id+"/reject", nil)
	if w.Code != 200 {
		t.Fatalf("reject = %d\n%s", w.Code, w.Body.String())
	}
	panel := w.Body.String()
	if strings.Contains(panel, "State: proposed") {
		t.Fatalf("rejected panel must not stay proposed:\n%s", panel)
	}
	if strings.Contains(panel, `hx-post="/editor/`+id+`/approve"`) {
		t.Fatalf("rejected panel must not offer an active Approve control:\n%s", panel)
	}
	// Worktree restored, no commit, main untouched.
	wtFile := filepath.Join(root, ".worktrees", id, filepath.FromSlash(file))
	wtBody, _ := os.ReadFile(wtFile)
	if strings.Contains(string(wtBody), genericAdd) {
		t.Fatalf("reject must restore the worktree file: %q", string(wtBody))
	}
	if tip := gitOut(t, root, "rev-parse", id); tip != base {
		t.Fatalf("reject must not commit: tip=%s base=%s", tip, base)
	}
	onMain, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if strings.Contains(string(onMain), genericAdd) {
		t.Fatalf("reject must not touch main: %q", string(onMain))
	}
}

// TestEditGenericTraversalRejected is the "path traversal rejected" acceptance at
// the HTTP entry point: an escaping ?path= is a 400 and opens no session.
func TestEditGenericTraversalRejected(t *testing.T) {
	s, _, _ := setupGenericEditor(t)
	for _, p := range []string{"../../etc/passwd", "submodules/../../escape", "/etc/passwd"} {
		w := get(t, s, "/edit?path="+p)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("GET /edit?path=%s = %d, want 400", p, w.Code)
		}
	}
	if n := len(s.editors.List()); n != 0 {
		t.Fatalf("a rejected traversal must open no session, got %d", n)
	}
}

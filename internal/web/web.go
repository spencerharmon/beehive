// Package web is the beehived frontend: file-derived read views and git-backed
// writes over the beehive repo. HTMX templates and assets are embedded so the
// daemon ships as a single binary. ROI.md is writable only here.
package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spencerharmon/beehive/internal/artifacts"
	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/editor"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/instruct"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spencerharmon/beehive/internal/submod"
	"github.com/spencerharmon/beehive/internal/swarm"
)

//go:embed templates/*.html
var tmplFS embed.FS

//go:embed assets/*
var assetFS embed.FS

// submoduleAddTimeout bounds a `git submodule add` (a network clone needing
// creds); it is larger than the 30s plain-commit budget because cloning a real
// remote can legitimately take longer than a local commit.
const submoduleAddTimeout = 2 * time.Minute

// faviconPath is the embedded favicon's location under assetFS, served both at
// its natural /assets/ path (via the shared FileServer below) and again at the
// conventional /favicon.ico (faviconICO) so a browser that probes the latter
// regardless of the page's <link rel=icon> never 404s.
const faviconPath = "assets/favicon.svg"

// faviconICO serves the SAME embedded SVG bytes at /favicon.ico. Most browsers
// honor layout.html's <link rel="icon"> and never request /favicon.ico at all,
// but some (and any tooling that skips the DOM, e.g. a bookmark/tab-restore
// probe) still fetch it directly regardless of that hint. A browser keys off
// the response's Content-Type, not the URL's extension, so re-serving the one
// embedded SVG here — rather than shipping a second raster asset — keeps a
// single favicon source (single-binary embed, no CDN) while ensuring that path
// is never a failed request.
func faviconICO(w http.ResponseWriter, r *http.Request) {
	b, err := assetFS.ReadFile(faviconPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write(b)
}

// pageTitle composes a page's browser <title> from innermost-first context
// parts (e.g. pageTitle("session", branch, sm.Name)), joined with " · " and
// always suffixed with the site name, so every open tab reads distinctly at a
// glance while still identifying as beehive. It is the ONE place a page title
// is assembled, keeping the scheme uniform across every handler. Empty parts
// are dropped (a caller can pass a possibly-blank value with no special
// casing); a handler that wants the bare site name (the dashboard's root page)
// simply omits "Title" from its render data instead of calling this — that
// same fallback lives in layout.html's {{if .Title}}.
func pageTitle(parts ...string) string {
	out := make([]string, 0, len(parts)+1)
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	out = append(out, "beehive")
	return strings.Join(out, " · ")
}

// Server holds the parsed templates and the repo it serves.
type Server struct {
	repo    *repo.Repo
	cfg     config.Config
	git     *git.Repo
	tmpl    *template.Template
	editors *editor.Manager
	cache   *viewCache

	// chat is the generic chat-diff editor over ANY repo file: a per-edit ROOT
	// worktree + opencode session that proposes a full-file change rendered as a
	// unified diff, applied+committed only on human approval (chat-diff-editor-core).
	chat *chatManager

	// humans owns the AI resolution AGENT for each NEEDS-HUMAN task and drives the
	// deterministic reopen (NEEDS-HUMAN -> TODO). Each session is a general,
	// tool-using opencode agent in a private worktree that can investigate the
	// blocker and make multi-file beehive-layer changes (resolveagent.go); the
	// operator reviews the diff and Publishes, then flips status via Mark resolved.
	humans *resolveManager

	// gitMu serializes operations that mutate the primary beehive checkout (where
	// main is checked out): the viewer's periodic `git pull --ff-only main` that
	// follows off-box sessions, and the frontend's own commit/publish. Without it a
	// poll-driven pull races a concurrent frontend commit on the shared index
	// (index.lock). Read-only git (log/show/status) is not guarded.
	gitMu sync.Mutex

	// pullIvl coalesces the follow-the-remote pulls: many open session panes each
	// poll every ~2s, but only one actual pull runs per interval. 0 disables
	// coalescing (tests). syncMu guards the last-pull bookkeeping (pulled/lastPull/
	// pullErr) that backs the session panes' staleness banner.
	pullIvl  time.Duration
	syncMu   sync.Mutex
	pulled   bool
	lastPull time.Time
	pullErr  error

	// streamInterval is the SSE re-read cadence for a live session transcript
	// (sessionStream). 0 means the default (streamIvl); tests set it small to keep
	// them fast.
	streamInterval time.Duration

	// Multi-repo routing (set ONLY on the container returned by NewRegistry; nil
	// on a single-repo Server from New). The container IS one of the per-repo
	// servers — siblings[order[0]], the default active repo — and additionally
	// holds the whole registered set so Routes can dispatch each request to the
	// server for that request's ACTIVE repo (see active/bind). Selection is
	// per-request (the repoCookie), never a mutable field, so it can never leak
	// across concurrent requests. name is this server's registry handle ("" for a
	// single-repo server).
	name     string
	reg      config.Registry
	siblings map[string]*Server // registry name -> per-repo server (incl. the container itself)
	order    []string           // registry names sorted ascending; order[0] is the default active repo
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
	g := git.New(r.Root)
	// The chat-diff editor drives its own opencode client (same server/model as
	// the single-file editor); it opens a per-edit ROOT worktree and awaits each
	// turn (opencode-turn-poll) before rendering the proposed diff.
	oc := &swarm.Opencode{Base: cfg.AgentURL, Model: cfg.Model, Temperature: cfg.Temperature, MaxTokens: cfg.MaxTokens, HTTP: &http.Client{Timeout: 0}}
	chat := newChatManager(r.Root, oc)
	// The resolution agent is a headless tool-using turn, so it needs the same
	// wedge protection the honeybee runner gives its passes: a progress watchdog
	// (IdleTimeout) that cuts a turn stuck on a hung tool call (e.g. an opencode
	// permission elicitation that nothing can answer), plus an absolute per-turn
	// ceiling. Without them a single wedged turn pins the session at "working…"
	// forever. It drives its OWN client so tuning it never perturbs the chat-diff
	// editor's behavior.
	resolveOC := &swarm.Opencode{Base: cfg.AgentURL, Model: cfg.Model, Temperature: cfg.Temperature, MaxTokens: cfg.MaxTokens, HTTP: &http.Client{Timeout: 0}, IdleTimeout: turnIdleTimeout(cfg)}
	return &Server{repo: r, cfg: cfg, git: g, tmpl: t, editors: em, cache: newViewCache(), pullIvl: pullInterval(cfg), chat: chat, humans: newResolveManager(r.Root, resolveOC, turnCeiling(cfg))}, nil
}

// turnIdleTimeout is the resolution agent's per-turn PROGRESS watchdog (a turn
// with no new transcript activity for this long is cut), from config with a
// sane default when unset.
func turnIdleTimeout(cfg config.Config) time.Duration {
	if cfg.TurnIdleTimeoutMinutes > 0 {
		return time.Duration(cfg.TurnIdleTimeoutMinutes) * time.Minute
	}
	return 5 * time.Minute
}

// turnCeiling is the resolution agent's absolute per-turn wall-clock ceiling,
// from config with a sane default when unset.
func turnCeiling(cfg config.Config) time.Duration {
	if cfg.TurnTimeoutMinutes > 0 {
		return time.Duration(cfg.TurnTimeoutMinutes) * time.Minute
	}
	return 20 * time.Minute
}

// repoCookie carries the selected repo handle for a multi-repo daemon. Selection
// lives ENTIRELY in this per-request cookie (set by POST /repo/{name}), never in
// a server field, so concurrent requests each resolve their own active repo and a
// switch can never leak across them. An absent or unregistered cookie falls back
// to the default active repo (the first registered name, sorted).
const repoCookie = "beehive_repo"

// NewRegistry builds the multi-repo frontend: one fully-wired per-repo Server for
// every RepoEntry in reg, each resolved exactly as the daemon contract specifies
// — entry.Config(config.Resolve(entry.Root, "")) for the effective per-repo
// config (that repo's own layered config under the entry's isolated keyring +
// agent overrides) and repo.Open(entry.Root) for the repo. Each per-repo server
// owns its OWN repo/git/config/editors/chat/cache, so repos never share mutable
// state and a request routed to one can never crawl into another.
//
// The returned Server is the container: it IS the default active repo's server
// (siblings[order[0]]) and additionally holds the registry + every sibling, so
// Routes dispatches each request to the server for that request's active repo.
// An empty registry is an error (the daemon always resolves at least a
// synthesized single entry via config.ResolveRegistry). A single-entry registry
// yields flat, unprefixed routes byte-identical to New's single-repo server
// (multi reports false) — the no-regression path.
func NewRegistry(reg config.Registry) (*Server, error) {
	if reg.Empty() {
		return nil, fmt.Errorf("web: empty registry, no repo to serve")
	}
	servers := make(map[string]*Server, len(reg.Repos))
	for _, e := range reg.Repos {
		base, err := config.Resolve(e.Root, "")
		if err != nil {
			return nil, fmt.Errorf("web: resolve config for repo %q: %w", e.Name, err)
		}
		r, err := repo.Open(e.Root)
		if err != nil {
			return nil, fmt.Errorf("web: open repo %q at %s: %w", e.Name, e.Root, err)
		}
		srv, err := New(r, e.Config(base))
		if err != nil {
			return nil, fmt.Errorf("web: build server for repo %q: %w", e.Name, err)
		}
		srv.name = e.Name
		servers[e.Name] = srv
	}
	order := reg.Names() // sorted ascending; order[0] is the default active repo
	container := servers[order[0]]
	container.reg = reg
	container.siblings = servers
	container.order = order
	return container, nil
}

// multi reports whether this server routes more than one registered repo. A
// single-entry (or single-repo New) server is not multi: it keeps flat routes,
// exposes no /repo switch, and every request resolves to this one server.
func (s *Server) multi() bool { return len(s.siblings) > 1 }

// targets is every per-repo server this frontend serves, in sorted registry
// order. A single-repo server is its own only target. Used for cross-repo startup
// housekeeping (RecoverEditors) that must touch every repo, not just the active
// one.
func (s *Server) targets() []*Server {
	if len(s.siblings) == 0 {
		return []*Server{s}
	}
	out := make([]*Server, 0, len(s.siblings))
	for _, name := range s.order {
		out = append(out, s.siblings[name])
	}
	return out
}

// active resolves the per-repo server a request is routed to: the repo named by
// the request's selection cookie when it is a REGISTERED repo, else the default
// (order[0]). A single-repo server always resolves to itself. Resolution reads
// only the request and the immutable registry maps — no shared mutable selection
// state — so concurrent requests never leak a selection across one another.
func (s *Server) active(r *http.Request) *Server {
	if !s.multi() {
		return s
	}
	if c, err := r.Cookie(repoCookie); err == nil {
		if srv, ok := s.siblings[c.Value]; ok {
			return srv
		}
	}
	return s // container == siblings[order[0]], the default active repo
}

// bind adapts a Server handler method to the request's active repo: it resolves
// the active per-repo server per request and invokes the handler on it. Every
// route is wired through bind, so in multi-repo mode a request transparently runs
// against its selected repo and in single-repo mode against the only server — the
// existing handlers are reused unchanged and never crawl across repos.
func (s *Server) bind(h func(*Server, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h(s.active(r), w, r)
	}
}

// repoSwitch selects the active repo for subsequent requests by setting the
// selection cookie, rejecting a switch to an unregistered handle with 404 (never
// trusting an arbitrary name). Registered only in multi-repo mode. The selection
// lives in the client cookie, not a server field, so switching is per-client and
// cannot race concurrent requests.
func (s *Server) repoSwitch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.reg.Repo(name); !ok {
		http.Error(w, "unknown repo "+name, http.StatusNotFound)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     repoCookie,
		Value:    name,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// pullInterval is the resolved follow-the-remote coalescing window (config
// session_pull_seconds), defaulting to 2s when unset. It bounds how often the
// polled session panes actually hit the remote to fast-forward local main.
func pullInterval(cfg config.Config) time.Duration {
	s := cfg.SessionPullSeconds
	if s <= 0 {
		s = 2
	}
	return time.Duration(s) * time.Second
}

// RecoverEditors runs the editor's startup recovery for EVERY served repo: a
// multi-repo daemon must re-register each repo's persisted in-flight edit
// sessions and prune each one's stale/abandoned edit worktrees, not just the
// active repo's (see editor.Manager.Reload). It is best-effort startup
// housekeeping — the daemon calls it once before serving and treats a failure as
// non-fatal — so a recovery hiccup never blocks the frontend from starting.
func (s *Server) RecoverEditors(ctx context.Context) error {
	for _, srv := range s.targets() {
		if err := srv.editors.Reload(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Routes returns the mux wired to all handlers. Every handler is bound through
// s.bind, which dispatches the request to its ACTIVE repo (the selection cookie
// in multi-repo mode, the sole repo otherwise) — so the one handler set serves
// every registered repo without crawling across them. In multi-repo mode the
// POST /repo/{name} switch is additionally registered; a single-repo (or
// single-entry) server keeps exactly today's flat, unprefixed routes.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	b := s.bind
	mux.HandleFunc("GET /{$}", b((*Server).dashboard))
	mux.HandleFunc("GET /bootstrap", b((*Server).bootstrapAgent))
	mux.HandleFunc("GET /stats", b((*Server).stats))
	mux.HandleFunc("GET /submodule/{name}", b((*Server).explorer))
	mux.HandleFunc("GET /submodule/{name}/branches", b((*Server).branches))
	mux.HandleFunc("GET /submodule/{name}/commit/{sha}", b((*Server).commitView))
	mux.HandleFunc("GET /submodule/{name}/doc/{file...}", b((*Server).doc))
	mux.HandleFunc("GET /submodule/{name}/docs", b((*Server).docExplorer))
	mux.HandleFunc("GET /submodule/{name}/plan", b((*Server).plan))
	mux.HandleFunc("POST /submodule/{name}/plan/delete", b((*Server).planDelete))
	mux.HandleFunc("GET /submodule/{name}/sessions", b((*Server).sessionsList))
	mux.HandleFunc("GET /submodule/{name}/sessions/body", b((*Server).sessionsListBody))
	mux.HandleFunc("GET /submodule/{name}/session/{branch}", b((*Server).sessionView))
	mux.HandleFunc("GET /submodule/{name}/session/{branch}/body", b((*Server).sessionBody))
	mux.HandleFunc("GET /submodule/{name}/session/{branch}/stream", b((*Server).sessionStream))
	mux.HandleFunc("GET /roi/{name}", b((*Server).roiGet))
	mux.HandleFunc("POST /roi/{name}", b((*Server).roiPost))
	mux.HandleFunc("GET /secrets", b((*Server).secretsGet))
	mux.HandleFunc("POST /secrets", b((*Server).secretsPost))
	mux.HandleFunc("GET /merge", b((*Server).mergeGet))
	mux.HandleFunc("POST /merge", b((*Server).mergePost))
	mux.HandleFunc("POST /submodule/add", b((*Server).submoduleAdd))
	mux.HandleFunc("POST /submodule/link", b((*Server).submoduleLink))
	// Refresh the managed repo-ROOT instruction files (AGENTS/HONEYBEE/BOOTSTRAP)
	// to the binary's embedded defaults via the SAME installer the CLI uses
	// (internal/instruct.Update, clobber path). LOCALS.md is never touched.
	mux.HandleFunc("POST /instruction/update", b((*Server).instructionUpdate))
	// Blue/green deploy is per-submodule (each target owns its own
	// INFRASTRUCTURE.md state), never a single global env: the panel and the
	// deploy write are scoped to the named submodule.
	mux.HandleFunc("GET /submodule/{name}/env", b((*Server).envGet))
	mux.HandleFunc("POST /submodule/{name}/env/deploy", b((*Server).envDeploy))
	mux.HandleFunc("GET /human", b((*Server).human))
	mux.HandleFunc("GET /human/{sub}/{id}", b((*Server).humanResolvePage))
	mux.HandleFunc("GET /human/{sub}/{id}/panel/{sid}", b((*Server).humanResolvePanel))
	mux.HandleFunc("POST /human/{sub}/{id}/message/{sid}", b((*Server).humanResolveMessage))
	mux.HandleFunc("POST /human/{sub}/{id}/publish/{sid}", b((*Server).humanResolvePublish))
	mux.HandleFunc("POST /human/{sub}/{id}/discard/{sid}", b((*Server).humanResolveDiscard))
	mux.HandleFunc("POST /human/{sub}/{id}/resolve", b((*Server).humanResolveApply))
	mux.HandleFunc("GET /hygiene", b((*Server).hygiene))
	// Maintenance skills: an index of named actions each with a read-only dry-run
	// (plan) and a separate apply; destructive skills gate apply on confirm.
	mux.HandleFunc("GET /skills", b((*Server).skillsPage))
	mux.HandleFunc("POST /skills/{name}/plan", b((*Server).skillPlanHandler))
	mux.HandleFunc("POST /skills/{name}/apply", b((*Server).skillApplyHandler))
	// AI editor chat (browser): one worktree branch per session. GET /edit is
	// the ONE entry point for every edit-with-AI link (dashboard/explorer/
	// roi_editor) and always opens the publish-capable internal/editor Manager
	// (editEntry -> editNew), never chatManager (ai-edit-publish-to-main). The
	// /edit/{id}/panel|message|approve|reject fragments remain registered
	// because they ALSO back the bootstrap wizard's embedded agent (its own
	// fixed LOCALS.md session, opened only via GET /bootstrap) — chatManager's
	// generic, never-published full-page /edit/{id} view and its POST /edit
	// open are retired along with it.
	mux.HandleFunc("GET /edit", b((*Server).editEntry))
	mux.HandleFunc("GET /edit/{id}/panel", b((*Server).chatPanel))
	mux.HandleFunc("POST /edit/{id}/message", b((*Server).chatMessage))
	mux.HandleFunc("POST /edit/{id}/approve", b((*Server).chatApprove))
	mux.HandleFunc("POST /edit/{id}/reject", b((*Server).chatReject))
	mux.HandleFunc("GET /editor/{id}", b((*Server).editorPage))
	mux.HandleFunc("GET /editor/{id}/panel", b((*Server).editorPanel))
	mux.HandleFunc("POST /editor/{id}/chat", b((*Server).editorChat))
	mux.HandleFunc("POST /editor/{id}/merge", b((*Server).editorMerge))
	mux.HandleFunc("POST /editor/{id}/close", b((*Server).editorClose))
	// AI editor chat (JSON API): browser-free clients.
	mux.HandleFunc("POST /api/editor", b((*Server).apiEditorOpen))
	mux.HandleFunc("GET /api/editor/{id}", b((*Server).apiEditorGet))
	mux.HandleFunc("POST /api/editor/{id}/chat", b((*Server).apiEditorChat))
	mux.HandleFunc("POST /api/editor/{id}/merge", b((*Server).apiEditorMerge))
	mux.HandleFunc("GET /api/editor/{id}/diff", b((*Server).apiEditorDiff))
	mux.Handle("GET /assets/", http.FileServer(http.FS(assetFS)))
	// Conventional favicon path: some clients request it directly regardless of
	// layout.html's <link rel="icon">. Unbound like /assets/ above — the asset is
	// baked into the binary and identical for every registered repo.
	mux.HandleFunc("GET /favicon.ico", faviconICO)
	// Multi-repo selection: switch the active repo for subsequent requests. Only
	// registered in multi-repo mode, so a single-repo daemon keeps today's flat
	// routes with no extra endpoint.
	if s.multi() {
		mux.HandleFunc("POST /repo/{name}", s.repoSwitch)
	}
	return mux
}

// editEntry (GET /edit) is the ONE HTTP entry point every edit-with-AI link
// reaches (dashboard's "edit infrastructure with AI"/"edit roi (AI)", the
// explorer's per-file view/edit/create links, roi_editor's "edit with AI
// chat") — it always opens through editNew, the publish-capable
// internal/editor Manager (internal/web/editor.go), whose Merge lands an
// approved change on main (ai-edit-publish-to-main). Before this fix a
// path-carrying request (every real link passes one, e.g. ?path=ROI.md) was
// dispatched to chatManager instead: that surface's approve committed to a
// throwaway edit-* branch and had no publish step at all, so the change never
// reached main and, for a submodule ROI.md, the PLAN's Beehive-ROI stamp never
// advanced past it — silently discarding the edit. chatManager is not
// reachable from here anymore; its one surviving caller is the bootstrap
// wizard's fixed LOCALS.md session, opened directly by openBootstrap
// (bootstrap.go) via GET /bootstrap, never through this entry point.
func (s *Server) editEntry(w http.ResponseWriter, r *http.Request) {
	s.editNew(w, r)
}

func (s *Server) render(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// renderConditional is render's poll-friendly sibling: it executes the named
// template into memory and serves the result under a strong ETag computed from
// its own rendered bytes (fragmentETag, cache.go), replying "304 Not Modified"
// with an empty body when the request's If-None-Match already names that tag.
// A polled htmx fragment (session transcripts, editor/chat panels, the human
// resolve panel — every one on a 1.5-2s hx-trigger) otherwise re-renders and
// re-transfers its FULL body every tick even when byte-identical to what the
// browser already holds; this lets a client that already has the current bytes
// skip the retransfer entirely (poll-fragment-etag-304). Unlike headSHA/
// viewCache (which key on the repo HEAD alone), hashing the actual rendered
// output stays correct for panes whose content can change on the wall clock
// with no new commit — see fragmentETag's doc. Template errors still surface
// as a 500 exactly as render's do; only a successfully rendered fragment ever
// reaches the ETag path.
func (s *Server) renderConditional(w http.ResponseWriter, r *http.Request, name string, data interface{}) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeConditional(w, r, buf.Bytes(), "text/html; charset=utf-8")
}

// writeConditional serves body under a strong ETag (fragmentETag), replying
// "304 Not Modified" with NO body when the request's If-None-Match already
// names the current tag — the conditional-GET short-circuit RFC 7232 defines.
// ETag and Cache-Control are set on BOTH outcomes (a client tracking the
// validator must see them either way); Content-Type only on the 200, since a
// 304 MUST NOT carry a representation or the headers that describe one (RFC
// 7230 §3.3.2). Cache-Control is "no-cache" (may be stored, but MUST always be
// revalidated with the server) rather than absent or "no-store": that is what
// makes the payoff automatic for a plain htmx poll — the BROWSER's own HTTP
// cache attaches If-None-Match for us and, on a 304, hands the XHR layer the
// previously cached 200 body without htmx ever seeing a raw 304 in the common
// case (layout.html's htmx:beforeSwap guard covers the uncommon case where it
// does, so an empty 304 body can never blank the pane either way).
func writeConditional(w http.ResponseWriter, r *http.Request, body []byte, contentType string) {
	etag := fragmentETag(body)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if etagMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Write(body)
}

// etagMatch reports whether etag is named in an If-None-Match request header,
// which RFC 7232 §3.2 allows to be "*" (matches any current representation) or
// a comma-separated list of entity-tags, each optionally weak ("W/"-prefixed).
// The comparison is the weak comparison function §2.3.2 mandates for
// If-None-Match: the opaque quoted value only, ignoring a "W/" prefix on
// either side — so a cache that downgraded our strong tag to weak still gets
// its 304.
func etagMatch(header, etag string) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	if header == "*" {
		return true
	}
	want := strings.TrimPrefix(etag, "W/")
	for _, part := range strings.Split(header, ",") {
		if strings.TrimPrefix(strings.TrimSpace(part), "W/") == want {
			return true
		}
	}
	return false
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

// subView is the dashboard's per-submodule card, all derived live from files
// each request (no cache). State is the swarm status (active/dormant/bootstrap);
// Pending/Human are task counts from the unified parser (internal/plan) so they
// match what the runner/selector see; Env is the submodule's active blue/green
// deploy env from the typed artifacts model ("" when it has no
// INFRASTRUCTURE.md); Working is true when a honeybee is actively working this
// submodule, driving the card's live overlay; Bees is HOW MANY honeybees are
// actively working it right now (0 when idle, and Working is exactly Bees > 0),
// surfaced on the card with the 🐝 badge. Both come from the ONE canonical set
// activeHoneybees derives (active-honeybee-count-unify): a fresh PLAN-task
// claim UNIONED with a live session with no claim (a Bootstrap/Reconcile pass,
// which claims no task) — never a task's status, and never a divergent,
// dashboard-only rule.
type subView struct {
	Name    string
	State   string
	Stamp   string
	Pending int
	Human   int
	Env     string
	Working bool
	Bees    int
}

// EnvClass is the design-system badge hue modifier for the active deploy env:
// env-blue / env-green for the standard blue/green envs, "" (a neutral badge)
// for any other or absent env. Resolving it in Go bounds the class names the
// template can emit to a known set rather than interpolating a raw file value
// into the class attribute.
func (v subView) EnvClass() string {
	switch v.Env {
	case "blue":
		return "env-blue"
	case "green":
		return "env-green"
	default:
		return ""
	}
}

// headSHA resolves the beehive repo HEAD short SHA used to key the parse cache,
// returning "" when HEAD is unresolvable (a repo with no commits yet) so callers
// read through uncached rather than failing. Resolve it ONCE per request and
// share it across every submodule's cached read: a multi-submodule dashboard
// then pays a single `git rev-parse`, not one per submodule, and every card in
// that render sees a coherent commit snapshot.
func (s *Server) headSHA(ctx context.Context) string {
	h, err := s.git.Head(ctx)
	if err != nil {
		return ""
	}
	return h
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	views, err := s.subViews(r.Context(), time.Now(), s.ttl())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// No global "active env": blue/green is per-submodule. Each card's Env comes
	// from that submodule's OWN INFRASTRUCTURE.md (subViews), and the deploy panel
	// is scoped per submodule (/submodule/{name}/env) — the dashboard never reads a
	// single hive-wide deploy state.
	// Read-only hygiene summary alongside the submodule cards. A scan error is
	// surfaced inside the widget (not swallowed) rather than failing the whole
	// dashboard, which is the operator's primary page.
	hyg, err := scanHygiene(r.Context(), s.repo.Root, s.git)
	if err != nil {
		hyg = Hygiene{Skill: hygieneSkill, Err: err.Error()}
	}
	rootFiles := s.rootFileLinks()
	// No "Title": the dashboard is the root page, and layout.html's own
	// {{if .Title}} falls back to the bare site name — the one page that
	// demonstrates the fallback rather than composing one via pageTitle.
	s.render(w, "dashboard.html", map[string]interface{}{"Subs": views, "Hygiene": hyg, "Bootstrap": s.bootstrapState(), "RootFiles": rootFiles, "RootFilesDrift": rootFilesDrift(rootFiles), "Nav": "dashboard"})
}

// subViews builds the dashboard card data for every submodule: State
// (active/dormant/bootstrap), the ROI Stamp, the Pending/Human task counts from
// the unified parser (internal/plan via planView — the same parse the
// runner/selector use), the active blue/green Env from the submodule's own
// INFRASTRUCTURE.md via the typed artifacts model, and Working/Bees from
// activeHoneybees — the canonical active-honeybee set (active-honeybee-count-
// unify), so the dashboard can never diverge from the sessions page/list or
// /stats. now/ttl are passed in so the claim-freshness derivation is
// deterministically testable; the handler supplies time.Now() and the resolved
// TTL.
//
// The PLAN.md read+parse is memoized per repo HEAD via planView, but the swarm
// status stays current: the counts and Working flag are re-projected against
// now/ttl every call (so a claim still goes stale on TTL expiry with no new
// commit), and any file change advances HEAD and drops the cache. ctx carries
// the request's cancellation/deadline into the HEAD lookup and into
// activeHoneybees' own git reads (branch existence for a claimless live
// session).
func (s *Server) subViews(ctx context.Context, now time.Time, ttl time.Duration) ([]subView, error) {
	subs, err := s.repo.Submodules()
	if err != nil {
		return nil, err
	}
	// Resolve HEAD once for the whole render: every submodule's cached parse is
	// keyed by this one generation, so the dashboard pays a single rev-parse and
	// all cards reflect the same commit.
	head := s.headSHA(ctx)
	var views []subView
	for _, sm := range subs {
		v := subView{Name: sm.Name, State: "active"}
		switch {
		case sm.Dormant():
			v.State = "dormant"
		case sm.NeedsBootstrap():
			v.State = "bootstrap"
		}
		// The ROI stamp and the task counts both come from the SAME cached
		// PLAN.md parse (p.ROIStamp is plan.Parse().ROI — the exact value
		// Submodule.ROIStamp scans PLAN.md for), so the dashboard reads each
		// PLAN.md once per generation instead of twice. Counts: Pending = every
		// task not DONE, Human = NEEDS-HUMAN only. A NEEDS-HUMAN task is BOTH
		// pending and human — the two counters legitimately overlap, but each task
		// increments each counter at most once (never double-counted within a
		// counter). Working/Bees come from activeHoneybees, the canonical
		// active-honeybee set (active-honeybee-count-unify): a fresh PLAN-task
		// claim UNIONED with a live claimless session (Bootstrap/Reconcile, which
		// claims no task), deduped by session id — so Bees also counts a
		// reconcile pass in flight, not just claimed tasks, and Working is
		// exactly Bees > 0. A parse error leaves this submodule's stamp/counts
		// empty rather than failing the whole dashboard (mirrors the
		// pre-existing per-submodule resilience).
		if p, err := s.planView(head, sm.PlanPath(), now, ttl); err == nil {
			v.Stamp = p.ROIStamp
			for _, it := range p.Items {
				if it.Status != StatusDone {
					v.Pending++
				}
				if it.Status == StatusHuman {
					v.Human++
				}
			}
			v.Bees = len(s.activeHoneybees(ctx, sm, p))
			v.Working = v.Bees > 0
		}
		// Env badge: the submodule's own blue/green deploy state via the typed
		// artifacts model. An absent INFRASTRUCTURE.md leaves Env "" (no badge)
		// instead of a misleading default; a read error is surfaced as no badge
		// too (best-effort overview, never a dashboard-wide failure).
		if in, err := artifacts.LoadInfra(filepath.Join(sm.Path, repo.InfraFile)); err == nil && in.Present() {
			v.Env = in.Deployment().Active
		}
		views = append(views, v)
	}
	return views, nil
}

// fileLink is one row of the explorer's optional-file index: a view/edit (or,
// when absent, create) link for one member of the KNOWN per-submodule optional-
// file set (repo.OptionalFiles). The index is driven by that DECLARED set, not by
// the directory listing, so an absent file renders a discoverable create link
// instead of being invisible (optional-file-links). File is the basename and the
// template composes the repo-relative editor path submodules/<name>/<File>; the
// chat-diff editor (chat-diff-editor-core) opens a present file on its current
// contents (view+edit) and an absent one on an EMPTY base (create), seeded per
// file via chat-diff-file-context — including ROI.md, which stays human-owned and
// is therefore never auto-generated (the editor only writes on human approval).
type fileLink struct {
	Label   string // display name, e.g. "INFRASTRUCTURE" (basename minus .md)
	File    string // basename, e.g. "INFRASTRUCTURE.md"
	Present bool   // file exists on disk in this submodule
}

// optionalFileLinks builds the explorer's optional-file index for sm from the
// DECLARED set repo.OptionalFiles (never the disk listing), stamping each member
// present/absent by a plain existence check so a missing file still yields a row.
func optionalFileLinks(sm repo.Submodule) []fileLink {
	links := make([]fileLink, 0, len(repo.OptionalFiles))
	for _, f := range repo.OptionalFiles {
		_, err := os.Stat(filepath.Join(sm.Path, f))
		links = append(links, fileLink{
			Label:   strings.TrimSuffix(f, ".md"),
			File:    f,
			Present: err == nil,
		})
	}
	return links
}

// rootFileLink is one row of the dashboard's repo-ROOT instruction-file index: a
// uniform view/edit (or, when absent, create) link for one member of the DECLARED
// set repo.RootInstructionFiles (AGENTS.md, HONEYBEE.md, BOOTSTRAP.md, LOCALS.md).
// It is the root analogue of fileLink: the index is driven by that SET, not the
// directory listing, so an absent file renders a discoverable create link (into
// the chat-diff editor's empty-base create path, seeded per file via
// chat-diff-file-context) instead of being invisible (root-instruction-file-
// links). File is the basename and the template composes the repo-ROOT editor path
// /edit?path=<File> (no submodules/<name>/ prefix — these live at the repo root).
// Managed exposes whether beehive ships/refreshes a default for the file (the
// signal instruction-update-drift keys off); LOCALS.md is site-authored, so it is
// never managed and never auto-generated (create still routes through the same
// approval-gated editor). Drift is the read-only staleness of a MANAGED file
// against the binary's embedded default ("clean" | "drift" | "missing"), empty for
// the site-authored LOCALS.md which is categorically excluded from the drift check
// (instruction-update-drift).
type rootFileLink struct {
	Label   string // display name, e.g. "AGENTS" (basename minus .md)
	File    string // basename at the repo root, e.g. "AGENTS.md"
	Present bool   // file exists on disk at the repo root
	Managed bool   // beehive ships/refreshes a default (vs. site-authored)
	Drift   string // "" (unmanaged) | "clean" | "drift" | "missing"
}

// driftLabel maps an instruct.Status to the drift vocabulary the dashboard badge
// shows: a file byte-identical to the embedded default is "clean", one that exists
// but differs has "drift"ed, an absent one is "missing". It is the presentation
// mapping for the managed root files; the CLI keeps instruct's own "modified" word.
func driftLabel(st instruct.Status) string {
	switch st {
	case instruct.Clean:
		return "clean"
	case instruct.Modified:
		return "drift"
	case instruct.Missing:
		return "missing"
	default:
		return ""
	}
}

// rootFileLinks builds the dashboard's repo-ROOT instruction-file index from the
// DECLARED set repo.RootInstructionFiles (never the disk listing), stamping each
// member present/absent by a plain existence check at the repo root so a missing
// file (e.g. an unwritten LOCALS.md) still yields a discoverable row. It is read
// fresh each render, so a plain manual commit that lands a root file on disk flips
// its row to present on the next dashboard load with no special write path.
//
// For every MANAGED member it additionally attaches a read-only Drift status —
// "clean" | "drift" | "missing" — by comparing the on-disk file against the
// binary's embedded default through the SAME source the CLI uses (instruct), so a
// stale managed file surfaces a badge and an operator can run the update. The
// site-authored LOCALS.md is categorically excluded (Managed=false => no drift
// check, no badge). A per-file scan read error leaves Drift empty (best-effort
// overview, never a dashboard-wide failure), mirroring Present's tolerance.
func (s *Server) rootFileLinks() []rootFileLink {
	links := make([]rootFileLink, 0, len(repo.RootInstructionFiles))
	for _, f := range repo.RootInstructionFiles {
		_, err := os.Stat(filepath.Join(s.repo.Root, f.File))
		l := rootFileLink{
			Label:   strings.TrimSuffix(f.File, ".md"),
			File:    f.File,
			Present: err == nil,
			Managed: f.Managed,
		}
		if f.Managed {
			if st, ok, serr := instruct.StatusOf(s.repo.Root, f.File); ok && serr == nil {
				l.Drift = driftLabel(st)
			}
		}
		links = append(links, l)
	}
	return links
}

// rootFilesDrift reports whether ANY managed root file has drifted from or is
// missing versus the binary's embedded default, i.e. whether `instruction update`
// has anything to do. The dashboard uses it to emphasize the update action only
// when it would change something (a clean set shows the action as idempotent).
func rootFilesDrift(links []rootFileLink) bool {
	for _, l := range links {
		if l.Drift == "drift" || l.Drift == "missing" {
			return true
		}
	}
	return false
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
	// PLAN and ROI render their raw markdown (PLAN's structure lives in
	// internal/plan; ROI is human-owned and edited verbatim). AGENTS.md is the
	// per-submodule rules overlay and RULES.md is the beehive-owned overlay
	// ADDITIVE to it — both are shown when present (the explorer's docs map is
	// rendered by sorted key, so "AGENTS" precedes "RULES": the AGENTS-then-RULES
	// order the agent context also applies). os.ReadFile only populates a label on
	// success, so an absent file is silently skipped — RULES.md's absence is a
	// safe no-op.
	for label, f := range map[string]string{
		"PLAN":   repo.PlanFile,
		"ROI":    repo.ROIFile,
		"AGENTS": repo.AgentsFile,
		"RULES":  repo.RulesFile,
	} {
		if b, err := os.ReadFile(filepath.Join(sm.Path, f)); err == nil {
			docs[label] = renderMarkdown(string(b))
		}
	}
	// INFRASTRUCTURE.md and ARTIFACTS.md are read through internal/artifacts (the
	// typed model) instead of raw text: the rendered HTML is the model's
	// round-tripped serialization, and the same parse feeds the dashboard env
	// badge. An absent doc is skipped (Present()==false).
	if in, err := artifacts.LoadInfra(filepath.Join(sm.Path, repo.InfraFile)); err == nil && in.Present() {
		docs["INFRA"] = renderMarkdown(in.String())
	}
	if a, err := artifacts.LoadArtifacts(filepath.Join(sm.Path, repo.Artifacts)); err == nil && a.Present() {
		docs["ARTIFACTS"] = renderMarkdown(a.String())
	}
	// The optional-file index (optional-file-links) renders view/edit/create links
	// for the FULL known optional-file set UNIFORMLY, present or absent, so a file
	// that does not exist yet is still discoverable (the docs map above only shows
	// present files' rendered content — the index is what makes an absent member
	// reachable). Driven by the declared set, not the directory listing.
	s.render(w, "explorer.html", map[string]interface{}{
		"Name":  sm.Name,
		"Docs":  docs,
		"Files": optionalFileLinks(sm),
		"Title": pageTitle(sm.Name),
	})
}

func (s *Server) branches(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	off, lim := pageParams(r)
	// Scoped to this ONE submodule's checkout: commitGraph reads only
	// sm.RepoDir(), so the view never crawls across submodules.
	cs, err := commitGraph(r.Context(), sm.RepoDir(), off, lim)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// delivery-traceability half (a): for each row whose Beehive stamp names a
	// DONE task, link to the hive commit that flipped it (buildDeliveries also
	// covers half (b), but DocHref below already carries that unchanged).
	deliveries := indexDeliveries(s.buildDeliveries(r.Context(), s.headSHA(r.Context()), sm, doneTaskIDs(sm)))
	for i := range cs {
		cs[i].DocHref = resolveDocHref(sm, cs[i].DocPath)
		if d, ok := deliveries[cs[i].DocTask]; ok {
			cs[i].FlipSHA = d.FlipSHA
			cs[i].FlipHref = d.FlipHref
		}
	}
	prev := off - lim
	if prev < 0 {
		prev = 0
	}
	s.render(w, "branch_view.html", map[string]interface{}{
		"Name":     sm.Name,
		"Sections": sectionByDate(cs),
		"Prev":     prev,
		"HasPrev":  off > 0,
		"Next":     off + lim,
		"HasNext":  len(cs) == lim, // a full page may have more
		"Title":    pageTitle("commits", sm.Name),
	})
}

// doc renders one of a submodule's Beehive docs
// (submodules/<name>/docs/<path>, which may nest under docs/audit/ or
// docs/tasks/) as sanitized markdown. It backs both the branch view's
// commit-stamp links (a flat basename) and the doc explorer's whole-tree
// listing (submodule-doc-explorer, a possibly nested path), so both routes
// resolve to the SAME viewer. The {file...} wildcard captures the remaining
// URL segments; path is validated by safeDocPath (traversal-guarded, every
// segment charset-checked) and the read is scoped to that single submodule's
// docs/ dir (never another submodule, never outside docs/).
func (s *Server) doc(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	file := r.PathValue("file")
	if !safeDocPath(file) {
		http.NotFound(w, r)
		return
	}
	b, err := os.ReadFile(filepath.Join(sm.Path, "docs", filepath.FromSlash(file)))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "doc_view.html", map[string]interface{}{
		"Name": sm.Name, "File": file, "Body": renderMarkdown(string(b)),
		"Title": pageTitle(file, sm.Name),
	})
}

// docExplorer lists EVERY file under a submodule's docs/ tree — change docs,
// docs/audit/ audit reports, docs/tasks/ task design docs, and anything else
// written there — each linked through the existing doc viewer (doc/{file...}
// above). It is distinct from explorer (routed at /submodule/{name}, no
// trailing "s"): that page renders the FIXED known-optional-file set (ROI.md/
// INFRASTRUCTURE.md/RULES.md/etc.); this walks the actual docs/ directory, so
// a doc no commit's Beehive stamp happens to point at (an audit report, a task
// design doc) is still discoverable instead of reachable only through a
// task/branch row. Strictly read-only and scoped to this ONE submodule's
// docs/ dir (docTree never crawls another submodule).
func (s *Server) docExplorer(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	entries, err := docTree(sm)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "doc_explorer.html", map[string]interface{}{
		"Name":     sm.Name,
		"Sections": sectionDocs(entries),
		"Title":    pageTitle("docs", sm.Name),
	})
}

func (s *Server) plan(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, err := s.planView(s.headSHA(r.Context()), sm.PlanPath(), time.Now(), s.ttl())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Link each task to a viewable doc — never inert when one is locatable
	// (plan-view-detail-polish's "none inert" fix, auditing plan-view-pills'
	// resolution against delivery-traceability's): first the change doc its
	// implementing commit stamped (Beehive: <taskid> <docpath>, scanned once
	// for this submodule's history), falling back to the task's own planned
	// "Doc:" design-doc convention line when THAT resolves to a real file
	// (e.g. a still-in-flight task's docs/tasks/<id>.md design doc). Both
	// paths go through resolveDocHref (traversal + existence guarded), so a
	// row with neither locatable is still never a dead link.
	docs := changeDocsByTask(r.Context(), sm.RepoDir())
	for i := range p.Items {
		p.Items[i].DocHref = resolveDocHref(sm, docs[p.Items[i].ID])
		if p.Items[i].DocHref == "" && p.Items[i].Doc != "" {
			p.Items[i].DocHref = resolveDocHref(sm, p.Items[i].Doc)
		}
	}
	s.render(w, "plan_items.html", map[string]interface{}{"Name": sm.Name, "Plan": p, "Title": pageTitle("plan", sm.Name)})
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
	if err := s.publishMain(r.Context(), "frontend: delete PLAN "+sm.Name+" (force rebootstrap from ROI)"); err != nil {
		http.Error(w, "deleted locally but publish to remote failed: "+err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/submodule/"+sm.Name+"/plan", http.StatusSeeOther)
}

// publishMain is the single write path every mutating frontend handler uses: it
// records the working-tree change on the beehived primary checkout's main AND
// propagates it to the remote so other hosts' honeybees (which branch off
// origin/main) see it. It stage-and-commits everything (git add -A) under msg,
// then pushes. An empty commit is not an error — an idempotent write (an
// already-merged merge, an unchanged file) stages nothing and still succeeds. No
// remote => single-host: the local commit is the whole publish (honeybees branch
// off local main) and the push is skipped. On a non-fast-forward (a peer advanced
// the remote under us) it fetches, merges the advanced branch in — never
// clobbering the peer's commit — and retries the push once; a real merge conflict
// is aborted (checkout left clean) and surfaced.
func (s *Server) publishMain(ctx context.Context, msg string) error {
	// Serialize against the follow-the-remote pull and other frontend writes: all
	// touch the primary checkout's index/refs (index.lock).
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	return s.publishMainLocked(ctx, msg)
}

// publishMainLocked is publishMain's body; the CALLER MUST ALREADY HOLD gitMu. It
// lets a handler that itself mutates the primary checkout under the lock — e.g. the
// instruction-update installer, which writes AND commits — commit-and-publish
// atomically without dropping the lock between its own commit and the push (a drop
// would open a window for the follow-remote pull to race the index).
func (s *Server) publishMainLocked(ctx context.Context, msg string) error {
	if err := s.git.Commit(ctx, msg); err != nil && !errors.Is(err, git.ErrNothing) {
		return err
	}
	remote, err := s.git.Remote(ctx)
	if err != nil || remote == "" {
		return err
	}
	// Push the primary checkout's own branch (resolved, not hardcoded "main") so
	// the publish tracks whatever branch the checkout is on.
	branch, err := s.git.CurrentBranch(ctx)
	if err != nil {
		return err
	}
	if err := s.git.Push(ctx, remote, branch); err == nil {
		return nil
	}
	if err := s.git.Fetch(ctx, remote, branch); err != nil {
		return err
	}
	if _, err := s.git.Run(ctx, "merge", "--no-edit", "FETCH_HEAD"); err != nil {
		_, _ = s.git.Run(ctx, "merge", "--abort")
		return err
	}
	return s.git.Push(ctx, remote, branch)
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
		"Title": pageTitle("roi", sm.Name),
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
	if err := s.publishMain(r.Context(), "frontend: edit ROI "+sm.Name); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "roi_editor.html", map[string]interface{}{"Name": sm.Name, "Body": body, "Saved": true, "Rendered": renderMarkdown(body), "Title": pageTitle("roi", sm.Name)})
}

// secretsGet lists the ACTIVE repo's secret KEYS (never values). s is the
// per-request active repo's server (resolved by bind/active), so s.cfg.GPGHome is
// that repo's OWN isolated keyring and s.repo.Root its own SECRETS.yaml.gpg — a
// request can only ever read the selected repo's secrets, never a sibling's.
func (s *Server) secretsGet(w http.ResponseWriter, r *http.Request) {
	keys, err := listSecretKeys(r.Context(), s.cfg.GPGHome, filepath.Join(s.repo.Root, repo.SecretsFile))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "secrets_panel.html", map[string]interface{}{"Keys": keys, "Title": pageTitle("secrets"), "Nav": "secrets"})
}

// secretsPost writes one key into the ACTIVE repo's SECRETS.yaml.gpg. Like
// secretsGet, s is the active repo's server, so the write is encrypted to that
// repo's OWN recipient under its OWN keyring (s.cfg.GPGHome/GPGRecipient) and
// lands in its OWN root — the per-repo keyring isolation the registry guarantees.
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
	if err := s.publishMain(r.Context(), "frontend: update secret "+key); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.secretsGet(w, r)
}

func (s *Server) mergeGet(w http.ResponseWriter, r *http.Request) {
	s.renderMerge(w, nil)
}

// renderMerge renders the merge panel fragment, always injecting the live
// submodule list and a (possibly empty) Selected name so the template's
// preselect comparison is type-safe. Extra keys (Merged/Branch/Tracked) drive
// the post-merge success banner.
func (s *Server) renderMerge(w http.ResponseWriter, data map[string]interface{}) {
	if data == nil {
		data = map[string]interface{}{}
	}
	if _, ok := data["Selected"]; !ok {
		data["Selected"] = ""
	}
	subs, _ := s.repo.Submodules()
	data["Subs"] = subs
	data["Title"] = pageTitle("merge")
	data["Nav"] = "merge"
	s.render(w, "merge_panel.html", data)
}

// mergePost publishes a merge end-to-end rather than merging locally and
// stopping. It merges the chosen branch into the submodule's tracked branch
// (resolved from .gitmodules, never a hardcoded "main"), pushes that branch to
// the submodule's origin, then advances + commits the beehive pointer and
// publishes the beehive root through the same shared write path the other
// mutating handlers use (publishMain: commit -> push). A conflict is aborted
// cleanly (origin and pointer untouched) and returns 409; an already-merged
// branch moves nothing and is still a success (idempotent).
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
	ctx := r.Context()
	tracked := s.trackedBranch(ctx, sm)
	g := git.New(sm.RepoDir())
	// Merge INTO the tracked branch: check it out first so the merge advances that
	// branch ref itself (submodule checkouts are frequently left detached), which
	// is exactly what the subsequent push publishes.
	if _, err := g.Run(ctx, "checkout", tracked); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := g.Merge(ctx, branch); err != nil {
		if errors.Is(err, git.ErrConflict) {
			// Abort the conflicted merge so the checkout (and origin) are left
			// exactly as found — no partial publish.
			_, _ = g.Run(ctx, "merge", "--abort")
			http.Error(w, "merge conflict", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	// Publish the merged tracked branch to the submodule's origin (branch-tracking
	// model) so other clones and the recorded pointer agree. No remote => a
	// single-host target with nothing to push. A failed push stops here, BEFORE
	// the pointer is advanced, so the recorded pointer never points past origin.
	remote, err := g.Remote(ctx)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if remote != "" {
		if err := g.Push(ctx, remote, tracked); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	// Advance + commit the beehive pointer (stages the submodule gitlink) and
	// publish the beehive root, reusing the same path planDelete/roiPost use. An
	// already-merged branch stages nothing (publishMain tolerates it) and is still
	// a success.
	if err := s.publishMain(ctx, "frontend: merge "+branch+" into "+tracked+" ("+name+")\n\nBeehive: frontend-merge "+name); err != nil {
		http.Error(w, "merged locally but publish to remote failed: "+err.Error(), 500)
		return
	}
	s.renderMerge(w, map[string]interface{}{
		"Selected": name, "Merged": true, "Branch": branch, "Tracked": tracked,
	})
}

// trackedBranch resolves the submodule's tracked branch from .gitmodules
// (submodule.<path>.branch) — the branch the beehive pointer follows, the same
// lookup `beehive submodule sync` uses. It defaults to "main" only when the entry
// is unset, so the merge target is never hardcoded.
func (s *Server) trackedBranch(ctx context.Context, sm repo.Submodule) string {
	rel := filepath.Join("submodules", sm.Name, "repo")
	branch, err := s.git.Run(ctx, "config", "-f", ".gitmodules", "submodule."+rel+".branch")
	if err != nil || branch == "" {
		return "main"
	}
	return branch
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
	if err := s.publishMain(r.Context(), "frontend: add submodule "+added); err != nil {
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
	if err := s.publishMain(r.Context(), "frontend: link "+from+" -> "+to); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// instructionUpdate runs `beehive instruction update` from the frontend. It
// invokes the SAME shared installer the CLI uses (internal/instruct.Update) — it
// never shells out and holds no second copy of the defaults — over the managed
// repo-ROOT instruction files (AGENTS/HONEYBEE/BOOTSTRAP; the site-authored
// LOCALS.md is not in the managed set, so it is never written, backed up, or
// reported). It uses the clobber path (true): a clean file is a no-op, a missing
// one is created, and a MODIFIED one is backed up to <name>.<epoch>.bak and
// replaced, with both the backup and the refreshed copy committed. The whole
// update+publish runs under a single gitMu hold via publishMainLocked so
// instruct.Update's own commit cannot race the follow-remote pull, and the commit
// is propagated to the remote (a no-op on a single-host hive). Idempotent: a second
// run over an already-clean set writes nothing and produces no new backup.
func (s *Server) instructionUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s.gitMu.Lock()
	_, err := instruct.Update(ctx, s.repo.Root, true, nil)
	if err == nil {
		err = s.publishMainLocked(ctx, "frontend: beehive instruction update")
	}
	s.gitMu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// envGet renders one submodule's blue/green deploy panel from its OWN
// INFRASTRUCTURE.md (submodules/<name>/INFRASTRUCTURE.md) — never a global env.
// The panel carries the submodule name so its deploy form posts back to the same
// scoped route.
func (s *Server) envGet(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	env, _ := parseEnv(filepath.Join(sm.Path, repo.InfraFile))
	s.render(w, "env_panel.html", map[string]interface{}{"Name": sm.Name, "Env": env, "Title": pageTitle("env", sm.Name)})
}

// envDeploy switches ONE submodule's active env, writing only that submodule's
// INFRASTRUCTURE.md (submodules/<name>/INFRASTRUCTURE.md) so a deploy on one
// target never touches another's deploy state. It resolves the submodule from the
// path, deploys, publishes, then re-renders that submodule's scoped panel.
func (s *Server) envDeploy(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target := r.FormValue("target")
	if target == "" {
		http.Error(w, "target required", 400)
		return
	}
	if err := deploy(filepath.Join(sm.Path, repo.InfraFile), target); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := s.publishMain(r.Context(), "frontend: deploy "+sm.Name+" "+target); err != nil {
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
	head := s.headSHA(r.Context())
	for _, sm := range subs {
		p, _ := s.planView(head, sm.PlanPath(), time.Now(), s.ttl())
		for _, it := range p.Items {
			if it.Status == StatusHuman {
				rows = append(rows, row{Sub: sm.Name, Item: it})
			}
		}
	}
	s.render(w, "human.html", map[string]interface{}{"Rows": rows, "Title": pageTitle("human"), "Nav": "human"})
}

// hygiene renders the standalone read-only hive-hygiene page: a full scan of the
// git cruft that accumulates under updateInstead (stale worktrees, orphan
// submodule gitlinks, drifted submodule checkouts, unexpected remotes), counts
// with a drill-down, and the beehive-hygiene cleanup skill as the remediation
// pointer. It also surfaces the frontend's own view-cache health (CacheWidget) —
// a second, unrelated diagnostic that shares this "operational health,
// diagnostic only" page. The handler is strictly diagnostic — scanHygiene
// mutates nothing and cacheWidget only reads s.cache's counters.
func (s *Server) hygiene(w http.ResponseWriter, r *http.Request) {
	hyg, err := scanHygiene(r.Context(), s.repo.Root, s.git)
	if err != nil {
		hyg = Hygiene{Skill: hygieneSkill, Err: err.Error()}
	}
	s.render(w, "hygiene_panel.html", map[string]interface{}{"Hygiene": hyg, "Cache": cacheWidget(s.cache), "Title": pageTitle("hygiene"), "Nav": "hygiene"})
}

// ttl is the resolved claim heartbeat TTL: a task's session+heartbeat is "active"
// within it and "stale" (GC-reclaimable) beyond it. It mirrors the runner's TTL
// (config ttl_minutes) so the frontend's active/stale derivation matches
// selection. A non-positive config value falls back to the 60m default.
func (s *Server) ttl() time.Duration {
	m := s.cfg.TTLMinutes
	if m <= 0 {
		m = 60
	}
	return time.Duration(m) * time.Minute
}

// streamIvl is the SSE re-read cadence for a live session transcript: how often
// sessionStream re-derives the file-backed transcript (following off-box main
// each tick) and pushes it if changed. It is short enough to feel token-live but
// bounded so a viewer never hammers git; tests override it via streamInterval to
// run fast. The default (1s) beats the 2s htmx poll it supersedes while the
// stream is connected.
func (s *Server) streamIvl() time.Duration {
	if s.streamInterval > 0 {
		return s.streamInterval
	}
	return time.Second
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

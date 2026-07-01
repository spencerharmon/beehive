# Frontend Components (beehived)

Single Go binary. `net/http` + HTMX, templates and static assets embedded via `//go:embed`.
All views derive from beehive repo files; the daemon is the only long-running process. No DB.

## Routes / handlers
| Route                              | View / action                                   |
|------------------------------------|-------------------------------------------------|
| GET  /                             | Project dashboard: submodules, env, status      |
| GET  /submodule/{name}             | Submodule explorer: PLAN/ROI/INFRA/ARTIFACTS    |
| GET  /submodule/{name}/branches    | Per-submodule commit graph, sectioned/paginated         |
| GET  /submodule/{name}/plan        | PLAN.md items + state machine status            |
| GET  /roi/{name}                   | ROI.md CRUD (humans only)                        |
| POST /roi/{name}                   | edit record of intent, commit                   |
| GET  /secrets                      | secrets list (keys only, gpg-encrypted)         |
| POST /secrets                      | add/update/edit -> SECRETS.yaml.gpg             |
| GET  /merge                        | merge-to-main review                            |
| POST /merge                        | merge submodule/plan/infra changes to main      |
| POST /submodule/add                | add submodule (dormant)                         |
| POST /submodule/link               | create SUBMODULE-LINKS entry                     |
| GET  /env                          | dev/prod + blue/green deployment mgmt           |
| POST /env/deploy                   | trigger blue/green switch                       |
| GET  /human                        | NEEDS-HUMAN items awaiting resolution with reason |

## Component inventory (templates/)
- `layout.html` — shell, nav, htmx headers
- `dashboard.html` — submodule cards, env badge, swarm activity
- `explorer.html` — submodule file tree, doc viewer
- `plan_items.html` — task rows: status, deps, in-progress TTL, links to change docs
- `branch_view.html` — branches ↔ change documents
- `roi_editor.html` — intent CRUD (only human-writable surface)
- `secrets_panel.html` — gpg secrets add/update/edit
- `merge_panel.html` — diff + merge to main
- `links_editor.html` — submodule link graph
- `env_panel.html` — env + blue/green controls

## Principles
- Read = parse files; Write = edit file + git commit (push optional). Same ops as CLI, shared internal/.
- ROI editor is the ONLY place ROI.md is writable. Honeybees never reach it.
- Static assets + templates embedded -> single binary, container/host parity.
- No page ever crawls all branches across all submodules. Branch/commit history is rendered per-submodule
  only, as a graph fetched one section (page) at a time. Beehive change-doc links are derived from a
  commit-message stamp `Beehive: <task-id> <doc-path>` so rendering reads the stamp, not a cross-repo scan.
  Lower priority; revisit perf once the core app works.

## Performance: the HEAD-keyed parse cache

Read handlers used to re-read and re-parse every `PLAN.md`, `INFRASTRUCTURE.md`, and
`ARTIFACTS.md` on every request, so a burst of HTMX dashboard polls reparsed the whole
fleet each time. The daemon now memoizes those parses (`internal/web/cache.go`) keyed by
the beehive repo's **HEAD commit**:

- The handler resolves HEAD **once per request** (`git rev-parse HEAD`) and threads it into
  every cached read, so a dashboard over N submodules pays a single rev-parse, not one per file.
- Within one HEAD, repeated reads reuse the parse. On any commit HEAD moves and the whole
  cache is dropped, so the next read reparses from disk. This is correct because the
  checked-out `main` is a pure projection of committed history — honeybees publish by pushing
  and operator edits route through the frontend's own commit path, so **nothing mutates a
  tracked file without a commit**. A HEAD change is therefore a conservative superset of "some
  cached view is stale": correctness over hit-rate, by design.
- Only the HEAD-invariant *parse* is cached. Time-dependent projections (a task's claim
  active/stale vs the TTL) are applied per request on top of the cached parse, so nothing
  freezes at a stale value while HEAD sits still.
- If HEAD cannot be resolved (a brand-new repo with no commit yet) reads bypass the cache and
  always build fresh, so an uncommitted tree is never served stale.

### Supported-submodule ceiling

Invalidation is whole-cache-drop-per-commit: any commit evicts every submodule's entry, and
the next request rebuilds all of them under one lock (O(submodules) small file reads). This
comfortably serves a single beehive host's realistic fleet — **on the order of 100 submodules**
— where each parse is a few KB and commits arrive seconds (not milliseconds) apart. Well beyond
that (many hundreds/thousands of submodules AND a very high commit rate) an unrelated
submodule's commit still evicts every entry, so the amortization degrades. The deferred scaling
path is **per-file invalidation** (key on path + blob sha rather than one repo-HEAD key); it is
intentionally not built here in favor of the simplest correct design at the documented ceiling.


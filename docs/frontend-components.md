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
| GET  /human                        | NEEDS-HUMAN items awaiting resolution           |

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

## PLAN.md parse cache (`internal/web/cache.go`)

The dashboard, plan, and human views all read+parse every submodule's `PLAN.md` on
every request; once a hive grows past a handful of submodules that repeated parse is
the dominant per-request cost. A `planCache` memoizes the file-read + structural
parse (`internal/plan.Parse`), keyed by the beehive repo's **HEAD** commit.

- **Invalidation is by HEAD.** The checked-out `main` is a pure projection of
  committed history (honeybees publish by pushing/merging, the frontend commits its
  own writes), so any `PLAN.md`'s parsed *structure* can change only when HEAD
  advances. Each request resolves HEAD once (one cheap `rev-parse`, shared across
  every submodule it renders) and passes it in; when it differs from the cached
  generation every entry is dropped and re-parsed. A cached plan can therefore never
  outlive the commit it was parsed from — including a honeybee's out-of-process merge.
- **Only the wall-clock-independent parse is cached.** The claim projection
  (active/stale vs the TTL) depends on the current time and is recomputed on every
  read, so a cached entry can never report an expired claim as still active between
  commits. Correctness over hit-rate.
- **Empty HEAD = uncacheable.** A repo with no commits yet (HEAD cannot key the
  cache) is parsed fresh every call so pre-first-commit views still render.

### Supported-submodule ceiling

The cache holds one parsed plan per submodule for the current HEAD in memory, behind
a single mutex, rebuilt wholesale on every commit. It is sized for the **low hundreds
of submodules** a single `beehived` realistically serves:

- Memory is `O(submodules)` parsed plans for ONE HEAD (bounded — the previous
  generation is dropped, not accumulated).
- The one lock serializes parses, so a cold cache right after a commit re-parses under
  contention (a load is held under the lock, which also collapses a thundering herd on
  the same path). Fine at this scale, a bottleneck far beyond it.
- Invalidation is whole-cache per commit and honeybees commit frequently, so the
  steady-state hit rate falls as submodule count rises.

Past that range, switch to **per-submodule HEADs** (a submodule's gitlink moves only
when ITS plan/pointer changes, so an unrelated commit need not evict it) and/or
sharded locks. The same ceiling is stated in the `cache.go` doc comment and the
change doc.

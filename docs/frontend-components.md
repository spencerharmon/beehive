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

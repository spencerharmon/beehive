# env-badge-per-submodule: blue/green deploy state is per-submodule, never global

## Problem

Blue/green deployment is a property of each individual target, not of the hive as
a whole. `dashboard-cards` already reads each card's env badge from that
submodule's OWN `submodules/<name>/INFRASTRUCTURE.md` (via the typed
`internal/artifacts` model). But the env-deploy path predated multi-submodule
cards and still treated deployment as a single GLOBAL environment:

- `GET /env` / `POST /env/deploy` (`envGet`/`envDeploy` in `internal/web/web.go`)
  read and WROTE the repo-ROOT `INFRASTRUCTURE.md` — one env for the whole hive.
- The dashboard header rendered a single `Active env: <b>…</b>` line, also read
  from the root `INFRASTRUCTURE.md` (`parseEnv(s.repo.Root, …)`).
- The top nav offered one global `env` link.

So an operator switching "the" active env mutated a hive-wide doc that no
submodule's card actually reads, and the header implied a global deploy state that
contradicts the per-card badges.

## Design

Every blue/green READ and WRITE that renders or acts on a target's env badge /
coloring / deploy is scoped to an explicit submodule and touches ONLY that
submodule's `submodules/<name>/INFRASTRUCTURE.md`. No hive-wide env remains.

### 1. Per-submodule env routes + handlers (`internal/web/web.go`)

- Routes change from global `GET /env` + `POST /env/deploy` to
  `GET /submodule/{name}/env` + `POST /submodule/{name}/env/deploy`.
- `envGet` resolves the submodule from `{name}` (404 on unknown), reads its own
  `filepath.Join(sm.Path, repo.InfraFile)` via `parseEnv`, and renders the panel
  with the submodule `Name` so the form posts back to the same scoped route.
- `envDeploy` resolves the submodule, `deploy`s into that submodule's
  `INFRASTRUCTURE.md` only, publishes with a per-submodule message
  (`frontend: deploy <name> <target>`), then re-renders that submodule's scoped
  panel. A deploy on one target can never write another's doc.

### 2. Dashboard no longer reads a global env (`internal/web/web.go` + `dashboard.html`)

- The `dashboard` handler drops the root `parseEnv` and the top-level `Env` key.
- `dashboard.html` drops the `Active env: …` header line; the repo-wide
  `INFRASTRUCTURE.md` edit link stays (it is legitimate repo-wide notes), now
  labelled to say deploy env is per-submodule.
- The per-card env badge (already read per-submodule by `subViews`) becomes a LINK
  to `/submodule/{name}/env`, so each card's badge is both the per-target state and
  the entry point to manage that one target's deploy.

### 3. Scoped panel + discovery (`env_panel.html`, `explorer.html`, `layout.html`)

- `env_panel.html` interpolates `{{.Name}}` into its heading, form `action`,
  `hx-post`, and `hx-confirm`, and back-links to the submodule — the panel is a
  single submodule's control surface.
- `explorer.html` gains a `manage deploy env →` link (`/submodule/{name}/env`), so
  env is reachable per submodule even when it has no INFRASTRUCTURE.md yet.
- `layout.html` drops the global `env` nav link (env is per-submodule now).

### 4. `internal/artifacts` needs no change

The typed model (`LoadInfra`/`ParseInfra`/`Deployment`/`SetActive`) is already
path-parameterized and holds no package-level deployment state, so it is
submodule-agnostic by construction. Correct scoping is a property of the CALL
SITES passing the right per-submodule path, which is what this change enforces.

## Audit of every blue/green call site

- `envGet` / `envDeploy` — now per-submodule (was global root). FIXED.
- dashboard header `Active env` — removed (was a global root read). FIXED.
- `subViews` card badge — already per-submodule (`sm.Path`); NOT regressed.
- `explorer` INFRA render (`internal/web/web.go`) — already per-submodule.
- `internal/web/skills.go` `skillResources`, `skillInfraConventions`, `infraLine` —
  each already acts on an EXPLICITLY named target (the root repo-wide infra doc, or
  a specific submodule in the inventory loop). They never apply one submodule's env
  to another, so they are not the "global treatment" this task corrects; left
  unchanged (and out of the task's declared file scope). The root
  `INFRASTRUCTURE.md` remains a legitimate repo-wide notes doc.

## Tests (`internal/web/web_test.go`)

- `TestEnvDeploy` — POSTs `/submodule/alpha/env/deploy`; asserts alpha's OWN
  `INFRASTRUCTURE.md` flips to green and the ROOT doc is never mutated.
- `TestEnvDeployPerSubmoduleIsolated` — two submodules in OPPOSITE states (alpha
  blue, bravo green); deploying alpha (green, then blue) leaves bravo's doc
  byte-for-byte unchanged, its panel still reporting green, and its dashboard card
  badge still green — proving switching one target never affects another.
- `TestEnvDeployConfirmAndIndicator` — the scoped panel carries
  `hx-post="/submodule/alpha/env/deploy"`, the confirm, and the indicator.
- `TestFrontendWritesReachOrigin` — the per-submodule deploy reaches origin main at
  `submodules/alpha/INFRASTRUCTURE.md`.
- `TestDashboardCards` (unchanged) still passes: the per-card env read is not
  regressed.

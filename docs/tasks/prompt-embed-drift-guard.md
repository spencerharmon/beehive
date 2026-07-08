# prompt-embed-drift-guard: stamp a build SHA + warn when deployed binaries are stale

## Problem

The prompts (`HONEYBEE.md`, `AGENTS.md`, `BOOTSTRAP.md`) are `go:embed`ded and all
code is compiled into the three binaries (`beehive`, `beehived`, `honeybee`) at
BUILD time. Deploying a merged change requires an operator to run `beehive-rebuild`
(LOCALS.md) — there is no timer, git hook, or auto-trigger, and no observability.

Confirmed live (session-audit-010 Finding #1): a prompt fix (`gitlink-bump-recipe-hint`,
commit `588ebb6`, P1/weight=128) merged to main and bumped the pointer, yet a
honeybee dispatched ~2h48m later still ran the bare pre-fix prompt — the fix had had
ZERO effect on any live pass, invisibly. Every future prompt-level fix is subject to
the identical gap. We cannot see when the running binaries predate merged changes.

This task adds that visibility. It is deliberately **observability only**: it does
NOT auto-trigger `beehive-rebuild` (LOCALS.md frames the deploy as explicitly
operator-owned) and does not retroactively redeploy anything.

## Design

### 1. `internal/version` — a build-stamped commit (new package)

`var SHA = ""` set only by the build path via
`-ldflags "-X github.com/spencerharmon/beehive/internal/version.SHA=<sha>"`. Empty is
the HONEST "dev" signal for a plain `go build`; the value is never guessed, so a
binary never reports a wrong commit. `String()` → `"beehive <sha>"` or `"beehive dev"`;
`Build()` → `(sha, ok)` so callers distinguish "no SHA" from a real commit without
string-matching "dev". `cmd/beehive/cmd_basic.go`'s hardcoded `"beehive dev"` now
prints `version.String()`.

### 2. Build-time stamping (packaging)

`scripts/build-release-artifacts.sh` (the release/nfpm feed) and `scripts/install.sh`
(from-source install) compute `git rev-parse HEAD` and pass the `-X` ldflag to every
`go build`. Both are best-effort: a tree with no `.git` (a release tarball) builds an
honest unstamped "dev" binary rather than a wrong SHA. `nfpm.yaml` only copies the
prebuilt `dist/` binaries, so no change there. `-trimpath` does not strip `-X` vars.

> **Operator action (out of PLAN.md scope).** `~/.local/bin/beehive-rebuild` is
> operator-owned (LOCALS.md) and lives outside this repo, so it is not edited here.
> Until the operator adds the same `-ldflags "-X …/internal/version.SHA=$(git -C
> <src> rev-parse HEAD)"` to it, `beehive-rebuild` binaries stay unstamped ("dev")
> and the guard is simply INERT (never wrong). This is the intended
> operator-owned-deploy boundary; session-audit-011 verifies redeploy end-to-end.

### 3. `cmd/honeybee` preflight — the drift warning

In `run()`, right after the preflight resolves `baseMain` (the tracked-main tip) and
opens the repo, before selection/claim, `promptEmbedDriftWarning` runs. It:

- returns "" immediately for an unstamped (`version.SHA==""`) build — nothing to
  compare, so a dev build is silent;
- identifies the **self** submodule as the one whose `repo` object DB contains the
  build SHA (`git cat-file -e`): the build SHA is a commit of the beehive product
  repo, so only that target's checkout holds the object; unrelated targets (flux,
  helm-charts) never will — no hardcoded submodule name;
- resolves that submodule's **tracked-main tip** = the gitlink SHA at
  `submodules/<self>/repo` in `baseMain`'s tree (`git.Repo.GitlinkAt`, new helper);
- warns iff the build does **not** contain the tip (`!IsAncestor(tip, build)`), in
  the SAME `honeybee: WARNING preflight …` style as the existing dirty-checkout
  warning, naming the self submodule and both short SHAs.

Every unresolved-git branch (no tracked pointer, ancestry error, self not found)
returns "" — the guard NEVER errors out and NEVER touches selection/claim/publish.

#### Staleness direction (note vs the card's wording)

The card's Accept says "build SHA is not an ancestor of the tip". Taken literally
that is `!IsAncestor(build, tip)`, which is **false in the exact motivating case**: a
stale binary built from an OLD commit X IS an ancestor of the newer tip Y, so a
literal reading would stay silent precisely when we must warn. The card BODY ("warn
when the build SHA **predates** the tip") and the whole Finding-#1 motivation
("warn when the deployed binary is behind merged changes") are unambiguous, so the
guard implements the semantically-correct check: **warn when the build does not
CONTAIN the tracked tip** = `!IsAncestor(tip, build)`. This is silent for a fresh
build (build==tip) and for a dev build ahead of the pointer, and warns for a build
behind or diverged from the tip.

## Tests

- `internal/version/version_test.go` — unstamped → `"beehive dev"`/`("",false)`;
  stamped → the exact SHA in `String()`/`Build()`.
- `internal/git/git_test.go` `TestGitlinkAt` — real gitlink (mode 160000) resolves
  to its pinned commit; a regular file and an absent path both → `""` no error.
- `cmd/honeybee/main_test.go` `TestPromptEmbedDriftWarning` — a real fixture (beehive
  product repo with `c1`→`c2`, an unrelated flux target, a hive superproject pinning
  a chosen gitlink): stale (`c1` vs tip `c2`) warns and names the submodule + both
  short SHAs; fresh (`c2`==tip) silent; ahead (`c2` vs tip `c1`) silent; unstamped
  silent; a phantom SHA no target contains silent. `TestShortSHA` covers truncation.

## Acceptance mapping

- *`beehive version` reports a real build SHA via the packaging path; honest "dev"
  fallback; never a wrong SHA* → `internal/version` + both build scripts;
  `version_test.go`, and verified live (`build-release-artifacts.sh` output).
- *preflight emits a stderr warning (existing style) whenever its build SHA is not
  contained in the submodule's tracked-main tip, silent when it is* →
  `promptEmbedDriftWarning` + `GitlinkAt`; `TestPromptEmbedDriftWarning`.
- *no change to publish/claim/completion logic* → the guard is a read-only,
  non-fatal stderr emit inserted before selection; nothing downstream reads it.
- *go build/test green* → `go build ./...`, `go vet ./...`, `go test ./...` all pass.

## Not done (deliberate)

- The optional beehived dashboard banner is a clean follow-up: beehived would read
  its own compiled `version.SHA` and the tracked tip (both in-repo data, so it stays
  within the "repo is the only data source" rule) and render a banner. Skipped to
  keep this change focused on the graded Accept surface; the reusable pieces
  (`version.Build`, `git.Repo.GitlinkAt`, the `!IsAncestor(tip, build)` decision)
  are already in place for it.

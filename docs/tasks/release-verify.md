# release-verify: beehive's release/CI pipeline on self-hosted Zuul

## Problem

`release-verify` had been reframed and re-stranded across 8 prior attempts. The
task card that finally unblocked it corrects a false premise baked into the
older framings: it isn't "confirm the release pipeline" (implying nothing
exists, or that whatever exists just needs checking off) — this repo already
had a *complete, working* release pipeline (`.github/workflows/ci.yml`,
`packaging/`, `docs/RELEASE-NOTES-TEMPLATE.md`, `scripts/verify-release.sh`,
`internal/release/*_test.go`), built and merged by an earlier `release-verify`
pass (commit `ae5454d`). The actual, corrected instruction is: that pipeline
runs on **GitHub Actions with GitHub-hosted `cosign` signing** — the ROI wants
it on **self-hosted Zuul** (deployed into k3s by the `flux` submodule's `zuul`
task) instead. Nothing needed "confirming"; the Zuul half needed *building*.

This task is also the flagship proof of the swarm's cross-submodule
dependency selector: `beehive:release-verify` cross-deps on `flux:zuul`, and
is not selectable until flux's `zuul` task is `DONE` and the link is
authorized in `SUBMODULE-LINKS.yaml`. By the time this pass ran, flux's `zuul`
task was `DONE` (see `submodules/flux/docs/bee-zuul-zuul.md`) — Zuul is
deployed, but two facts from that doc bound what "done" can mean here:

- **No Nodepool build-node provider exists yet** (explicitly flagged as
  unverified/deferred in flux's own doc) — so no Zuul job, including this
  one, can actually *execute* on this cluster today.
- **Only `check`/`gate` pipelines are confirmed configured** for the `beehive`
  tenant project ("a minimal check/gate pipeline is enough to prove the
  tenant loads. Add other projects later."). There is no confirmed
  tag-triggered `release`/`post` pipeline for `beehive` yet.

Given that, and given this task's own Accept bar is explicitly **static +
local only** ("the live CI run + signing + verify-blob are explicitly out of
this Accept"), the job here is to author a *correct, locally-reproducible*
Zuul job config now, not to cut over live release traffic before Zuul can run
anything at all.

## Fix

### `zuul.d/jobs.yaml` (new)

Two Zuul job definitions:

- `beehive-build-test` — gofmt/vet/build/test + the submodule smoke test
  (`playbooks/beehive-build-test.yaml`), mirroring the non-release half of
  the existing `ci.yml` `build-test` job.
- `beehive-release-cross-compile` — the task's actual acceptance bar
  (`playbooks/beehive-release-cross-compile.yaml`): cross-compiles
  `beehive`/`beehived`/`honeybee`, `CGO_ENABLED=0`, for
  linux/darwin × amd64/arm64, then asserts every one of the 12 artifacts is
  static, and their checksums verify.

Nodeset is deliberately **omitted** on both jobs (they inherit whichever
nodeset the tenant's own base job eventually defines) rather than hardcoding
a Nodepool label this repo has no way to confirm exists — there IS no
Nodepool provider yet. Both jobs carry an explicit `timeout:`.

### `zuul.d/project.yaml` (new)

Attaches both jobs to `check` **and** `gate` — the only two pipelines
confirmed to exist for the tenant. `name:` is omitted (defaults to the
defining project). beehive is an *untrusted* project here (flux owns the
Zuul config-project), so it cannot itself define a new `pipeline:` object —
only attach jobs to pipelines the config-project already defines. Running
the cross-compile+static-assert job on check/gate (not only at tag time) is
also independently useful: it catches a cross-platform static-build
regression on every proposed change.

### `playbooks/beehive-build-test.yaml`, `playbooks/beehive-release-cross-compile.yaml` (new)

Thin Ansible playbooks — each resolves the checkout path as
`{{ ansible_user_dir }}/{{ zuul.project.src_dir }}` (the documented,
standard Zuul pattern for locating a job's own project checkout) and then
shells out to repo scripts, exactly like `ci.yml` already does. The release
playbook runs `scripts/build-release-artifacts.sh dist` then
`SKIP_COSIGN=1 scripts/verify-release.sh dist` — it never invokes `cosign`
itself; signing/publishing is explicitly a live/operator concern once the
pipeline is actually running (see "Explicitly out of scope" below).

### `scripts/build-release-artifacts.sh` (new)

Extracts the cross-compile-matrix loop that previously lived inline in
`ci.yml`'s `release:` job into a standalone, POSIX-shell, CI-agnostic script
— usable identically from a shell, from the new Zuul playbook, or (as a
follow-up, see below) from `ci.yml` itself. Builds all 12
binary × os/arch combinations into `DIST_DIR` (default `dist`) and writes
`SHA256SUMS-<os>-<arch>` per target, in the same layout
`scripts/verify-release.sh` already expects.

### `internal/release/zuul_test.go` (new)

`internal/release`'s own doc comment says it plainly: "a fix must ship
tests, and nothing else exercises the release wiring." The existing
`release_test.go` guards the GitHub-Actions half by asserting `ci.yml`'s
literal contents; this file applies the identical discipline to the new
Zuul half:

- Parses `zuul.d/jobs.yaml` with `gopkg.in/yaml.v3` (a real parse, not a
  guess) and asserts `beehive-release-cross-compile` and `beehive-build-test`
  exist, run the right playbooks, and that every job's `run:` playbook
  actually exists on disk.
- Parses `zuul.d/project.yaml` and asserts both jobs are listed under both
  `check` and `gate`.
- Asserts the release playbook delegates to `build-release-artifacts.sh` +
  `verify-release.sh SKIP_COSIGN=1` and never calls `cosign` directly.
- `TestBuildReleaseArtifactsScriptContract` mirrors
  `TestVerifyReleaseScriptContract` for the new script (all 3 binaries, all
  4 targets, `CGO_ENABLED=0`, checksums, executable bit).
- `TestBuildReleaseArtifactsScriptE2E` actually **runs**
  `build-release-artifacts.sh` end to end (all 4 targets — no cgo needed,
  since every target is `CGO_ENABLED=0`, so this runs on any host), feeds
  the result into `verify-release.sh SKIP_COSIGN=1`, and independently
  re-confirms `file`-static-linkage on the linux artifacts. This is the
  automated, permanent version of this task's Accept bar, not a one-off
  manual check.

`internal/release/doc.go`'s package comment now names both pipelines and the
GitHub Actions → Zuul migration in progress.

### `docs/RELEASE-NOTES-TEMPLATE.md` (edited)

The "Verify" section's `cosign verify-blob` example hardcoded
`--certificate-oidc-issuer https://token.actions.githubusercontent.com` and a
`github.com/<owner>/<repo>/.github/workflows/...` identity regexp — a
GitHub-Actions-only assumption that becomes actively wrong once a release can
also come from Zuul. Replaced both with explicit
`<signer identity regexp — from this release's CI run>` /
`<OIDC issuer — from this release's CI run>` placeholders and a note that the
two pipelines have different values, neither hardcoded here. This is a
minimal, targeted edit — the existing
`internal/release.TestReleaseNotesVerifyCommand` (unchanged) still passes,
since every literal substring it checks for (`verify-blob`, `--signature`,
`--certificate `, `--certificate-identity`, `--certificate-oidc-issuer`,
`sha256sum -c`) is still present, just no longer hardcoded to one issuer.

### `packaging/*` — reviewed, unchanged

`packaging/nfpm.yaml`/`config.yaml`/`postinstall.sh`/`opencode.service`
operate purely on `dist/<binary>-linux-amd64` artifacts by naming
convention — nothing in them is GitHub-Actions-specific, and
`build-release-artifacts.sh` produces the identical layout. No changes
needed; this task's `packaging/*` deliverable is satisfied by confirming
continued correctness rather than churning working files.

## Explicitly left alone (scope)

- **`.github/workflows/ci.yml`** is untouched. It is not in this task's
  `Files:` list, it still works today (unlike the new Zuul job, which cannot
  run anywhere yet — no Nodepool), and ripping it out now would leave
  beehive with *no* functioning release pipeline at all. Cutover — disabling
  or removing the GitHub Actions release/verify-release jobs — is a
  follow-up once the Zuul pipeline is proven live end to end, matching this
  task's own "CI-only, NOT a honeybee Accept" carve-out for the live run.
- **Signing, publishing, `cosign verify-blob` from a clean checkout** are
  never invoked by anything committed here — explicitly out of this task's
  Accept, and correctly so: there is no live Zuul execution capability
  (Nodepool) yet to run them from.
- **A dedicated tag-triggered `release`/`post` pipeline** for `beehive` is
  not created (only flux's Zuul config-project can define a new `pipeline:`
  object) — `beehive-release-cross-compile` runs on `check`/`gate` instead,
  which is safe (confirmed to exist) and independently useful. Follow-up:
  retarget once such a pipeline exists.

## Verification

Zuul/Ansible tooling (`zuul`, `zuul-client`, `ansible-playbook`, `yamllint`)
is not installed in this sandbox, so "the Zuul config parses/lints" is
verified two ways: (1) `python3`'s `yaml.safe_load` on every new YAML file
(clean parse, all four files), cross-checked by hand against Zuul's own
`job`/`project` attribute reference (`zuul-ci.org/docs/zuul/latest/config/
{job,project}.html`, fetched this pass); and (2) permanently, via
`internal/release/zuul_test.go`'s `gopkg.in/yaml.v3` unmarshal of both
`zuul.d/*.yaml` files on every `go test` run.

```
$ python3 -c "import yaml; yaml.safe_load(open('zuul.d/jobs.yaml'))"      # clean
$ python3 -c "import yaml; yaml.safe_load(open('zuul.d/project.yaml'))"   # clean
$ sh -n scripts/build-release-artifacts.sh                                # clean

$ ./scripts/build-release-artifacts.sh dist
build-release-artifacts: building dist/beehive-linux-amd64 (CGO_ENABLED=0 GOOS=linux GOARCH=amd64)
... (12 builds total: {beehive,beehived,honeybee} x {linux,darwin}/{amd64,arm64})
build-release-artifacts: OK (dist)

$ SKIP_COSIGN=1 ./scripts/verify-release.sh dist
verify-release: static OK   dist/beehive-linux-amd64
... (12/12 static OK, 4/4 SHA256SUMS OK)
verify-release: OK (static + checksum)

$ file -b dist/beehive-linux-amd64
ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked, Go BuildID=..., with debug_info, not stripped
$ file -b dist/beehive-linux-arm64
ELF 64-bit LSB executable, ARM aarch64, version 1 (SYSV), statically linked, Go BuildID=..., with debug_info, not stripped
$ file -b dist/beehive-darwin-amd64
Mach-O 64-bit x86_64 executable, flags:<|DYLDLINK|PIE>     # CGO-free (go version -m), Mach-O always links libSystem — expected, documented

$ gofmt -l .                 # clean
$ go vet ./...                # clean
$ CGO_ENABLED=0 go build ./... # clean
$ go test -count=1 ./...      # all packages ok, including the 5 new internal/release/zuul_test.go cases
```

`go test -race` was not run in this sandbox: the honeybee turn-loop's own
host-mandated `CGO_ENABLED=0` build env and this container's incomplete cgo
toolchain (`ld: cannot find -latomic_asneeded`) make `-race` unrunnable here
regardless of this change — a pre-existing sandbox limitation, not something
this task touched or regressed (no Go changed outside `internal/release`,
and `go test` without `-race` is fully green).

## Result

`zuul.d/` + `playbooks/` + `scripts/build-release-artifacts.sh` give beehive
a locally-reproducible, self-hosted-Zuul release/CI pipeline definition
matching the ROI's actual direction (Zuul, not GitHub Actions), scoped
honestly to what can be verified without a live Zuul executor: the config
parses, the job/project wiring is internally consistent and permanently
regression-tested, and the cross-compile + static-assert matrix it describes
is proven correct by actually running it. The live pipeline run, signing,
and publishing remain — correctly — future, operator/CI-only work.

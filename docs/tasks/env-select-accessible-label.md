# env-select-accessible-label: give the deploy `<select>` a programmatic name

## Problem

A follow-up UI accessibility audit (ui-audit-004) found that after
`form-input-accessible-labels` (DONE) named every other form control,
`env_panel.html` was the **lone unlabeled control left**: the blue/green deploy
picker rendered as `<select name="target">` with no `<label for>` and no
`aria-label`. A screen reader announces a bare combobox with no hint that it
selects a deploy environment — WCAG 4.1.2 (Name, Role, Value) and 3.3.2 (Labels
or Instructions).

Re-verified against the other selects: `merge_panel.html` pairs an sr-only
`<label for="merge-name">` and `stats.html` uses `<select aria-label="filter
operator">` — both already named. Only `env_panel` was not.

## Design

Reuse the established pattern verbatim from `merge_panel.html` — a visually-hidden
`<label class="sr-only" for>` tied to the `<select>` by `for`/`id`:

```html
<label class="sr-only" for="env-deploy-target">Deploy environment</label>
<select id="env-deploy-target" name="target">…</select>
```

- The `.sr-only` utility already ships in `internal/web/assets/style.css` (the
  standard `position:absolute; clip:rect(0 0 0 0); 1px` off-screen box). An sr-only
  `<label>` is out-of-flow, so it is **not** a flex item and takes no part in the
  form's `gap`/layout — adding it is visually inert (no regression).
- `name="target"` is unchanged, so the form still posts the same field to
  `/submodule/{{.Name}}/env/deploy`. Only an `id` (and the paired label) is added;
  no option/value/handler change.

This is the sr-only `<label for>` variant (not `aria-label`) to match its nearest
sibling, the `merge_panel.html` submodule `<select>`.

## Tests — `internal/web/web_test.go`

`TestFormInputsHaveAccessibleNames` gains an `env_panel.html deploy form` case:
renders the template via `renderTmpl` with `Env{Active:"blue", Envs:{blue,green}}`
and asserts the exact `<label class="sr-only" for="env-deploy-target">Deploy
environment</label>` + matching `id="env-deploy-target"`, plus that `name="target"`
still renders (the field the form posts is preserved).

## Acceptance mapping

- *deploy `<select>` has a programmatic accessible name consistent with
  merge_panel.html* → the sr-only `<label for="env-deploy-target">` + matching
  `id`, the same pattern as `merge-name`; asserted by the new test case.
- *no visual/layout change* → sr-only content is `position:absolute` + clipped
  (out-of-flow, no flex/gap impact); no visible node added.
- *form still posts `name=target`* → the `name` attribute is untouched; the test
  asserts `name="target"` still renders.
- *grep `<select` shows no remaining select without a label/aria-label* →
  env_panel (sr-only label), merge_panel (sr-only label), stats (aria-label) are
  now all named.
- *gofmt / go vet / go test ./internal/web green (CGO_ENABLED=0)* → verified.

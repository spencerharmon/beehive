# roi-source-textarea-label: give the ROI raw-source `<textarea>` a programmatic name

## Problem

A re-scan (ui-audit-005, accessibility lens) found that `roi_editor.html:12`
renders the ROI raw-source editor as `<textarea name="body" rows="20" cols="80">`
inside a `<details><summary>edit source</summary>` disclosure with **no
`<label for>`, no `aria-label`, and no `id`**. A `<summary>` is not a native label
source for a descendant control, so a screen reader announces an anonymous
multi-line field — WCAG 4.1.2 (Name, Role, Value) and 3.3.2 (Labels or
Instructions).

`grep -rn "<textarea" internal/web/templates` confirms it is the app's **only**
`<textarea>`. It was previously spotted by ui-audit-002, which assumed
`form-input-accessible-labels` (then in flight) would cover it — but that task's
`Files:` list and DONE review enumerated exactly 9 templates and never included
`roi_editor.html`, so it fell through uncaught.

## Design

Reuse the established pattern verbatim from `merge_panel.html` / `env_panel.html` —
a visually-hidden `<label class="sr-only" for>` tied to the `<textarea>` by
`for`/`id`:

```html
<label class="sr-only" for="roi-source-body">Edit ROI.md source</label>
<textarea id="roi-source-body" name="body" rows="20" cols="80">{{.Body}}</textarea>
```

- The `.sr-only` utility already ships in `internal/web/assets/style.css` (the
  standard `position:absolute; clip:rect(0 0 0 0); 1px` off-screen box, added by
  `form-input-accessible-labels`). An sr-only `<label>` is out-of-flow, so it is
  **not** a flex item and takes no part in the form's layout — adding it is
  visually inert (no regression), and no CSS changed.
- `name="body"` and `rows`/`cols` are untouched, so the form still posts the same
  field to `/roi/{{.Name}}` and the textarea keeps its size. Only an `id` (and the
  paired label) is added; no handler/value change.
- Chose the sr-only `<label for>` variant (not `aria-label`) to match its nearest
  siblings, the `merge_panel.html` / `branch_view.html` / `env_panel.html`
  controls, per the task's requested fix.

## Tests — `internal/web/web_test.go`

`TestFormInputsHaveAccessibleNames` gains a `roi_editor.html source textarea`
case: renders the editor via the real route `GET /roi/alpha` and asserts the exact
`<label class="sr-only" for="roi-source-body">Edit ROI.md source</label>` +
matching `id="roi-source-body"`, plus that `name="body"` still renders (the field
the form posts is preserved).

## Acceptance mapping

- *the `<textarea name="body">` has a programmatic accessible name consistent with
  merge_panel.html/branch_view.html* → the sr-only `<label for="roi-source-body">`
  + matching `id`, the same pattern as `merge-name`; asserted by the new test case.
- *no visual/layout change* → sr-only content is `position:absolute` + clipped
  (out-of-flow); `rows`/`cols` preserved; no visible node added.
- *form still posts `name=body` and saves correctly* → the `name` attribute is
  untouched; the test asserts `name="body"` still renders.
- *grep `<textarea` shows the textarea now resolves a non-empty accessible name* →
  the only `<textarea>` now carries `id="roi-source-body"` paired to its sr-only
  `<label for>`.
- *gofmt / go vet / go test ./internal/web green (CGO_ENABLED=0)* → verified.

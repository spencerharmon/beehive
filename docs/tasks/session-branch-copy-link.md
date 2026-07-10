# session-branch-copy-link: copy-to-clipboard for the session page's branch name

## Problem

`session_view.html`'s secondary line renders the session's full
`bee-<taskid>-<epoch>-<pid>` branch string as inert plain text:

```
<p class="muted small">branch <code>{{.Branch}}</code></p>
```

Like a submodule commit sha (`commit-sha-deep-links`/`-relanding`), a session
branch string cannot resolve to anything inside the hive superproject — the
branch IS the identifier, with nowhere to link it. It was the one long opaque
identifier left on the page with no copy affordance; every prior
copy-to-clipboard task's scope stopped at commit shas
(`commit-sha-deep-links[-relanding]`) and never reached branch names.

## Fix

Reuse `layout.html`'s existing generic `.copy-btn`/`[data-copy]` delegated click
handler and `#copy-live` aria-live region verbatim — same markup shape already
used for commit shas' `sha-cell` (`branch_view.html`, `commit_view.html`):

```
<p class="muted small">branch <span class="sha-cell"><code>{{.Branch}}</code><button type="button" class="copy-btn" data-copy="{{.Branch}}" aria-label="Copy branch {{.Branch}} to clipboard">copy</button></span></p>
```

- The branch stays plain, selectable `<code>` text (no-JS fallback), now wrapped
  in a `sha-cell` span alongside a progressively-enhanced `.copy-btn` carrying
  the branch in `data-copy` plus an accessible label matching the commit-sha
  copy buttons' phrasing convention (`Copy <thing> to clipboard`).
- No new JS/CSS token: `layout.html`'s copy handler, `#copy-live` region, and the
  `.sha-cell`/`.copy-btn` CSS are all reused verbatim.
- No change to the branch heading's typography/placement beyond what
  `session-list-links-labels` already left it as (the `<h1>` shortened-name /
  secondary-branch-line split is untouched).
- Sequenced after `session-list-links-labels` (which rewrote this same heading
  line for a different reason — making the branch smaller, secondary text) so
  this task wraps the markup shape that task left rather than colliding with it.

## Tests (`internal/web/web_test.go`)

- `TestSessionViewBranchCopyControl` — new test locking the branch's plain
  `<code>` fallback plus the `.copy-btn`/`data-copy`/`aria-label` copy control.
- `TestSessionListLinksTaskDocCommit` / `TestSessionListLinksDegradeGracefully` —
  updated their exact-markup assertions on the branch secondary line to match the
  new `sha-cell`/`copy-btn` wrapper (both previously asserted the old bare
  `<code>{{.Branch}}</code>` markup byte-for-byte).

# chat-editor-status-poll-fix

Fixes the chat-diff editor's stuck-status bug on the polling path (reproduced by
`chat-editor-status-poll-failing-test`): the live indicator now clears the instant
the assistant's reply is ready, instead of staying "working…" through the
trailing commit/transcript/push/merge publish work.

## The fix

`Session.runTurn` (`internal/editor/editor.go`) previously held `busy == true`
across the ENTIRE turn — the agent prompt, the local `CommitPaths`, the
durability transcript commit + `PushBranchReconciled`, and any agent-triggered
`merge` — clearing it only after all of that finished. `Busy()` (read verbatim by
the `/editor` state poll and the panel template's `{{if .Busy}}` spinner/re-poll
node) therefore stayed "working…" long after the reply the human is waiting for
was already appended to the log — worst-case stuck for as long as a slow/stalled
push to a trusted remote.

The turn's serialization (one turn at a time per session) and its UI-facing
status are now two separate signals on `Session`:

- `busy` — UI-facing only. Set `true` when a turn starts; cleared the moment the
  assistant's reply is appended to `s.log`, under the SAME lock acquisition, so a
  poll can never observe "reply ready" and "still busy" as separate states, only
  "busy with no reply yet" or "idle with the reply already in the log".
- `turnOn` — internal-only serialization guard. Set `true` when a turn starts
  (`StartChat`/`Chat`), left `true` through the trailing commit/transcript/
  push/merge publish work, cleared only once that work fully finishes. Never
  read by `Busy()` or surfaced to the UI; its only job is refusing a concurrent
  `StartChat`/`Chat` on the same session while the previous turn's publish work
  is still touching the worktree.

The in-flight connecting/connected/working lifecycle (`internal/web/chatedit.go`,
a separate session type over a different code path) is untouched — this task
only repairs `editor.Session`'s terminal idle transition.

The panel's poll-until-idle template node
(`internal/web/templates/editor_panel.html`) already gates strictly on `.Busy`
(`{{if .Busy}}...re-poll node{{end}}`), so once `busy` clears the next (and, in
practice, already-in-flight) poll renders the settled panel — reply visible, no
manual refresh — exactly per the accept criteria.

## Verification

`TestStatusClearsWhenReplyReadyWhilePublishing`
(`internal/editor/status_poll_test.go`) — the reproduction test from
`chat-editor-status-poll-failing-test` — now passes. Its post-assert drain loop
was updated to wait on the session's internal `turnOn` flag (direct field access;
same package) instead of `Busy()`, since `Busy()` now correctly clears before
the publish work is done — draining on it would return immediately and race
`t.TempDir()` cleanup against the still-running background commit/push. This
does not weaken the test's actual assertion (`stuck := sess.Busy()`, still
required `false` while the push is stalled); it only fixes the teardown
synchronization to match the corrected semantics.

```
$ CGO_ENABLED=0 go test ./internal/editor/... -run TestStatusClearsWhenReplyReadyWhilePublishing -v -count=3
=== RUN   TestStatusClearsWhenReplyReadyWhilePublishing
--- PASS: TestStatusClearsWhenReplyReadyWhilePublishing (0.12s)
=== RUN   TestStatusClearsWhenReplyReadyWhilePublishing
--- PASS: TestStatusClearsWhenReplyReadyWhilePublishing (0.12s)
=== RUN   TestStatusClearsWhenReplyReadyWhilePublishing
--- PASS: TestStatusClearsWhenReplyReadyWhilePublishing (0.13s)
PASS
ok  	github.com/spencerharmon/beehive/internal/editor	0.385s
```

Full suite green under `CGO_ENABLED=0`:

```
$ CGO_ENABLED=0 go test ./...
ok  	github.com/spencerharmon/beehive/cmd/beehive	...
ok  	github.com/spencerharmon/beehive/cmd/honeybee	...
ok  	github.com/spencerharmon/beehive/internal/editor	...
ok  	github.com/spencerharmon/beehive/internal/git	...
ok  	github.com/spencerharmon/beehive/internal/web	...
... (all packages ok)
```

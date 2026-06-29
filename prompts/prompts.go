// Package prompts embeds the system + user prompts used by the honeybee runner.
package prompts

import _ "embed"

//go:embed AGENTS.md
var Agents string

// RepoAgents is the slim, repo-local AGENTS.md written to disk by `beehive init`.
// Unlike Agents (the full protocol injected as the runtime system prompt), this
// is committed into the repo and therefore frozen at init time, so it must stay
// minimal: a repo marker plus local rules, deferring the protocol to the binary.
//
//go:embed repo_agents.md
var RepoAgents string

//go:embed bootstrap.md
var Bootstrap string

//go:embed select.md
var Select string

//go:embed review.md
var Review string

//go:embed arbitrate.md
var Arbitrate string

//go:embed reconcile.md
var Reconcile string

//go:embed continue.md
var Continue string

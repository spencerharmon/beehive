// Package prompts embeds the system + user prompts used by the honeybee runner.
package prompts

import _ "embed"

//go:embed AGENTS.md
var Agents string

//go:embed bootstrap.md
var Bootstrap string

//go:embed select.md
var Select string

//go:embed reconcile.md
var Reconcile string

//go:embed continue.md
var Continue string

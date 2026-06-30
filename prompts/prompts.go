// Package prompts embeds the system + user prompts used by the honeybee runner.
package prompts

import _ "embed"

//go:embed AGENTS.md
var Agents string

// Honeybee is the honeybee runtime protocol. It is the binary's DEFAULT copy of
// HONEYBEE.md; `beehive init` / `beehive instruction update` write it to the repo
// root, and the runner reads the on-disk file (falling back to this default only
// when the file is absent). The on-disk file — not the binary — is authoritative.
//
//go:embed HONEYBEE.md
var Honeybee string

// BootstrapGuide is the default BOOTSTRAP.md (install setup walkthrough). Distinct
// from Bootstrap, which is the per-pass bootstrap PROMPT injected at runtime.
//
//go:embed bootstrap_guide.md
var BootstrapGuide string

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

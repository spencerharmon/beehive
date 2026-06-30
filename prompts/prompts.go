// Package prompts embeds the system + user prompts used by the honeybee runner.
package prompts

import (
	"embed"
	_ "embed"
	"io/fs"
	"sort"
	"strings"
)

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

// skillsFS holds the default skill files. Each is a managed instruction file
// installed under the repo's skills/ directory; AGENTS.md indexes them and an agent
// reads the relevant one into context on demand. Defaults are refreshed by
// `beehive instruction update`.
//
//go:embed skills/*.md
var skillsFS embed.FS

// Skill is one default skill file: its base name (e.g. "cleanup.md") and body.
type Skill struct {
	Name string
	Body string
}

// Skills returns the embedded default skill files, sorted by name. The install path
// for each is skills/<Name> under the repo root.
func Skills() []Skill {
	ents, err := fs.ReadDir(skillsFS, "skills")
	if err != nil {
		panic("prompts: reading embedded skills: " + err.Error())
	}
	var out []Skill
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := skillsFS.ReadFile("skills/" + e.Name())
		if err != nil {
			panic("prompts: reading embedded skill " + e.Name() + ": " + err.Error())
		}
		out = append(out, Skill{Name: e.Name(), Body: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

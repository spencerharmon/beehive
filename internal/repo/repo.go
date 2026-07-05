// Package repo models the beehive repo layout and derives all state from files.
// The frontend and honeybee read state through this package; nothing else owns truth.
package repo

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// Layout names. ROI is human-owned and never written by honeybees. LOCALS is
// the site-specific operator record (authored per install, never managed);
// BOOTSTRAP is the managed setup walkthrough installed at init. RULES is a
// beehive-owned, per-submodule rules overlay ADDITIVE to any AGENTS.md (both are
// read into agent context, AGENTS first then RULES); its absence is a safe no-op.
const (
	AgentsFile    = "AGENTS.md"
	HoneybeeFile  = "HONEYBEE.md"
	PlanFile      = "PLAN.md"
	ROIFile       = "ROI.md"
	InfraFile     = "INFRASTRUCTURE.md"
	Artifacts     = "ARTIFACTS.md"
	RulesFile     = "RULES.md"
	LinksFile     = "SUBMODULE-LINKS.yaml"
	SecretsFile   = "SECRETS.yaml.gpg"
	LocalsFile    = "LOCALS.md"
	BootstrapFile = "BOOTSTRAP.md"
)

// RootInstructionFile is one repo-ROOT instruction file the frontend surfaces a
// uniform view/edit (or, when absent, create) link for. Managed marks the members
// beehive ships a default for and that `beehive instruction update` refreshes
// (AGENTS.md, HONEYBEE.md, BOOTSTRAP.md — the instruct.Files set); it is false for
// the site-authored LOCALS.md, which is never managed or auto-generated. The flag
// is what instruction-update-drift keys off to scope its staleness check.
type RootInstructionFile struct {
	File    string // basename at the repo root, e.g. "AGENTS.md"
	Managed bool   // beehive ships/refreshes a default (vs. site-authored)
}

// RootInstructionFiles is the DECLARED set of repo-ROOT instruction files the
// frontend renders discoverable links for UNIFORMLY — present or absent — driven
// by THIS set, not the directory listing, so a missing member (e.g. an unwritten
// LOCALS.md) is discoverable and offers a create path instead of being invisible.
// It is the root analogue of OptionalFiles (which is per-submodule). The root
// AGENTS.md here is the GENERIC operating guide and is deliberately NOT the same
// thing as a per-submodule submodules/<sm>/AGENTS.md rules overlay (that overlay
// rides OptionalFiles). Order is the render order. This is the single source of
// truth for membership and the per-file managed flag.
var RootInstructionFiles = []RootInstructionFile{
	{AgentsFile, true},    // AGENTS.md — generic operating guide (managed)
	{HoneybeeFile, true},  // HONEYBEE.md — honeybee runtime protocol (managed)
	{BootstrapFile, true}, // BOOTSTRAP.md — install walkthrough (managed)
	{LocalsFile, false},   // LOCALS.md — site-authored, never managed
}

// OptionalFiles is the KNOWN set of optional per-submodule files the frontend
// renders view/edit links for UNIFORMLY — present or absent — so a missing file
// is discoverable instead of invisible (the explorer drives links from THIS set,
// not from the directory listing). It mirrors the layout name constants above
// (RULES.md rides the submodule-rules-md overlay); ROI.md is human-owned and is
// edited only through the frontend editor, never auto-generated. PLAN.md is
// deliberately excluded: it is honeybee-owned, is produced by bootstrap rather
// than authored ad hoc, and has its own dedicated plan view. Order is the
// discovery order the explorer index renders in.
var OptionalFiles = []string{
	InfraFile,  // INFRASTRUCTURE.md
	RulesFile,  // RULES.md
	Artifacts,  // ARTIFACTS.md
	AgentsFile, // AGENTS.md
	ROIFile,    // ROI.md
}

// roiStamp matches the PLAN.md reconcile marker: <!-- Beehive-ROI: <sha> -->
var roiStamp = regexp.MustCompile(`Beehive-ROI:\s*([0-9a-f]+)`)

// Repo is a beehive repo rooted at Root.
type Repo struct{ Root string }

// Open returns a Repo if Root contains AGENTS.md and a submodules dir.
func Open(root string) (*Repo, error) {
	if _, err := os.Stat(filepath.Join(root, AgentsFile)); err != nil {
		return nil, err
	}
	return &Repo{Root: root}, nil
}

// Submodule is one tracked target repo with its beehive coordination files.
type Submodule struct {
	Name string
	Path string // submodules/<name>
}

// RepoDir is the tracked target checkout (worktree base).
func (s Submodule) RepoDir() string { return filepath.Join(s.Path, "repo") }

// PlanPath, ROIPath, WorktreesDir locate coordination files.
func (s Submodule) PlanPath() string     { return filepath.Join(s.Path, PlanFile) }
func (s Submodule) ROIPath() string      { return filepath.Join(s.Path, ROIFile) }
func (s Submodule) WorktreesDir() string { return filepath.Join(s.Path, "worktrees") }

// SessionsDir holds recorded honeybee session transcripts (one .md per branch).
func (s Submodule) SessionsDir() string { return filepath.Join(s.Path, "sessions") }

// Submodules lists submodule dirs sorted by name.
func (r *Repo) Submodules() ([]Submodule, error) {
	base := filepath.Join(r.Root, "submodules")
	ents, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Submodule
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		out = append(out, Submodule{Name: e.Name(), Path: filepath.Join(base, e.Name())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Dormant reports whether a submodule has no ROI.md and is never selected.
func (s Submodule) Dormant() bool {
	_, err := os.Stat(filepath.Join(s.Path, ROIFile))
	return os.IsNotExist(err)
}

// NeedsBootstrap reports ROI present but PLAN absent.
func (s Submodule) NeedsBootstrap() bool {
	_, roiErr := os.Stat(filepath.Join(s.Path, ROIFile))
	_, planErr := os.Stat(s.PlanPath())
	return roiErr == nil && os.IsNotExist(planErr)
}

// ROIStamp reads the last-reconciled ROI commit from PLAN.md, "" if none.
func (s Submodule) ROIStamp() (string, error) {
	b, err := os.ReadFile(s.PlanPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if m := roiStamp.FindSubmatch(b); m != nil {
		return string(m[1]), nil
	}
	return "", nil
}

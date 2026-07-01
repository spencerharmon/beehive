// Package repo models the beehive repo layout and derives all state from files.
// The frontend and honeybee read state through this package; nothing else owns truth.
package repo

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// Layout names. ROI is human-owned and never written by honeybees.
const (
	AgentsFile  = "AGENTS.md"
	PlanFile    = "PLAN.md"
	ROIFile     = "ROI.md"
	InfraFile   = "INFRASTRUCTURE.md"
	Artifacts   = "ARTIFACTS.md"
	LinksFile   = "SUBMODULE-LINKS.yaml"
	SecretsFile = "SECRETS.yaml.gpg"
)

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

// PlanRel is PlanPath as a repo-root-relative pathspec (submodules/<name>/PLAN.md).
// Claim commits stage PLAN.md with this EXPLICIT pathspec rather than a blanket
// `add -A`, so a commit taken while a code worktree exists under
// submodules/<name>/worktrees/ can never absorb that worktree as an orphan gitlink.
func (s Submodule) PlanRel() string { return filepath.Join("submodules", s.Name, PlanFile) }

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

package web

import (
	"context"
	"strconv"
	"strings"

	"github.com/spencerharmon/beehive/internal/git"
)

// Commit is one entry of a per-submodule commit graph section.
type Commit struct {
	SHA     string
	Refs    string
	Subject string
	Author  string
	Date    string
	Doc     string // derived Beehive change-doc stamp, "" if none
}

// commitGraph returns one paginated section of a single submodule's repo
// history. It never crawls across submodules. limit/offset bound the section.
func commitGraph(ctx context.Context, repoDir string, offset, limit int) ([]Commit, error) {
	g := git.New(repoDir)
	// %x1f field sep, %x1e record sep; %b is the body carrying the stamp.
	fmt := "--pretty=format:%H%x1f%d%x1f%s%x1f%an%x1f%ad%x1f%b%x1e"
	out, err := g.Run(ctx, "log", "--date=short",
		"--skip="+strconv.Itoa(offset), "-n", strconv.Itoa(limit), fmt)
	if err != nil {
		return nil, err
	}
	var cs []Commit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		f := strings.Split(rec, "\x1f")
		if len(f) < 6 {
			continue
		}
		cs = append(cs, Commit{
			SHA:     f[0][:min(12, len(f[0]))],
			Refs:    strings.Trim(strings.TrimSpace(f[1]), "()"),
			Subject: f[2],
			Author:  f[3],
			Date:    f[4],
			Doc:     docFromMessage(ctx, f[5]),
		})
	}
	return cs, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

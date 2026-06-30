package web

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/repo"
)

// Commit is one row of a single submodule's commit graph. The Doc* fields are
// derived from the commit's `Beehive: <taskid> <docpath>` stamp: DocTask and
// DocPath are for display, and DocHref is a link to VIEW that change doc when
// the docpath resolves to a real file under the submodule's docs/ dir. DocHref
// is "" when the stamp is absent or the doc is missing, so the template shows
// plain text instead of a dead link.
type Commit struct {
	SHA     string
	Refs    string
	Subject string
	Author  string
	Date    string
	DocTask string // task id from the Beehive stamp, "" if none
	DocPath string // change-doc path from the stamp (display), "" if none
	DocHref string // link to view the change doc, "" if it does not resolve
}

// Section groups one submodule's commits by date for the sectioned branch view.
// git log already returns commits newest-first, so consecutive same-date commits
// fall into one section.
type Section struct {
	Date    string
	Commits []Commit
}

// commitGraph returns one paginated page of a SINGLE submodule's repo history.
// It reads only repoDir, so it can never crawl across submodules. offset/limit
// bound the page (the caller caps limit via pageParams). The Beehive change-doc
// stamp is split into DocTask/DocPath here; href resolution (which needs the
// submodule's docs/ dir) is the caller's job via resolveDocHref.
func commitGraph(ctx context.Context, repoDir string, offset, limit int) ([]Commit, error) {
	g := git.New(repoDir)
	// %x1f field sep, %x1e record sep; %b is the body carrying the stamp.
	format := "--pretty=format:%H%x1f%d%x1f%s%x1f%an%x1f%ad%x1f%b%x1e"
	out, err := g.Run(ctx, "log", "--date=short",
		"--skip="+strconv.Itoa(offset), "-n", strconv.Itoa(limit), format)
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
		task, doc := splitStamp(docFromMessage(ctx, f[5]))
		cs = append(cs, Commit{
			SHA:     f[0][:min(12, len(f[0]))],
			Refs:    strings.Trim(strings.TrimSpace(f[1]), "()"),
			Subject: f[2],
			Author:  f[3],
			Date:    f[4],
			DocTask: task,
			DocPath: doc,
		})
	}
	return cs, nil
}

// splitStamp splits docFromMessage's "<taskid> <docpath>" into its two fields;
// both are "" when the commit carries no Beehive stamp.
func splitStamp(stamp string) (task, doc string) {
	if parts := strings.Fields(stamp); len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

// resolveDocHref returns a link to view docPath's change doc when it names a
// real file under the submodule's docs/ dir, else "". Only the basename is used
// and it is traversal-guarded, so a link can never escape submodules/<sm>/docs/
// or reach another submodule.
func resolveDocHref(sm repo.Submodule, docPath string) string {
	if docPath == "" {
		return ""
	}
	base := filepath.Base(docPath)
	if !safeBranch(base) {
		return ""
	}
	if _, err := os.Stat(filepath.Join(sm.Path, "docs", base)); err != nil {
		return ""
	}
	return "/submodule/" + sm.Name + "/doc/" + base
}

// sectionByDate groups a single submodule's date-ordered commits (git log is
// newest-first) into per-date sections for the sectioned view, preserving order.
func sectionByDate(cs []Commit) []Section {
	var secs []Section
	for _, c := range cs {
		if n := len(secs); n > 0 && secs[n-1].Date == c.Date {
			secs[n-1].Commits = append(secs[n-1].Commits, c)
			continue
		}
		secs = append(secs, Section{Date: c.Date, Commits: []Commit{c}})
	}
	return secs
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

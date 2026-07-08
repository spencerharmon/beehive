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
//
// Flip* are delivery-traceability's half (a): when DocTask names a task that is
// DONE, they link to the HIVE superproject commit that flipped it to DONE (see
// hiveDoneFlips/indexDeliveries in delivery.go). Both are "" when DocTask is
// empty, the task isn't DONE, or the flip commit can't be located — never a
// dead link, matching DocHref's own contract.
type Commit struct {
	SHA      string
	Refs     string
	Subject  string
	Author   string
	Date     string
	DocTask  string // task id from the Beehive stamp, "" if none
	DocPath  string // change-doc path from the stamp (display), "" if none
	DocHref  string // link to view the change doc, "" if it does not resolve
	FlipSHA  string // short hive commit sha that flipped DocTask to DONE, "" if not applicable/unlocated
	FlipHref string // link to view that hive commit, "" if not applicable/unlocated
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

// changeDocsByTask scans a SINGLE submodule repo's history for Beehive change-doc
// stamps ("Beehive: <taskid> <docpath>") and returns task id -> change-doc path,
// the NEWEST stamping commit winning (git log is newest-first, so the first
// occurrence of a task id is kept). It is how the plan view links each task to
// the change doc its implementing commit recorded — the same stamp the branch
// view reads. Reads only repoDir, so it never crawls another submodule; a missing
// or empty history yields an empty map (no links, never an error to the page).
func changeDocsByTask(ctx context.Context, repoDir string) map[string]string {
	g := git.New(repoDir)
	// %b is the commit body carrying the stamp; %x1e separates records.
	out, err := g.Run(ctx, "log", "--pretty=format:%b%x1e")
	if err != nil {
		return nil
	}
	docs := map[string]string{}
	for _, rec := range strings.Split(out, "\x1e") {
		task, doc := splitStamp(docFromMessage(ctx, rec))
		if task == "" || doc == "" {
			continue
		}
		if _, seen := docs[task]; !seen { // newest-first: keep the latest stamp
			docs[task] = doc
		}
	}
	return docs
}

// resolveDocHref returns a link to VIEW docPath's doc (via the doc handler,
// web.go's `doc`) when it names a real file under the submodule's docs/ dir,
// else "". docPath may be any of the conventions a caller hands it (plan-
// view-detail-polish unified these, previously audited as two independent
// resolvers): a flat `Beehive: <taskid> <docpath>` commit-stamp basename
// ("bee-t1.md"), that same stamp prefixed with "docs/" ("docs/bee-t1.md") or
// with the beehive-layer's own submodule root ("submodules/<name>/docs/bee-
// t1.md" — some commits stamp the doc path from the hive root instead of the
// submodule root), or a PLAN.md "Doc:" design-doc convention line, which nests
// under a docs/ subdirectory ("docs/tasks/t1.md", "docs/audit/....md"). Every
// form normalizes (normalizeDocPath) to one slash-separated path relative to
// the submodule's docs/ dir, traversal/charset-guarded via safeDocPath (nested
// segments allowed, unlike the single-segment safeBranch this used before), so
// a link can never escape submodules/<sm>/docs/ or reach another submodule; a
// doc that does not resolve under ANY of these forms returns "" — never a dead
// link.
func resolveDocHref(sm repo.Submodule, docPath string) string {
	rel := normalizeDocPath(sm, docPath)
	if rel == "" {
		return ""
	}
	if _, err := os.Stat(filepath.Join(sm.Path, "docs", filepath.FromSlash(rel))); err != nil {
		return ""
	}
	return "/submodule/" + sm.Name + "/doc/" + rel
}

// normalizeDocPath reduces a raw doc-path stamp/convention string (see
// resolveDocHref) to a slash-separated path relative to the submodule's docs/
// dir, or "" when it is empty or fails validation. It strips an optional
// "submodules/<sm.Name>/" hive-root prefix, then an optional leading "docs/"
// component (the convention every known form uses to anchor itself under the
// submodule's docs/ dir); the remainder is validated via safeDocPath
// (traversal/charset-guarded, nested segments allowed — docs/tasks/*,
// docs/audit/*, ...). A path that never had a "docs/" component to anchor it
// (i.e. some other, unanticipated convention) degrades to just its basename,
// validated the same way — this keeps every historical flat stamp resolving
// exactly as before rather than rejecting it outright.
func normalizeDocPath(sm repo.Submodule, docPath string) string {
	if docPath == "" {
		return ""
	}
	p := strings.TrimPrefix(filepath.ToSlash(docPath), "submodules/"+sm.Name+"/")
	if rest, ok := strings.CutPrefix(p, "docs/"); ok {
		p = rest
	} else {
		p = filepath.Base(p)
	}
	if !safeDocPath(p) {
		return ""
	}
	return p
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

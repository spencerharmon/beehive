package web

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
)

// subStat is one submodule's honeybee-performance figures, all derived on read
// from git — never stored, so they can't drift from reality (the same signals as
// skills/bin/beehive-stats.sh; see docs/conflict-resolution.md).
//
//	DeliveredTasks (✅) = tasks at PLAN [DONE] — a task can take more than one
//	                     merge, so we count the task, not merges.
//	Honeybees      (🐝) = session transcript files (one per honeybee pass).
//	Stranded            = tasks with a stamped bee-<task> branch ahead of main that
//	                     never merged (finished work whose merge didn't land — the
//	                     wedge indicator; not lost, GC never drops an unmerged branch).
//	DeliveredPerBeePct  = 100 * DeliveredTasks / Honeybees   (the ✅/🐝 yield)
type subStat struct {
	Name               string
	DeliveredTasks     int
	Honeybees          int
	Stranded           int
	DeliveredPerBeePct float64
}

var sessionNameRE = regexp.MustCompile(`^bee-(.+)-\d+-\d+$`)

func (st *subStat) derive() {
	if st.Honeybees > 0 {
		st.DeliveredPerBeePct = 100 * float64(st.DeliveredTasks) / float64(st.Honeybees)
	}
}

// computeStats returns per-submodule figures plus a total row.
func (s *Server) computeStats(ctx context.Context) (subs []subStat, total subStat, err error) {
	sms, err := s.repo.Submodules()
	if err != nil {
		return nil, subStat{}, err
	}
	total.Name = "total"
	for _, sm := range sms {
		st := subStat{Name: sm.Name}
		done := map[string]bool{}
		if b, rerr := os.ReadFile(sm.PlanPath()); rerr == nil {
			if p, perr := plan.Parse(string(b)); perr == nil {
				for _, t := range p.Tasks {
					if t.Status == plan.StatusDone {
						done[t.ID] = true
						st.DeliveredTasks++
					}
				}
			}
		}
		if ents, rerr := os.ReadDir(sm.SessionsDir()); rerr == nil {
			for _, e := range ents {
				if !strings.HasSuffix(e.Name(), ".md") {
					continue
				}
				if sessionNameRE.MatchString(strings.TrimSuffix(e.Name(), ".md")) {
					st.Honeybees++
				}
			}
		}
		st.Stranded = strandedCount(ctx, git.New(sm.RepoDir()), done)
		st.derive()
		subs = append(subs, st)
		total.DeliveredTasks += st.DeliveredTasks
		total.Honeybees += st.Honeybees
		total.Stranded += st.Stranded
	}
	total.derive()
	return subs, total, nil
}

// strandedCount counts bee-<task> branches ahead of the submodule's pull target
// that carry the task's completion stamp but whose task isn't DONE — finished
// work whose merge never landed. Best-effort: any git error yields 0 for that
// submodule rather than failing the page.
func strandedCount(ctx context.Context, g *git.Repo, done map[string]bool) int {
	ref := "main"
	if _, err := g.RevParse(ctx, "origin/main"); err == nil {
		ref = "origin/main"
	}
	out, err := g.Run(ctx, "for-each-ref", "--format=%(refname:short)", "refs/remotes/origin/bee-*")
	if err != nil {
		return 0
	}
	n := 0
	for _, br := range strings.Fields(out) {
		i := strings.Index(br, "/bee-")
		if i < 0 {
			continue
		}
		task := br[i+len("/bee-"):]
		if done[task] {
			continue
		}
		if c, _ := g.Run(ctx, "rev-list", "--count", ref+".."+br); strings.TrimSpace(c) == "0" || strings.TrimSpace(c) == "" {
			continue
		}
		tr, _ := g.Run(ctx, "log", ref+".."+br, "--format=%(trailers:key=Beehive,valueonly)")
		for _, line := range strings.Split(tr, "\n") {
			if f := strings.Fields(line); len(f) > 0 && f[0] == task {
				n++
				break
			}
		}
	}
	return n
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	subs, total, err := s.computeStats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "stats.html", map[string]interface{}{"Subs": subs, "Total": total})
}

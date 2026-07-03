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
//	Delivered = tasks at PLAN [DONE] (merged + reviewed + live)
//	Sessions  = session transcript files (one per honeybee pass / one "bee")
//	Stranded  = stamped bee-branches ahead of main whose task isn't DONE
//	            (finished work whose merge never landed — the wedge indicator)
//	MergesPerBeePct = 100 * Delivered / Sessions   (the m/🐝 yield)
//	SessionsPerTask = Sessions / distinct tasks worked (retry pressure)
type subStat struct {
	Name            string
	Delivered       int
	Sessions        int
	DistinctTasks   int
	Stranded        int
	SessionsPerTask float64
	MergesPerBeePct float64
}

var sessionNameRE = regexp.MustCompile(`^bee-(.+)-\d+-\d+$`)

func (st *subStat) derive() {
	if st.DistinctTasks > 0 {
		st.SessionsPerTask = float64(st.Sessions) / float64(st.DistinctTasks)
	}
	if st.Sessions > 0 {
		st.MergesPerBeePct = 100 * float64(st.Delivered) / float64(st.Sessions)
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
						st.Delivered++
					}
				}
			}
		}
		tasks := map[string]bool{}
		if ents, rerr := os.ReadDir(sm.SessionsDir()); rerr == nil {
			for _, e := range ents {
				name := e.Name()
				if !strings.HasSuffix(name, ".md") {
					continue
				}
				m := sessionNameRE.FindStringSubmatch(strings.TrimSuffix(name, ".md"))
				if m == nil {
					continue
				}
				st.Sessions++
				tasks[m[1]] = true
			}
		}
		st.DistinctTasks = len(tasks)
		st.Stranded = strandedCount(ctx, git.New(sm.RepoDir()), done)
		st.derive()
		subs = append(subs, st)
		total.Delivered += st.Delivered
		total.Sessions += st.Sessions
		total.DistinctTasks += st.DistinctTasks
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

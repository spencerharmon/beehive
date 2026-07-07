package web

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spencerharmon/beehive/internal/git"
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
//	Models              = the same figures split by the agent model each session
//	                     ran on (transcript-header `model:` stamp), for A/B
//	                     comparison across models. Delivered is attributed to the
//	                     model of a DONE task's most-recent session.
//	Deliveries          = delivery-traceability: one DeliveryLink per DONE task,
//	                     linking the hive commit that flipped it to DONE and the
//	                     submodule commit/doc that carries its code (see delivery.go).
type subStat struct {
	Name               string
	DeliveredTasks     int
	Honeybees          int
	Stranded           int
	DeliveredPerBeePct float64
	Models             []modelStat
	Deliveries         []DeliveryLink
}

// modelStat is one agent model's slice of a submodule's (or the total's)
// performance, derived from the per-session transcript `model:` stamp.
type modelStat struct {
	Model              string
	DeliveredTasks     int
	Honeybees          int
	DeliveredPerBeePct float64
}

func (m *modelStat) derive() {
	if m.Honeybees > 0 {
		m.DeliveredPerBeePct = 100 * float64(m.DeliveredTasks) / float64(m.Honeybees)
	}
}

// defaultModel labels sessions whose transcript predates the model stamp (or was
// written by a build without it). This host has only ever run opus, so crediting
// unstamped history to it is exact rather than a guess (operator-approved).
const defaultModel = "github-copilot/claude-opus-4.8"

// sessionNameRE splits a transcript stem `bee-<task>-<epoch>-<pid>` into the task
// id (1) and the epoch (2) / pid (3) that order a task's repeated attempts.
var sessionNameRE = regexp.MustCompile(`^bee-(.+)-(\d+)-(\d+)$`)

// modelStampRE pulls the model out of the transcript header's metadata line
// ("submodule: … · kind: … · branch: … · model: <model>").
var modelStampRE = regexp.MustCompile(`· model: (\S+)`)

func (st *subStat) derive() {
	if st.Honeybees > 0 {
		st.DeliveredPerBeePct = 100 * float64(st.DeliveredTasks) / float64(st.Honeybees)
	}
}

// sessionModel reads a transcript's header (bounded — the stamp is on line 3) and
// returns the model it ran on, or defaultModel when unstamped/unreadable.
func sessionModel(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return defaultModel
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if m := modelStampRE.FindSubmatch(buf[:n]); m != nil {
		return string(m[1])
	}
	return defaultModel
}

// computeStats returns per-submodule figures plus a total row.
func (s *Server) computeStats(ctx context.Context) (subs []subStat, total subStat, err error) {
	sms, err := s.repo.Submodules()
	if err != nil {
		return nil, subStat{}, err
	}
	total.Name = "total"
	// Total-row per-model accumulators, summed across submodules.
	totBees := map[string]int{}
	totDelivered := map[string]int{}
	// Resolved ONCE and shared across every submodule's delivery lookup below,
	// so a multi-submodule /stats render pays a single `rev-parse`, not one per
	// submodule (mirrors headSHA's own doc comment / the planView cache key).
	head := s.headSHA(ctx)
	for _, sm := range sms {
		st := subStat{Name: sm.Name}
		doneIDs := doneTaskIDs(sm)
		done := make(map[string]bool, len(doneIDs))
		for _, id := range doneIDs {
			done[id] = true
		}
		st.DeliveredTasks = len(doneIDs)
		// delivery-traceability: link each DONE task to the hive commit that
		// flipped it (half a) and its submodule code/doc (half b) — see
		// delivery.go. Best-effort/read-only; never fails the page.
		st.Deliveries = s.buildDeliveries(ctx, head, sm, doneIDs)
		// Per-model tallies for this submodule, plus the model of each task's
		// most-recent session (epoch then pid) so a DONE task's delivery is
		// attributed to the model that last drove it.
		bees := map[string]int{}
		type latest struct {
			epoch, pid int
			model      string
		}
		taskLatest := map[string]latest{}
		if ents, rerr := os.ReadDir(sm.SessionsDir()); rerr == nil {
			for _, e := range ents {
				if !strings.HasSuffix(e.Name(), ".md") {
					continue
				}
				stem := strings.TrimSuffix(e.Name(), ".md")
				m := sessionNameRE.FindStringSubmatch(stem)
				if m == nil {
					continue
				}
				st.Honeybees++
				model := sessionModel(filepath.Join(sm.SessionsDir(), e.Name()))
				bees[model]++
				task := m[1]
				epoch, _ := strconv.Atoi(m[2])
				pid, _ := strconv.Atoi(m[3])
				if cur, ok := taskLatest[task]; !ok || epoch > cur.epoch || (epoch == cur.epoch && pid > cur.pid) {
					taskLatest[task] = latest{epoch, pid, model}
				}
			}
		}
		// Attribute each delivered task to its latest session's model.
		delivered := map[string]int{}
		for task := range done {
			if l, ok := taskLatest[task]; ok {
				delivered[l.model]++
			}
		}
		st.Models = buildModelStats(bees, delivered)
		for mdl, n := range bees {
			totBees[mdl] += n
		}
		for mdl, n := range delivered {
			totDelivered[mdl] += n
		}
		st.Stranded = strandedCount(ctx, git.New(sm.RepoDir()), done)
		st.derive()
		subs = append(subs, st)
		total.DeliveredTasks += st.DeliveredTasks
		total.Honeybees += st.Honeybees
		total.Stranded += st.Stranded
	}
	total.Models = buildModelStats(totBees, totDelivered)
	total.derive()
	return subs, total, nil
}

// buildModelStats folds the per-model session and delivered tallies into a stable,
// display-ordered slice (most honeybees first, then model name) with the yield
// derived. A model appears if it ran any session or delivered any task.
func buildModelStats(bees, delivered map[string]int) []modelStat {
	seen := map[string]bool{}
	for m := range bees {
		seen[m] = true
	}
	for m := range delivered {
		seen[m] = true
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]modelStat, 0, len(seen))
	for m := range seen {
		ms := modelStat{Model: m, Honeybees: bees[m], DeliveredTasks: delivered[m]}
		ms.derive()
		out = append(out, ms)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Honeybees != out[j].Honeybees {
			return out[i].Honeybees > out[j].Honeybees
		}
		return out[i].Model < out[j].Model
	})
	return out
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

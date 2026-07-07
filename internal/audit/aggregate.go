package audit

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spencerharmon/beehive/internal/plan"
)

// TaskAgg is the per-task rollup across every session sharing a TaskID. Reruns is
// the session count (links-graph-enforcement -> 3, reconcile -> 2 in the current
// corpus); Retries is Reruns-1 (the wasted re-attempts). Turns and Bytes sum the
// task's sessions. Delivered is true only when the task reached DONE on main.
type TaskAgg struct {
	TaskID    string
	Sessions  []string // session IDs, epoch-sorted
	Reruns    int      // == len(Sessions)
	Retries   int      // Reruns - 1
	Turns     int      // summed across sessions
	Bytes     int64    // summed across sessions
	Delivered bool
}

// Aggregate groups sessions by TaskID and marks each task delivered per the
// delivered set (taskid -> reached DONE). Sessions are assumed epoch-sorted (as
// ParseDir returns them); the result is sorted by TaskID for determinism.
func Aggregate(sessions []Session, delivered map[string]bool) []TaskAgg {
	idx := map[string]*TaskAgg{}
	var order []string
	for _, s := range sessions {
		a := idx[s.TaskID]
		if a == nil {
			a = &TaskAgg{TaskID: s.TaskID, Delivered: delivered[s.TaskID]}
			idx[s.TaskID] = a
			order = append(order, s.TaskID)
		}
		a.Sessions = append(a.Sessions, s.ID)
		a.Turns += s.Turns
		a.Bytes += s.Bytes
	}
	out := make([]TaskAgg, 0, len(order))
	for _, id := range order {
		a := idx[id]
		a.Reruns = len(a.Sessions)
		a.Retries = a.Reruns - 1
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	return out
}

// Trend is the tracked metric for one audit pass: the cost carried by DELIVERED
// tasks only, so successive passes can show whether the swarm is getting cheaper
// per shipped task. Totals are stored (not averages) to keep the ledger free of
// rounding; per-task averages are derived on read.
type Trend struct {
	Pass           int
	DeliveredTasks int
	Turns          int   // total turns across delivered tasks
	Bytes          int64 // total bytes across delivered tasks
	Retries        int   // total retries across delivered tasks
}

// ComputeTrend sums the delivered-only cost for a pass.
func ComputeTrend(aggs []TaskAgg, pass int) Trend {
	t := Trend{Pass: pass}
	for _, a := range aggs {
		if !a.Delivered {
			continue
		}
		t.DeliveredTasks++
		t.Turns += a.Turns
		t.Bytes += a.Bytes
		t.Retries += a.Retries
	}
	return t
}

// TurnsPerTask, BytesPerTask, RetriesPerTask are the derived per-delivered-task
// averages (zero when no delivered task, never a divide-by-zero panic).
func (t Trend) TurnsPerTask() float64   { return ratio(t.Turns, t.DeliveredTasks) }
func (t Trend) BytesPerTask() float64   { return ratio(int(t.Bytes), t.DeliveredTasks) }
func (t Trend) RetriesPerTask() float64 { return ratio(t.Retries, t.DeliveredTasks) }

func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

// DeliveredFromPlan collects the taskids that reached DONE on main, the only
// definition of "delivered". It reads the parsed PLAN.md (beehive layer); the
// caller passes plan.Parse(planBytes).
func DeliveredFromPlan(p *plan.Plan) map[string]bool {
	out := map[string]bool{}
	for _, t := range p.Tasks {
		if t.Status == plan.StatusDone {
			out[t.ID] = true
		}
	}
	return out
}

// Stub is one UNFINALIZED session file: a repo.SessionStub placeholder still on
// main while its transcript streams to a live branch. It is a known, expected
// shape (repo.ParseSessionStub recognises it), NOT corrupt noise — so the audit
// engine records it as an unfinalized stub (its sid and the branch it points to)
// rather than mis-filing it as a malformed parse error. A stub carries no
// mineable metrics yet; it becomes a finalized transcript when its session ends
// (that finalization is session-transcript-finalize's job, not audit's).
type Stub struct {
	SID    string // session id == the file stem (name without the ".md")
	Branch string // the live branch the stub points to
}

// Census is the corpus-integrity summary of a sessions directory: how many files
// are finalized (mineable) transcripts, how many are still unfinalized stubs, and
// how many are genuinely malformed. It exists to tell an EMPTY audit window apart
// from a rested swarm — empty-because-audited (healthy) versus
// empty-because-unfinalized (a corpus-loss defect that is byte-for-byte identical
// to a rest without this census). Reporting only: nothing here finalizes a stub.
type Census struct {
	// Sessions are the finalized, mineable transcripts, epoch-sorted (exactly the
	// slice ParseDir returns).
	Sessions []Session
	// Stubs are the unfinalized stub files, deterministically ordered by SID.
	Stubs []Stub
	// Errors are the genuinely malformed files (missing/garbled header, unreadable
	// bytes) — NEVER stubs. One error per bad file, none swallowed.
	Errors []error
}

// Finalized is the count of mineable transcripts.
func (c Census) Finalized() int { return len(c.Sessions) }

// StubCount is the number of unfinalized stub files.
func (c Census) StubCount() int { return len(c.Stubs) }

// ErrorCount is the number of genuinely malformed files.
func (c Census) ErrorCount() int { return len(c.Errors) }

// Total is every classified ".md" file: finalized + stubs + malformed.
func (c Census) Total() int { return c.Finalized() + c.StubCount() + c.ErrorCount() }

// MineableFraction is the share of the corpus that is finalized and thus
// mineable (finalized / total). An empty directory is defined as fully mineable
// (1.0): nothing is broken, there is simply nothing there — so a genuinely empty
// sessions dir never trips the corpus-broken warning.
func (c Census) MineableFraction() float64 {
	total := c.Total()
	if total == 0 {
		return 1
	}
	return float64(c.Finalized()) / float64(total)
}

// LowMineableFraction is the threshold below which a corpus that carries stubs is
// judged "mostly unfinalized" and the loud warning fires even before the audit
// window is fully empty. Chosen so a handful of live-streaming sessions against a
// large finalized corpus stays quiet, while a corpus that is majority stubs (the
// 96%-corpus-loss defect) is caught.
const LowMineableFraction = 0.5

// CorpusBroken reports the "broken, not rested" defect across BOTH integrity
// classes it guards: unfinalized stubs (stubs exist AND either the mineable
// audit window is empty — nothing to mine, yet the corpus is NOT actually
// rested — or the mineable fraction has fallen below LowMineableFraction, i.e.
// the corpus is mostly unfinalized) OR genuinely malformed files (ErrorCount() >
// 0). A malformed file is a parser/producer defect that must self-announce
// unconditionally — independent of windowEmpty and the stub fraction — so a
// malformed-header regression can never hide behind an otherwise stub-free
// corpus (the exact silent-starvation collapse this guards against: once the
// legacy stubs this guard was written against are gone, a malformed-only window
// would otherwise be byte-for-byte indistinguishable from a rest). A corpus with
// zero stubs AND zero errors is never broken, so a healthy rested swarm — its
// window empty only because everything was audited — stays byte-for-byte
// silent.
func (c Census) CorpusBroken(windowEmpty bool) bool {
	if c.ErrorCount() > 0 {
		return true
	}
	if len(c.Stubs) == 0 {
		return false
	}
	return windowEmpty || c.MineableFraction() < LowMineableFraction
}

// CorpusWarning returns the loud, human-facing "corpus broken, not a rest" banner
// when the census+window is broken (per CorpusBroken), or "" when the corpus is
// healthy. It is byte-stable: a corpus with zero stubs and zero errors yields
// exactly zero bytes, so a caller can print the result unconditionally and a
// healthy pass stays silent. Stubs and malformed files are distinct defects with
// distinct fixes, so the banner names each class it finds and steers to its own
// fix independently — a stub line pointing at session-transcript-finalize, a
// malformed line pointing at audit-parse-model-header — rather than crediting a
// malformed spike to the unrelated finalize path (or vice versa). Reporting
// only — it names the defect(s); it does not finalize a stub or parse a
// malformed file.
func (c Census) CorpusWarning(windowEmpty bool) string {
	if !c.CorpusBroken(windowEmpty) {
		return ""
	}
	var b strings.Builder
	b.WriteString("================ CORPUS BROKEN, NOT A REST ================\n")
	if len(c.Stubs) > 0 {
		fmt.Fprintf(&b, "audit: %d of %d session files are UNFINALIZED stubs (mineable fraction %.2f, threshold %.2f).\n",
			c.StubCount(), c.Total(), c.MineableFraction(), LowMineableFraction)
	}
	if c.ErrorCount() > 0 {
		fmt.Fprintf(&b, "audit: %d of %d session files are MALFORMED (unparseable header/body).\n",
			c.ErrorCount(), c.Total())
	}
	if windowEmpty {
		b.WriteString("The audit window is EMPTY because the corpus is broken, NOT because the swarm rested.\n")
	}
	if len(c.Stubs) > 0 {
		b.WriteString("Do NOT read this as 'nothing to audit'. Finalize the streaming sessions so their transcripts become mineable (session-transcript-finalize).\n")
	}
	if c.ErrorCount() > 0 {
		b.WriteString("Do NOT read this as 'nothing to audit'. Fix the parser/schema drift producing malformed session files (audit-parse-model-header).\n")
	}
	b.WriteString("===========================================================\n")
	return b.String()
}

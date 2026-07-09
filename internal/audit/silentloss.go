package audit

import "sort"

// markSilentLosses sets Heuristics.SilentLoss on every task-bearing session whose
// successful status flip never reached main — detected purely from the corpus
// shape, no git access (internal/audit reads only the beehive layer).
//
// For each task, its task-bearing sessions (work/review/arbitrate) are walked in
// epoch order and a session is flagged when the session IMMEDIATELY FOLLOWING it
// for the same task carries the SAME kind. Kind is the header-authoritative proxy
// for the status a session was dispatched at (work⟺TODO, review⟺NEEDS-REVIEW,
// arbitrate⟺NEEDS-ARBITRATION), so a same-kind successor means the task was
// handed out again at the identical status: the earlier session's own flip never
// landed and its whole run was discarded. The LATER session in each pair is the
// one that actually re-did the work, so only the EARLIER (superseded) session is
// flagged; a session whose successor is a DIFFERENT kind advanced the task (its
// flip landed) and is never flagged — so a legitimate work→review→arbitrate
// progression yields zero silent losses.
//
// Non-task-bearing kinds are excluded (mirroring isTaskBearingKind's use in the
// abort path): bootstrap and reconcile own no task-status handoff AND share a
// synthetic TaskID ("bootstrap"/"reconcile") across unrelated runs, so folding
// them in would misread the reconcile-loop pattern (adjacent reconcile sessions,
// already its own heuristic) as a silent loss. A real task's sessions are ALL
// task-bearing, so this filter never drops one of them — its only effect is to
// skip those two synthetic groups. Task-bearing sessions of OTHER kinds are kept
// in the sequence, which is exactly right: an interleaved review or arbitration
// between two work sessions is EVIDENCE the earlier work's flip landed (the task
// advanced far enough to be reviewed), and because it is a different kind it
// breaks the same-kind adjacency — so work→review→work flags nothing, while
// work→work→review flags only the first work (its successor is another work at
// the identical status). Only same-kind consecutive pairs are ever flagged.
//
// Input must be epoch-sorted (as ParseDirCensus guarantees). The pass mutates
// s in place by index and is order-independent across tasks, so a caller may run
// it once over the whole corpus.
func markSilentLosses(s []Session) {
	// Collect, per task, the indices of its task-bearing sessions in the epoch
	// order the slice already carries.
	idxByTask := map[string][]int{}
	for i := range s {
		if !isTaskBearingKind(s[i].Kind) {
			continue
		}
		idxByTask[s[i].TaskID] = append(idxByTask[s[i].TaskID], i)
	}
	for _, idxs := range idxByTask {
		for j := 0; j+1 < len(idxs); j++ {
			cur, next := idxs[j], idxs[j+1]
			if s[cur].Kind == s[next].Kind {
				s[cur].Heuristics.SilentLoss = true
			}
		}
	}
}

// SilentLossAgg is the per-task rollup of silent losses: the flagged (superseded)
// sessions for one task and the turns/bytes their discarded runs cost. It mirrors
// TaskAgg's shape so the read-only `beehive audit` output can surface it as a
// sibling section. Only tasks with at least one silent loss appear.
type SilentLossAgg struct {
	TaskID   string
	Sessions []string // the flagged (lost) session IDs, epoch-sorted
	Count    int      // == len(Sessions): silent losses for this task
	Turns    int      // summed turns of the flagged sessions (wasted)
	Bytes    int64    // summed bytes of the flagged sessions (wasted)
}

// AggregateSilentLoss groups the SilentLoss-flagged sessions by TaskID, summing
// the wasted turns and bytes. Sessions are assumed epoch-sorted (as ParseDir
// returns them, with SilentLoss already set by markSilentLosses); the result is
// sorted by TaskID for deterministic output. A corpus with no silent loss yields
// an empty (non-nil-safe) slice.
func AggregateSilentLoss(sessions []Session) []SilentLossAgg {
	idx := map[string]*SilentLossAgg{}
	var order []string
	for _, s := range sessions {
		if !s.Heuristics.SilentLoss {
			continue
		}
		a := idx[s.TaskID]
		if a == nil {
			a = &SilentLossAgg{TaskID: s.TaskID}
			idx[s.TaskID] = a
			order = append(order, s.TaskID)
		}
		a.Sessions = append(a.Sessions, s.ID)
		a.Turns += s.Turns
		a.Bytes += s.Bytes
	}
	out := make([]SilentLossAgg, 0, len(order))
	for _, id := range order {
		a := idx[id]
		a.Count = len(a.Sessions)
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	return out
}

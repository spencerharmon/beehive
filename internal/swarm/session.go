package swarm

import (
	"fmt"
	"os"
	"time"
)

// SessionID builds a time-sortable id used for session-file names and honeybee
// worktree branches. The form is
//
//	<branch>-<unix-epoch-seconds>-<pid>
//
// e.g. bee-T3-1751210912-4242. The epoch keeps re-runs on the same branch
// (work, GC, review, re-work happen seconds-to-minutes apart) from colliding and
// sorts chronologically; the pid disambiguates honeybees that start in the SAME
// second, which happens under fan-out (several honeybee processes launched at
// once all derive their per-submodule worktree branch from this id). Concurrent
// runs on one *task* still can't occur — that is claim-gated — but concurrent
// processes on one submodule can, and need distinct worktree branches.
func SessionID(branch string, now time.Time) string {
	return fmt.Sprintf("%s-%d-%d", branch, now.Unix(), os.Getpid())
}

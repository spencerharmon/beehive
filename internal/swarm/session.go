package swarm

import (
	"fmt"
	"time"
)

// SessionID builds a time-sortable id used for session-file names (branch = the
// task branch) and honeybee worktree branches (branch = "bee"). The form is
//
//	<branch>-<unix-epoch-seconds>
//
// e.g. bee-T3-1751210912. The epoch suffix keeps re-runs on the same branch
// (work, GC, review, re-work happen seconds-to-minutes apart) from colliding and
// sorts chronologically. Concurrent runs on one task can't occur (claim-gated).
func SessionID(branch string, now time.Time) string {
	return fmt.Sprintf("%s-%d", branch, now.Unix())
}

package swarm

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"time"
)

// SessionID builds a unique, time-sortable, human-readable id. Used both for
// session-file names (branch = the task branch) and for honeybee worktree
// branches (branch = "bee"). The form is
//
//	<branch>-<YYYYMMDD-HHMMSS>-<adjective>-<noun>
//
// e.g. bee-T3-20260629-153012-amber-otter — collision-resistant across
// concurrent honeybees via the random slug.
func SessionID(branch string, now time.Time) string {
	return fmt.Sprintf("%s-%s-%s", branch, now.UTC().Format("20060102-150405"), slug())
}

func slug() string {
	return pick(slugAdjectives) + "-" + pick(slugNouns)
}

func pick(words []string) string {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(words))))
	if err != nil {
		// crypto/rand failure is effectively impossible; fall back to first word
		// rather than panic in a recorder path.
		return words[0]
	}
	return words[n.Int64()]
}

var slugAdjectives = []string{
	"amber", "azure", "brave", "brisk", "calm", "clever", "coral", "crimson",
	"dapper", "eager", "fleet", "gentle", "golden", "hardy", "ivory", "jolly",
	"keen", "lively", "lunar", "mellow", "nimble", "olive", "proud", "quiet",
	"rapid", "rustic", "sage", "scarlet", "silver", "snowy", "solar", "spry",
	"stout", "swift", "teal", "tidy", "vivid", "warm", "witty", "zesty",
}

var slugNouns = []string{
	"otter", "lynx", "heron", "falcon", "badger", "marten", "raven", "sparrow",
	"finch", "wren", "fox", "hare", "ibex", "koi", "lark", "moth",
	"newt", "owl", "puffin", "quail", "robin", "seal", "stoat", "tern",
	"vole", "wolf", "yak", "adder", "bison", "crane", "drake", "egret",
	"gecko", "hawk", "jay", "kite", "mink", "perch", "shrew", "swift",
}

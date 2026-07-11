package plan

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RepairCorruptStamps performs a surgical, deterministic repair of the one
// PLAN.md corruption class that makes the whole file unparseable: a task header
// whose metadata comment carries an EMPTY-valued claim/gate token
// (`session=`, `heartbeat=`, or `not_before=`). This is the signature a pass
// killed mid-write leaves behind (e.g. OOM truncating the header stamp): the
// runner had begun writing `... session= heartbeat= ...` and never filled the
// values, and `time.Parse("")` on the empty heartbeat/not_before then rejects the
// entire document (`plan: bad heartbeat ""`), blocking selection, reconcile, and
// every view for that submodule.
//
// The repair is line-surgical and loss-honest:
//
//   - It DROPS an empty-valued `session=` / `heartbeat=` / `not_before=` token.
//     These are claim/gate metadata; a task with no session and no heartbeat is
//     exactly an unclaimed task in canonical form, which is the correct recovery
//     for a crashed pass (the dead claim is released so selection re-picks it). No
//     real value is invented and none is lost beyond a claim that was already
//     dead. Every other token on the line (id, status, attempts, deps, weight, a
//     VALID session/heartbeat/not_before) is preserved verbatim in its original
//     position.
//   - It REFUSES to touch a NON-empty malformed value, or a malformed structural
//     counter (`attempts=`, `weight=`). Guessing a scheduling counter or discarding
//     a partially-written real timestamp would be a data-losing shortcut, so those
//     lines are reported as needing manual attention and are left unchanged.
//
// It returns the repaired document, a per-line record of what changed (for a
// dry-run preview), and a list of residual problems it deliberately did not
// auto-fix. changed is empty and unfixable is nil when the document carries no
// empty-stamp corruption. The caller MUST re-validate the repaired text with
// Parse before trusting it: when unfixable is non-empty the document may still
// not parse, and RepairCorruptStamps never claims otherwise.
func RepairCorruptStamps(src string) (repaired string, changed []StampRepair, unfixable []string) {
	// Preserve the document's exact line structure (including a trailing newline)
	// by splitting on "\n" and rejoining; only individual header lines are ever
	// rewritten.
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		m := headerRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		id, meta := m[1], m[3]
		newMeta, dropped, residual := repairMeta(id, meta)
		for _, r := range residual {
			unfixable = append(unfixable, r)
		}
		if len(dropped) == 0 {
			continue
		}
		// Rebuild the header line with the surviving metadata, preserving the id,
		// status and every kept token exactly; only the dropped empty tokens go.
		rebuilt := fmt.Sprintf("## %s [%s] <!-- %s -->", id, m[2], newMeta)
		lines[i] = rebuilt
		changed = append(changed, StampRepair{
			Line:    i + 1,
			ID:      id,
			Before:  line,
			After:   rebuilt,
			Dropped: dropped,
		})
	}
	return strings.Join(lines, "\n"), changed, unfixable
}

// StampRepair records one header line the repair rewrote: its 1-based line
// number, the task id, the exact before/after text, and which empty tokens were
// dropped.
type StampRepair struct {
	Line    int
	ID      string
	Before  string
	After   string
	Dropped []string
}

// repairMeta filters one header's metadata-comment token list. It returns the
// rebuilt metadata string, the list of empty claim/gate tokens it dropped, and a
// list of residual problems (non-empty malformed values, malformed structural
// counters) it refused to auto-fix. The kept tokens retain their original order
// and text, so a valid line is returned unchanged and dropped == nil.
func repairMeta(id, meta string) (newMeta string, dropped []string, residual []string) {
	fields := strings.Fields(meta)
	kept := make([]string, 0, len(fields))
	for _, kv := range fields {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			// A bare token with no '=' is not something Parse reads; keep it
			// verbatim so the repair never silently discards unexpected content.
			kept = append(kept, kv)
			continue
		}
		switch k {
		case "session", "heartbeat", "not_before":
			if v == "" {
				// The empty-stamp corruption signature: drop it (canonical form
				// omits an empty session and a zero heartbeat/not_before).
				dropped = append(dropped, k)
				continue
			}
			// A non-empty session is always valid; a non-empty heartbeat/not_before
			// must be RFC3339 or it is real-but-corrupt data we must not discard.
			if k != "session" {
				if _, err := time.Parse(time.RFC3339, v); err != nil {
					residual = append(residual, fmt.Sprintf("%s: %s=%q is non-empty but not RFC3339 — left unchanged for manual repair (may carry real data)", id, k, v))
				}
			}
			kept = append(kept, kv)
		case "attempts", "weight":
			if _, err := strconv.Atoi(v); err != nil {
				residual = append(residual, fmt.Sprintf("%s: %s=%q is not an integer — left unchanged for manual repair (structural counter, not safe to guess)", id, k, v))
			}
			kept = append(kept, kv)
		default:
			// deps and any future/unknown token: Parse never fails on these, so
			// preserve verbatim.
			kept = append(kept, kv)
		}
	}
	return strings.Join(kept, " "), dropped, residual
}

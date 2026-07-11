package audit

import "sort"

// Window returns the sessions eligible for the next audit pass: epoch-sorted, the
// two most recent excluded (N-2 — they may still be in flight, under review, or
// in arbitration), then any already in the ledger dropped. The result is the
// "next un-audited batch"; as the ledger grows the batch shrinks, and as new
// sessions land the previously-excluded newest two become eligible. With two or
// fewer sessions the window is always empty.
func Window(sessions []Session, audited map[string]bool) []Session {
	s := append([]Session(nil), sessions...)
	sort.Slice(s, func(i, j int) bool {
		if s[i].Epoch != s[j].Epoch {
			return s[i].Epoch < s[j].Epoch
		}
		return s[i].ID < s[j].ID
	})
	if len(s) <= 2 {
		return nil
	}
	cands := s[:len(s)-2] // drop the two most recent
	var out []Session
	for _, x := range cands {
		if !audited[x.ID] {
			out = append(out, x)
		}
	}
	return out
}

// DefaultToolFailWindow is the number of most-recent sessions the tool-call-
// failure summary mines by default. It is deliberately a RECENT rolling window,
// not the whole corpus: mining every session ever recorded biases the ranking
// toward failure classes that have already been FIXED (their sessions dominate
// by sheer count and never age out), drowning the classes that are still costing
// agent-time now. A bounded recent window ages fixed classes out as newer
// sessions land, so the ranked output tracks the CURRENT failure surface.
const DefaultToolFailWindow = 50

// RecentSessions returns the n most-recent sessions by epoch (ties broken by ID
// for determinism), newest first. n <= 0 or n >= len returns all sessions (still
// newest-first). It is the recency gate for tool-call-failure mining — see
// DefaultToolFailWindow for why mining is scoped to a recent window rather than
// the full corpus.
func RecentSessions(sessions []Session, n int) []Session {
	s := append([]Session(nil), sessions...)
	sort.Slice(s, func(i, j int) bool {
		if s[i].Epoch != s[j].Epoch {
			return s[i].Epoch > s[j].Epoch // newest first
		}
		return s[i].ID > s[j].ID
	})
	if n > 0 && n < len(s) {
		s = s[:n]
	}
	return s
}

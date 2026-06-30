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

package web

// Regression gate for pageload-sessions-pagination: the sessions view must
// render a BOUNDED window of sessions rather than every session at once, so a
// paint's cost stays bounded as the session count grows without limit.

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// countSessionLinks counts rendered session rows by their per-session anchor.
func countSessionLinks(html, sm string) int {
	return strings.Count(html, "/submodule/"+sm+"/session/")
}

// TestSessionsListPaginationBounds asserts the sessions body renders at most one
// page of sessions even when the submodule holds far more, and exposes working
// prev/next navigation across the full set. It reuses seedPerfServer's session-
// heavy fixture (pageload_test.go).
func TestSessionsListPaginationBounds(t *testing.T) {
	const nSessions = 400
	t.Setenv("BEEHIVE_SESSIONS_PAGE_SIZE", "25")
	const pageSize = 25

	s, _ := seedPerfServer(t, nSessions, 20)
	h := s.Routes()

	get := func(path string) string {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, w.Code)
		}
		return w.Body.String()
	}

	// Page 1 must be bounded to the page size, NOT the full nSessions.
	body1 := get("/submodule/perf/sessions/body?page=1")
	got := countSessionLinks(body1, "perf")
	if got != pageSize {
		t.Fatalf("page 1 rendered %d session rows, want exactly the page size %d (bounded window)", got, pageSize)
	}
	if got >= nSessions {
		t.Fatalf("page 1 rendered %d rows — the list is unbounded (renders all %d sessions)", got, nSessions)
	}
	if !strings.Contains(body1, "page 1 of") {
		t.Fatalf("page 1 missing pagination navigation; body:\n%s", body1)
	}
	if !strings.Contains(body1, "sessions?page=2") {
		t.Fatalf("page 1 missing a next-page link; body:\n%s", body1)
	}

	// Every page is likewise bounded, and the union of all pages covers exactly
	// the full set with no duplicates — proving navigation reaches all sessions.
	seen := map[string]bool{}
	wantPages := (nSessions + pageSize - 1) / pageSize
	for p := 1; p <= wantPages; p++ {
		body := get("/submodule/perf/sessions/body?page=" + strconv.Itoa(p))
		n := countSessionLinks(body, "perf")
		if n > pageSize {
			t.Fatalf("page %d rendered %d rows, exceeds page size %d", p, n, pageSize)
		}
		for _, id := range sessionIDsIn(body) {
			if seen[id] {
				t.Fatalf("session %s appeared on more than one page", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != nSessions {
		t.Fatalf("pages covered %d distinct sessions across %d pages, want %d", len(seen), wantPages, nSessions)
	}
}

// sessionIDsIn extracts the session ids from rendered rows (the segment after
// /submodule/perf/session/ up to the closing quote).
func sessionIDsIn(html string) []string {
	const marker = `/submodule/perf/session/`
	var ids []string
	for {
		i := strings.Index(html, marker)
		if i < 0 {
			break
		}
		rest := html[i+len(marker):]
		j := strings.IndexAny(rest, `"`)
		if j < 0 {
			break
		}
		ids = append(ids, rest[:j])
		html = rest[j:]
	}
	return ids
}

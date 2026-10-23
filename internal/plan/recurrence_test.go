package plan

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEnableRecurrence(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	base := Task{
		ID: "standing", Title: "Daily check", Status: StatusTODO,
		Attempts: 3, Defers: MaxDefers, Deps: []string{"local", "other:task"}, Weight: 5,
		Session: "owner", Heartbeat: now, NotBefore: now.Add(time.Hour),
		Cycles: 4, LastCycle: now.Add(-24 * time.Hour), CycleSession: "previous",
		ReviewCommit: "abc", Commits: []string{"def"}, CommitsSet: true,
		HumanCategory: CatArchitecture, Body: []string{"description", "", "Check: go test ./..."},
	}
	for _, every := range []time.Duration{0, -24 * time.Hour, time.Hour, 48 * time.Hour} {
		tk := base
		if err := tk.EnableRecurrence(every); err == nil || !reflect.DeepEqual(tk, base) {
			t.Fatalf("invalid interval %s: err=%v task=%+v", every, err, tk)
		}
	}
	for _, status := range []Status{StatusReview, StatusArb, StatusDone, StatusHuman} {
		tk := base
		tk.Status = status
		before := tk
		if err := tk.EnableRecurrence(24 * time.Hour); err == nil || !reflect.DeepEqual(tk, before) {
			t.Fatalf("non-TODO %s: err=%v task=%+v", status, err, tk)
		}
	}
	for _, body := range [][]string{nil, {"Check:   "}, {"Verify-After-Merge: go test ./..."}} {
		tk := base
		tk.Body = body
		before := tk
		if err := tk.EnableRecurrence(24 * time.Hour); err == nil || !reflect.DeepEqual(tk, before) {
			t.Fatalf("missing Check: err=%v task=%+v", err, tk)
		}
	}
	tk := base
	tk.CheckNone = true
	if err := tk.EnableRecurrence(24 * time.Hour); err == nil {
		t.Fatal("check=none must not enable recurrence even with a Check body")
	}
	tk = base
	want := base
	want.Recurrence = 24 * time.Hour
	for i := 0; i < 2; i++ {
		if err := tk.EnableRecurrence(24 * time.Hour); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(tk, want) {
			t.Fatalf("enable changed unrelated fields: got %+v want %+v", tk, want)
		}
	}
}

func TestRecurrenceCyclesBeyondDeferCap(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 30, 0, 987654321, time.FixedZone("offset", 2*60*60))
	tk := &Task{ID: "standing", Status: StatusTODO, Defers: MaxDefers, Attempts: 2,
		Deps: []string{"dep"}, Weight: 3, Body: []string{"Check: go test ./..."},
		ReviewCommit: "abc", CommitsSet: true, Commits: []string{"def"}}
	if err := tk.EnableRecurrence(24 * time.Hour); err != nil {
		t.Fatal(err)
	}
	for cycle := 1; cycle <= 25; cycle++ {
		session := fmt.Sprintf("owner-%d", cycle)
		tk.Claim(session, now)
		want := *tk
		want.Cycles = cycle
		want.LastCycle = now.UTC().Truncate(time.Second)
		want.CycleSession = session
		want.NotBefore = want.LastCycle.Add(24 * time.Hour)
		want.Session = ""
		want.Heartbeat = time.Time{}
		if err := tk.CompleteCycle(session, now); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
		if !reflect.DeepEqual(*tk, want) {
			t.Fatalf("cycle %d: got %+v want %+v", cycle, tk, want)
		}
		text := (&Plan{Tasks: []*Task{tk}}).String()
		for _, token := range []string{"recurrence=24h", fmt.Sprintf("cycles=%d", cycle),
			"last_cycle=" + want.LastCycle.Format(time.RFC3339), "cycle_session=" + session} {
			if !strings.Contains(text, token) {
				t.Fatalf("missing %q in %s", token, text)
			}
		}
		parsed, err := Parse(text)
		if err != nil {
			t.Fatal(err)
		}
		tk = parsed.Tasks[0]
		if !reflect.DeepEqual(*tk, want) || parsed.String() != text {
			t.Fatalf("cycle %d roundtrip changed task: %+v", cycle, tk)
		}
		before := *tk
		if err := tk.CompleteCycle(session, now); err == nil || !reflect.DeepEqual(*tk, before) {
			t.Fatalf("duplicate completion: err=%v task=%+v", err, tk)
		}
		if err := tk.Defer(now.Add(48*time.Hour), now); err == nil || !reflect.DeepEqual(*tk, before) {
			t.Fatalf("recurrence bypassed defer cap: err=%v task=%+v", err, tk)
		}
		now = tk.NotBefore
	}
}

func TestCompleteCycleRefusals(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	base := Task{ID: "standing", Status: StatusTODO, Recurrence: 24 * time.Hour,
		Session: "owner", Heartbeat: now, Body: []string{"Check: go test ./..."}}
	for _, tc := range []struct {
		name    string
		edit    func(*Task)
		session string
		now     time.Time
	}{
		{name: "disabled", edit: func(t *Task) { t.Recurrence = 0 }},
		{name: "unsupported", edit: func(t *Task) { t.Recurrence = time.Hour }},
		{name: "review", edit: func(t *Task) { t.Status = StatusReview }},
		{name: "arbitration", edit: func(t *Task) { t.Status = StatusArb }},
		{name: "done", edit: func(t *Task) { t.Status = StatusDone }},
		{name: "human", edit: func(t *Task) { t.Status = StatusHuman }},
		{name: "missing check", edit: func(t *Task) { t.Body = nil }},
		{name: "blank check", edit: func(t *Task) { t.Body = []string{"Check:  "} }},
		{name: "check none", edit: func(t *Task) { t.CheckNone = true }},
		{name: "unclaimed", edit: func(t *Task) { t.Release() }},
		{name: "empty owner", session: ""},
		{name: "wrong owner", session: "other"},
		{name: "invalid token", session: "two words", edit: func(t *Task) { t.Session = "two words" }},
		{name: "duplicate owner", edit: func(t *Task) {
			t.Cycles, t.CycleSession, t.LastCycle = 1, "owner", now.Add(-48*time.Hour)
		}},
		{name: "not due", edit: func(t *Task) { t.NotBefore = now.Add(time.Nanosecond) }},
		{name: "same time", edit: func(t *Task) { t.LastCycle = now }},
		{name: "earlier time", edit: func(t *Task) { t.LastCycle = now.Add(time.Second) }},
		{name: "same persisted second", edit: func(t *Task) { t.LastCycle = now }, now: now.Add(time.Nanosecond)},
		{name: "zero time"},
		{name: "negative year", now: time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)},
		{name: "large year", now: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
		{name: "next gate overflow", now: time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)},
		{name: "counter overflow", edit: func(t *Task) { t.Cycles = int(^uint(0) >> 1) }},
		{name: "negative counter", edit: func(t *Task) { t.Cycles = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tk := base
			if tc.edit != nil {
				tc.edit(&tk)
			}
			if tc.session == "" && tc.name != "empty owner" {
				tc.session = "owner"
			}
			if tc.now.IsZero() && tc.name != "zero time" {
				tc.now = now
			}
			before := tk
			if err := tk.CompleteCycle(tc.session, tc.now); err == nil || !reflect.DeepEqual(tk, before) {
				t.Fatalf("refusal must not mutate: err=%v task=%+v", err, tk)
			}
		})
	}
}

func TestRecurrenceCandidates(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	tk := &Task{ID: "standing", Status: StatusTODO, Recurrence: 24 * time.Hour,
		Session: "owner", Body: []string{"Check: go test ./..."}, Deps: []string{"dep"}}
	dep := &Task{ID: "dep", Status: StatusDone}
	p := &Plan{Tasks: []*Task{tk, dep}}
	if err := tk.CompleteCycle("owner", now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		now  time.Time
		dep  Status
		want bool
	}{
		{"future gate", now, StatusDone, false},
		{"just before gate", tk.NotBefore.Add(-time.Nanosecond), StatusDone, false},
		{"gate reached", tk.NotBefore, StatusDone, true},
		{"past gate", tk.NotBefore.Add(time.Hour), StatusDone, true},
		{"unmet local dependency", tk.NotBefore.Add(time.Hour), StatusHuman, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dep.Status = tc.dep
			got := p.Candidates(tc.now, time.Minute)
			if (len(got) == 1 && got[0].ID == tk.ID) != tc.want {
				t.Fatalf("Candidates=%+v, want standing=%v", got, tc.want)
			}
		})
	}
}

func TestRecurrenceParseValidation(t *testing.T) {
	history := "cycles=1 last_cycle=2026-10-22T12:00:00Z cycle_session=owner"
	for _, meta := range []string{
		"recurrence=", "recurrence=daily", "recurrence=0", "recurrence=-24h", "recurrence=1h",
		"recurrence=999999999999999999999h", "cycles=", "cycles=-1", "cycles=x", "cycles=1.5",
		"cycles=999999999999999999999", "last_cycle=", "last_cycle=yesterday",
		"last_cycle=2026-02-30T12:00:00Z", "last_cycle=2026-10-22", "last_cycle=0001-01-01T00:00:00Z",
		"last_cycle=10000-01-01T00:00:00Z", "last_cycle=9999-12-31T23:00:00-02:00",
		"cycle_session=", "cycle_session=<bad>", history,
		"recurrence=24h cycles=1", "recurrence=24h cycle_session=owner",
		"recurrence=24h last_cycle=2026-10-22T12:00:00Z",
		"recurrence=24h cycles=1 last_cycle=2026-10-22T12:00:00Z",
		"recurrence=24h cycles=1 cycle_session=owner",
		"recurrence=24h cycles=0 last_cycle=2026-10-22T12:00:00Z cycle_session=owner",
	} {
		t.Run(meta, func(t *testing.T) {
			if _, err := Parse("## standing [TODO] <!-- " + meta + " -->\nCheck: go test ./...\n"); err == nil {
				t.Fatalf("accepted malformed metadata %q", meta)
			}
		})
	}
	for _, body := range []string{"", "Check:   ", "Verify-After-Merge: go test ./..."} {
		if _, err := Parse("## standing [TODO] <!-- recurrence=24h -->\n" + body); err == nil {
			t.Fatalf("recurrence without actual Check accepted: %q", body)
		}
	}
	if _, err := Parse("## standing [TODO] <!-- recurrence=24h check=none -->\njustification"); err == nil {
		t.Fatal("recurrence with check=none accepted")
	}
	for _, meta := range []string{"", "cycles=0", "recurrence=24h", "recurrence=1440m", "recurrence=24h " + history} {
		p, err := Parse("## standing [TODO] <!-- " + meta + " -->\nCheck: go test ./...\n")
		if err != nil {
			t.Fatalf("valid metadata %q: %v", meta, err)
		}
		if _, err := Parse(p.String()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecurrenceHistorySurvivesDeferAndTransition(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	tk := &Task{ID: "standing", Status: StatusTODO, Recurrence: 24 * time.Hour,
		Session: "owner", Defers: MaxDefers - 1, Body: []string{"Check: go test ./..."}}
	if err := tk.CompleteCycle("owner", now); err != nil {
		t.Fatal(err)
	}
	until := now.Add(48 * time.Hour)
	if err := tk.Defer(until, now); err != nil {
		t.Fatal(err)
	}
	for _, status := range []Status{StatusTODO, StatusReview, StatusDone} {
		if status != tk.Status {
			if err := tk.Transition(status, now); err != nil {
				t.Fatal(err)
			}
		}
		p, err := Parse((&Plan{Tasks: []*Task{tk}}).String())
		if err != nil {
			t.Fatal(err)
		}
		got := p.Tasks[0]
		if got.Cycles != 1 || got.CycleSession != "owner" || !got.LastCycle.Equal(now) ||
			!got.NotBefore.Equal(until) || got.Defers != MaxDefers || got.Status != status {
			t.Fatalf("history changed: %+v", got)
		}
	}
}

func TestDeferCapGuard(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	for _, defers := range []int{MaxDefers - 1, MaxDefers, MaxDefers + 1} {
		tk := Task{ID: "ordinary", Status: StatusTODO, Defers: defers, Session: "owner", Heartbeat: now}
		before := tk
		err := tk.Defer(now.Add(time.Hour), now)
		if defers < MaxDefers {
			before.Defers++
			before.NotBefore = now.Add(time.Hour)
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil {
			t.Fatalf("defers=%d bypassed cap", defers)
		}
		if !reflect.DeepEqual(tk, before) {
			t.Fatalf("defers=%d: unexpected mutation %+v", defers, tk)
		}
	}
}

package plan

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSetCheckNoneRejectsRecurrenceWithoutMutation(t *testing.T) {
	for _, status := range []Status{StatusTODO, StatusReview, StatusArb, StatusDone, StatusHuman} {
		t.Run(string(status), func(t *testing.T) {
			now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
			task := Task{
				ID: "standing", Status: status, Recurrence: 24 * time.Hour,
				Attempts: 3, Defers: MaxDefers, Weight: 5, Deps: []string{"setup", "other:ready"},
				Session: "owner", Heartbeat: now, NotBefore: now.Add(time.Hour),
				Cycles: 4, LastCycle: now.Add(-24 * time.Hour), CycleSession: "previous",
				ReviewCommit: "abc", Commits: []string{"def"}, CommitsSet: true,
				Body: []string{"Observe daily.", "Check: go test ./...", "Feedback: retain this history."},
			}
			before := task
			before.Body = append([]string(nil), task.Body...)
			before.Deps = append([]string(nil), task.Deps...)
			before.Commits = append([]string(nil), task.Commits...)
			if err := task.SetCheckNone(); err == nil || !strings.Contains(err.Error(), "requires a Check") {
				t.Fatalf("want recurring check refusal, got %v", err)
			}
			if !reflect.DeepEqual(task, before) {
				t.Fatalf("refusal mutated task:\nbefore: %+v\nafter: %+v", before, task)
			}
			if _, err := Parse((&Plan{Tasks: []*Task{&task}}).String()); err != nil {
				t.Fatalf("refusal left an invalid plan: %v", err)
			}
		})
	}
}

func TestSetCheckNoneOrdinaryTask(t *testing.T) {
	task := Task{ID: "ordinary", Status: StatusTODO, Body: []string{
		"No machine-checkable effect.", "Feedback: retain this history.", "Check: go test ./...",
	}}
	want := task
	want.CheckNone = true
	want.Body = []string{"No machine-checkable effect.", "Feedback: retain this history."}
	for i := 0; i < 2; i++ {
		if err := task.SetCheckNone(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(task, want) {
			t.Fatalf("unexpected mutation: got %+v want %+v", task, want)
		}
	}
	if _, err := Parse((&Plan{Tasks: []*Task{&task}}).String()); err != nil {
		t.Fatal(err)
	}
}

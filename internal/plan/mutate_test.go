package plan

import (
	"reflect"
	"testing"
)

func TestNewTaskValidatesAndDefaults(t *testing.T) {
	tk, err := NewTask("build-image", []string{"flux:base-job", "local-dep", ""}, 0, []string{"line one", ""})
	if err != nil {
		t.Fatalf("NewTask: %v", err)
	}
	if tk.Status != StatusTODO || tk.Attempts != 0 {
		t.Fatalf("new task must be a fresh TODO: %+v", tk)
	}
	if tk.Weight != 1 {
		t.Fatalf("weight<1 must default to 1, got %d", tk.Weight)
	}
	if !reflect.DeepEqual(tk.Deps, []string{"flux:base-job", "local-dep"}) {
		t.Fatalf("blank deps must be dropped: %+v", tk.Deps)
	}
	if !reflect.DeepEqual(tk.Body, []string{"line one"}) {
		t.Fatalf("trailing blank body line must be trimmed: %+v", tk.Body)
	}
	if _, err := NewTask("bad id", nil, 1, nil); err == nil {
		t.Fatal("an id with whitespace must be rejected")
	}
	if _, err := NewTask("ok", []string{"a:b:c"}, 1, nil); err == nil {
		t.Fatal("a malformed dep must be rejected")
	}
}

func TestAddTaskRejectsDuplicate(t *testing.T) {
	p, _ := Parse("## a [TODO] <!-- attempts=0 deps= -->\n")
	tk, _ := NewTask("b", nil, 1, []string{"body"})
	if err := p.AddTask(tk); err != nil {
		t.Fatalf("AddTask new: %v", err)
	}
	if p.Find("b") == nil {
		t.Fatal("task b must be present after AddTask")
	}
	dup, _ := NewTask("a", nil, 1, nil)
	if err := p.AddTask(dup); err == nil {
		t.Fatal("AddTask must reject a duplicate id")
	}
	// Round-trips.
	p2, err := Parse(p.String())
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if len(p2.Tasks) != 2 {
		t.Fatalf("expected 2 tasks after add, got %d", len(p2.Tasks))
	}
}

func TestAddDepIdempotent(t *testing.T) {
	p, _ := Parse("## a [TODO] <!-- attempts=0 deps=x -->\n")
	tk := p.Find("a")
	added, err := tk.AddDep("flux:base-job")
	if err != nil || !added {
		t.Fatalf("first AddDep should add: added=%v err=%v", added, err)
	}
	added, _ = tk.AddDep("flux:base-job")
	if added {
		t.Fatal("second AddDep of the same dep must be a no-op")
	}
	if !reflect.DeepEqual(tk.Deps, []string{"x", "flux:base-job"}) {
		t.Fatalf("deps: %+v", tk.Deps)
	}
	if _, err := tk.AddDep("no spaces allowed"); err == nil {
		t.Fatal("malformed dep must error")
	}
}

func TestBlockedLocalDeps(t *testing.T) {
	p, _ := Parse(
		"## a [TODO] <!-- attempts=0 deps=b,flux:remote -->\n" +
			"## b [TODO] <!-- attempts=0 deps= -->\n")
	a := p.Find("a")
	if !p.Blocked(a) {
		t.Fatal("a must be blocked while local dep b is not DONE")
	}
	// Cross-submodule dep is NOT judged locally.
	p.Find("b").Status = StatusDone
	if p.Blocked(a) {
		t.Fatal("a's only remaining dep is cross-submodule; Blocked must ignore it")
	}
	// A dep on a missing local task is blocked.
	p2, _ := Parse("## a [TODO] <!-- attempts=0 deps=ghost -->\n")
	if !p2.Blocked(p2.Find("a")) {
		t.Fatal("a dep on a non-existent local task must read as blocked")
	}
}

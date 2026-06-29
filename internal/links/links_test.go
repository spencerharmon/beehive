package links

import (
	"path/filepath"
	"testing"
)

func TestCycleDetection(t *testing.T) {
	l := &Links{}
	if err := l.AddDep("a", "b"); err != nil {
		t.Fatal(err)
	}
	if err := l.AddDep("b", "c"); err != nil {
		t.Fatal(err)
	}
	if err := l.AddDep("c", "a"); err == nil {
		t.Fatal("cycle c->a not rejected")
	}
	if len(l.Deps) != 2 {
		t.Fatalf("cyclic edge retained: %v", l.Deps)
	}
	if l.HasCycle() {
		t.Fatal("graph reported cyclic after rejection")
	}
	if err := l.AddDep("a", "a"); err == nil {
		t.Fatal("self-dep allowed")
	}
}

func TestLinkSubmodulesIdempotent(t *testing.T) {
	l := &Links{}
	l.LinkSubmodules("x", "y")
	l.LinkSubmodules("x", "y")
	if len(l.Submodules) != 2 {
		t.Fatalf("dupes: %v", l.Submodules)
	}
}

func TestRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), LinksName())
	l := &Links{}
	l.LinkSubmodules("b", "a")
	l.AddDep("a", "b")
	if err := l.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Submodules) != 2 || got.Submodules[0] != "a" {
		t.Fatalf("submodules=%v", got.Submodules)
	}
	if len(got.Deps) != 1 || got.Deps[0].From != "a" || got.Deps[0].To != "b" {
		t.Fatalf("deps=%v", got.Deps)
	}
}

func LinksName() string { return "SUBMODULE-LINKS.yaml" }

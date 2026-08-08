package guards

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const sampleGuards = `# Guards — flux
Diff-scoped mutation guards for the flux target.

## bluegreen-active-color-frozen
Refuse a honeybee change to the ACTIVE blue/green color's HelmRelease.
Protects: infrastructure/phantom-library-helm/helmrelease-*.yaml
Command: guards/active-color-frozen.sh

## guard-the-guards
Any change to guard code must go through operator review.
Protects: GUARDS.md, guards/**
Command: guards/require-review.sh
`

func TestParseRoundTrip(t *testing.T) {
	g, err := Parse(sampleGuards)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(g.Stubs) != 2 {
		t.Fatalf("want 2 stubs, got %d", len(g.Stubs))
	}
	if g.Stubs[0].ID != "bluegreen-active-color-frozen" {
		t.Fatalf("stub0 id = %q", g.Stubs[0].ID)
	}
	if g.Stubs[0].Command != "guards/active-color-frozen.sh" {
		t.Fatalf("stub0 command = %q", g.Stubs[0].Command)
	}
	// Round-trip is stable.
	g2, err := Parse(g.String())
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if len(g2.Stubs) != 2 || g2.Stubs[1].ID != "guard-the-guards" {
		t.Fatalf("round-trip drift: %+v", g2.Stubs)
	}
}

func TestTriggeredIntersectsDiff(t *testing.T) {
	g, err := Parse(sampleGuards)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		changed []string
		wantIDs []string
	}{
		{"active manifest touched", []string{"infrastructure/phantom-library-helm/helmrelease-green.yaml"}, []string{"bluegreen-active-color-frozen"}},
		{"idle manifest touched", []string{"infrastructure/phantom-library-helm/helmrelease-blue.yaml"}, []string{"bluegreen-active-color-frozen"}},
		{"unrelated file", []string{"README.md"}, nil},
		{"guard code touched", []string{"guards/active-color-frozen.sh"}, []string{"guard-the-guards"}},
		{"GUARDS.md touched", []string{"GUARDS.md"}, []string{"guard-the-guards"}},
		{"both", []string{"infrastructure/phantom-library-helm/helmrelease-green.yaml", "guards/x.sh"}, []string{"bluegreen-active-color-frozen", "guard-the-guards"}},
		{"nested non-helmrelease not matched", []string{"infrastructure/phantom-library-helm/kustomization.yaml"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := g.Triggered(c.changed)
			var ids []string
			for _, s := range got {
				ids = append(ids, s.ID)
			}
			if strings.Join(ids, ",") != strings.Join(c.wantIDs, ",") {
				t.Fatalf("triggered = %v, want %v", ids, c.wantIDs)
			}
		})
	}
}

func TestGlobSegmentSemantics(t *testing.T) {
	// * stays within a segment; ** spans them.
	star, _ := globToRegexp("a/*.yaml")
	if star.MatchString("a/b/c.yaml") {
		t.Fatal("single * must not cross a path separator")
	}
	if !star.MatchString("a/c.yaml") {
		t.Fatal("single * must match within a segment")
	}
	dstar, _ := globToRegexp("a/**")
	if !dstar.MatchString("a/b/c.yaml") {
		t.Fatal("** must span segments")
	}
	lit, _ := globToRegexp("GUARDS.md")
	if lit.MatchString("xGUARDSxmd") {
		t.Fatal("literal dots must be escaped")
	}
	if !lit.MatchString("GUARDS.md") {
		t.Fatal("literal must match itself")
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"no protects":   "## x\nCommand: guards/x.sh\n",
		"no command":    "## x\nProtects: a/*.yaml\n",
		"trivial true":  "## x\nProtects: a/*\nCommand: true\n",
		"trivial echo":  "## x\nProtects: a/*\nCommand: echo ok\n",
		"trivial exit0": "## x\nProtects: a/*\nCommand: exit 0\n",
		"trivial test":  "## x\nProtects: a/*\nCommand: test -f a\n",
		"duplicate id":  "## x\nProtects: a/*\nCommand: guards/x.sh\n## x\nProtects: b/*\nCommand: guards/y.sh\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(doc); err == nil {
				t.Fatalf("expected parse error for %q", name)
			}
		})
	}
}

func TestParseAcceptsRealScript(t *testing.T) {
	g, err := Parse("## x\nProtects: a/*.yaml\nCommand: guards/policy.sh --strict\n")
	if err != nil {
		t.Fatalf("real script command should parse: %v", err)
	}
	if g.Stubs[0].Command != "guards/policy.sh --strict" {
		t.Fatalf("command = %q", g.Stubs[0].Command)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "GUARDS.md"))
	if !errors.Is(err, ErrNoGuardsFile) {
		t.Fatalf("want ErrNoGuardsFile, got %v", err)
	}
}

func TestMatchedPaths(t *testing.T) {
	g, _ := Parse(sampleGuards)
	st := g.Stubs[0]
	got := st.MatchedPaths([]string{
		"infrastructure/phantom-library-helm/helmrelease-green.yaml",
		"README.md",
		"infrastructure/phantom-library-helm/helmrelease-blue.yaml",
	})
	if strings.Join(got, ",") != "infrastructure/phantom-library-helm/helmrelease-green.yaml,infrastructure/phantom-library-helm/helmrelease-blue.yaml" {
		t.Fatalf("matched = %v", got)
	}
}

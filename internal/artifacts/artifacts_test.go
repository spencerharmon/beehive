package artifacts

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// readTestdata returns a testdata file's bytes as a string.
func readTestdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata %s: %v", name, err)
	}
	return string(b)
}

// TestInfraRoundTrip proves a representative INFRASTRUCTURE.md (with markers)
// parses to the right blue/green state AND serializes back byte-for-byte.
func TestInfraRoundTrip(t *testing.T) {
	src := readTestdata(t, "INFRASTRUCTURE.md")
	in := ParseInfra(src)

	if got := in.String(); got != src {
		t.Fatalf("infra round-trip mismatch:\n--- got ---\n%q\n--- want ---\n%q", got, src)
	}
	if !in.Present() {
		t.Fatal("parsed infra should be Present()")
	}
	if in.Active != "green" {
		t.Fatalf("Active = %q, want green", in.Active)
	}
	wantEnvs := []string{"blue", "green", "canary"}
	if !reflect.DeepEqual(in.Envs, wantEnvs) {
		t.Fatalf("Envs = %v, want %v", in.Envs, wantEnvs)
	}
	d := in.Deployment()
	if d.Active != "green" || !reflect.DeepEqual(d.Envs, wantEnvs) {
		t.Fatalf("Deployment = %+v, want {green %v}", d, wantEnvs)
	}
}

// TestInfraNoMarkersRoundTripDefaults proves a prose-only INFRASTRUCTURE.md
// (no markers) still round-trips and resolves to the historical defaults.
func TestInfraNoMarkersRoundTripDefaults(t *testing.T) {
	src := readTestdata(t, "INFRASTRUCTURE_nomarkers.md")
	in := ParseInfra(src)

	if got := in.String(); got != src {
		t.Fatalf("no-markers round-trip mismatch:\n%q\nvs\n%q", got, src)
	}
	if in.Active != "" || in.Envs != nil {
		t.Fatalf("expected no raw markers, got Active=%q Envs=%v", in.Active, in.Envs)
	}
	d := in.Deployment()
	if d.Active != DefaultActive {
		t.Fatalf("default Active = %q, want %q", d.Active, DefaultActive)
	}
	if !reflect.DeepEqual(d.Envs, []string{"blue", "green"}) {
		t.Fatalf("default Envs = %v, want [blue green]", d.Envs)
	}
}

// TestInfraEnvExtractionMatchesLegacy locks that marker extraction reproduces the
// exact semantics the old env.go regexes had: first Active match wins, the
// Environments list is comma-split with surrounding spaces trimmed and empties
// dropped, and absent markers fall back to the defaults.
func TestInfraEnvExtractionMatchesLegacy(t *testing.T) {
	cases := []struct {
		name       string
		src        string
		wantActive string
		wantEnvs   []string
	}{
		{"both markers", "Active: blue\nEnvironments: blue, green\n", "blue", []string{"blue", "green"}},
		{"messy csv trimmed", "Environments:  a , , b ,c \nActive: c\n", "c", []string{"a", "b", "c"}},
		{"first active wins", "Active: blue\nActive: green\n", "blue", []string{"blue", "green"}},
		{"markers mid-prose", "# Topo\n\nsome text\nActive: canary\nmore text\n", "canary", []string{"blue", "green"}},
		{"empty file", "", "blue", []string{"blue", "green"}},
		{"no markers", "# Just prose\n\nnothing structured here.\n", "blue", []string{"blue", "green"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := ParseInfra(c.src).Deployment()
			if d.Active != c.wantActive {
				t.Fatalf("Active = %q, want %q", d.Active, c.wantActive)
			}
			if !reflect.DeepEqual(d.Envs, c.wantEnvs) {
				t.Fatalf("Envs = %v, want %v", d.Envs, c.wantEnvs)
			}
		})
	}
}

// TestInfraSetActive covers the deploy mutation: rewrite the marker in place when
// present, append it when absent, and round-trip cleanly afterward.
func TestInfraSetActive(t *testing.T) {
	// In place: an existing Active line is rewritten, the rest preserved.
	in := ParseInfra("# T\n\nActive: blue\nEnvironments: blue, green\n")
	in.SetActive("green")
	want := "# T\n\nActive: green\nEnvironments: blue, green\n"
	if got := in.String(); got != want {
		t.Fatalf("set-active in place:\n got %q\nwant %q", got, want)
	}
	if in.Active != "green" {
		t.Fatalf("Active field = %q, want green", in.Active)
	}

	// Absent: the marker is appended.
	in2 := ParseInfra("# T\n\nno markers yet\n")
	in2.SetActive("blue")
	if got := in2.String(); got != "# T\n\nno markers yet\nActive: blue\n" {
		t.Fatalf("set-active append: %q", got)
	}

	// From a zero/empty model (deploy into a fresh file).
	var in3 Infra
	in3.SetActive("green")
	if got := in3.String(); got != "Active: green\n" {
		t.Fatalf("set-active empty: %q", got)
	}
	if !in3.Present() {
		t.Fatal("SetActive must mark the model present")
	}
}

// TestLoadInfra covers the file loaders: a present file parses, a missing file is
// a non-error absent model resolving to defaults, and a directory (unreadable)
// surfaces the error.
func TestLoadInfra(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "INFRASTRUCTURE.md")
	if err := os.WriteFile(p, []byte("Active: green\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, err := LoadInfra(p)
	if err != nil || !in.Present() || in.Active != "green" {
		t.Fatalf("load present: in=%+v err=%v", in, err)
	}

	missing, err := LoadInfra(filepath.Join(dir, "absent.md"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if missing.Present() {
		t.Fatal("missing file should be !Present()")
	}
	if d := missing.Deployment(); d.Active != DefaultActive {
		t.Fatalf("missing Deployment Active = %q, want default", d.Active)
	}

	if _, err := LoadInfra(dir); err == nil {
		t.Fatal("reading a directory should surface an error")
	}
}

// TestArtifactsRoundTrip proves a representative ARTIFACTS.md round-trips and that
// its top-level bullets parse into structured Items (name/desc split, no-colon
// name, indented sub-bullet excluded).
func TestArtifactsRoundTrip(t *testing.T) {
	src := readTestdata(t, "ARTIFACTS.md")
	a := ParseArtifacts(src)

	if got := a.String(); got != src {
		t.Fatalf("artifacts round-trip mismatch:\n--- got ---\n%q\n--- want ---\n%q", got, src)
	}
	want := []Artifact{
		{Name: "beehive", Desc: "the swarm coordination CLI (static, CGO-free)"},
		{Name: "beehived", Desc: "the frontend daemon serving the web UI"},
		{Name: "honeybee", Desc: "the stateless worker binary"},
		{Name: "container image", Desc: "ghcr.io/example/alpha published on release"},
		{Name: "release notes", Desc: ""},
	}
	if !reflect.DeepEqual(a.Items, want) {
		t.Fatalf("Items =\n%#v\nwant\n%#v", a.Items, want)
	}
}

// TestLoadArtifactsMissing proves a missing ARTIFACTS.md is a non-error empty model.
func TestLoadArtifactsMissing(t *testing.T) {
	a, err := LoadArtifacts(filepath.Join(t.TempDir(), "ARTIFACTS.md"))
	if err != nil {
		t.Fatalf("missing artifacts should not error: %v", err)
	}
	if a.Present() || len(a.Items) != 0 || a.String() != "" {
		t.Fatalf("missing artifacts should be empty/absent: %+v", a)
	}
}

// TestArtifactsEmptyAndProse proves an artifacts doc with no bullets yields zero
// Items but still round-trips, and that a missing trailing newline is normalized
// to the canonical newline-terminated form.
func TestArtifactsEmptyAndProse(t *testing.T) {
	a := ParseArtifacts("# ARTIFACTS\n\nNo artifacts yet.\n")
	if len(a.Items) != 0 {
		t.Fatalf("prose-only doc should have no items, got %v", a.Items)
	}
	if a.String() != "# ARTIFACTS\n\nNo artifacts yet.\n" {
		t.Fatalf("prose round-trip: %q", a.String())
	}

	// No trailing newline -> canonical form adds one (matches the deploy writer).
	if got := ParseArtifacts("- only: one").String(); got != "- only: one\n" {
		t.Fatalf("trailing-newline normalization: %q", got)
	}
}

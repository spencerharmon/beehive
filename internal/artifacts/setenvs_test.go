package artifacts

import "testing"

// TestInfraSetEnvs mirrors TestInfraSetActive for the environment-list marker:
// an existing Environments line is rewritten in place, an absent one is
// appended, and a zero model synthesizes the marker and becomes present. The
// serialized form is the historical comma-space list.
func TestInfraSetEnvs(t *testing.T) {
	// In place: an existing Environments line is rewritten, the rest preserved.
	in := ParseInfra("# T\n\nActive: blue\nEnvironments: blue, green\n")
	in.SetEnvs([]string{"blue", "green", "canary"})
	want := "# T\n\nActive: blue\nEnvironments: blue, green, canary\n"
	if got := in.String(); got != want {
		t.Fatalf("set-envs in place:\n got %q\nwant %q", got, want)
	}
	if len(in.Envs) != 3 || in.Envs[2] != "canary" {
		t.Fatalf("Envs field = %v, want [blue green canary]", in.Envs)
	}

	// Absent: the marker is appended.
	in2 := ParseInfra("# T\n\nno markers yet\n")
	in2.SetEnvs([]string{"blue", "green"})
	if got := in2.String(); got != "# T\n\nno markers yet\nEnvironments: blue, green\n" {
		t.Fatalf("set-envs append: %q", got)
	}

	// From a zero/empty model (normalize a fresh file).
	var in3 Infra
	in3.SetEnvs([]string{"blue", "green"})
	if got := in3.String(); got != "Environments: blue, green\n" {
		t.Fatalf("set-envs empty: %q", got)
	}
	if !in3.Present() {
		t.Fatal("SetEnvs must mark the model present")
	}

	// The stored slice is a copy: mutating the caller's slice must not alias it.
	src := []string{"blue", "green"}
	in4 := ParseInfra("Active: blue\n")
	in4.SetEnvs(src)
	src[0] = "mutated"
	if in4.Envs[0] != "blue" {
		t.Fatalf("SetEnvs must copy its argument, got %v", in4.Envs)
	}
}

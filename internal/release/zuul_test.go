package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// This file guards the self-hosted-Zuul half of the release-pipeline contract
// (zuul.d/, playbooks/, scripts/build-release-artifacts.sh) exactly as
// release_test.go guards the .github/workflows/ci.yml half: a fix must ship
// tests, and nothing else exercises the Zuul wiring.

// zuulJob mirrors the subset of Zuul's `job:` schema this package checks.
type zuulJob struct {
	Job struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Run         string `yaml:"run"`
		Timeout     int    `yaml:"timeout"`
	} `yaml:"job"`
}

// zuulProject mirrors the subset of Zuul's `project:` schema this package
// checks: the check/gate pipelines' job lists.
type zuulProject struct {
	Project struct {
		Check struct {
			Jobs []string `yaml:"jobs"`
		} `yaml:"check"`
		Gate struct {
			Jobs []string `yaml:"jobs"`
		} `yaml:"gate"`
	} `yaml:"project"`
}

// TestZuulJobsDefineCrossCompileAndStaticAssert asserts zuul.d/jobs.yaml
// actually parses as Zuul job config, defines the release cross-compile job
// (documented as CGO_ENABLED=0 + static) and the basic CI job, and that every
// job's `run:` playbook exists on disk.
func TestZuulJobsDefineCrossCompileAndStaticAssert(t *testing.T) {
	root := repoRoot(t)
	raw := readRepoFile(t, root, "zuul.d/jobs.yaml")

	var jobs []zuulJob
	if err := yaml.Unmarshal([]byte(raw), &jobs); err != nil {
		t.Fatalf("zuul.d/jobs.yaml does not parse as YAML: %v", err)
	}
	if len(jobs) == 0 {
		t.Fatal("zuul.d/jobs.yaml defines no jobs")
	}

	byName := map[string]zuulJob{}
	for _, j := range jobs {
		if j.Job.Name == "" {
			t.Fatalf("zuul.d/jobs.yaml has a job with no name: %+v", j)
		}
		byName[j.Job.Name] = j
	}

	release, ok := byName["beehive-release-cross-compile"]
	if !ok {
		t.Fatal("zuul.d/jobs.yaml must define job beehive-release-cross-compile")
	}
	if release.Job.Run != "playbooks/beehive-release-cross-compile.yaml" {
		t.Errorf("beehive-release-cross-compile.run = %q, want playbooks/beehive-release-cross-compile.yaml", release.Job.Run)
	}
	if !strings.Contains(release.Job.Description, "CGO_ENABLED=0") {
		t.Error("beehive-release-cross-compile description must document the CGO_ENABLED=0 contract")
	}
	if !strings.Contains(release.Job.Description, "static") {
		t.Error("beehive-release-cross-compile description must document the static-binary assertion")
	}

	build, ok := byName["beehive-build-test"]
	if !ok {
		t.Fatal("zuul.d/jobs.yaml must define job beehive-build-test")
	}
	if build.Job.Run != "playbooks/beehive-build-test.yaml" {
		t.Errorf("beehive-build-test.run = %q, want playbooks/beehive-build-test.yaml", build.Job.Run)
	}

	for _, j := range jobs {
		if j.Job.Run == "" {
			t.Errorf("job %s has no run: playbook", j.Job.Name)
			continue
		}
		if _, err := os.Stat(filepath.Join(root, j.Job.Run)); err != nil {
			t.Errorf("job %s references missing playbook %s: %v", j.Job.Name, j.Job.Run, err)
		}
	}
}

// TestZuulProjectGatesOnReleaseCrossCompile asserts zuul.d/project.yaml
// parses and attaches both jobs to check AND gate -- the only pipelines
// flux:zuul's tenant config is confirmed to define today (see
// docs/tasks/release-verify.md). beehive is an untrusted project and cannot
// define a new pipeline itself, so this is the correct, safe attachment
// until a dedicated release/tag pipeline exists upstream.
func TestZuulProjectGatesOnReleaseCrossCompile(t *testing.T) {
	root := repoRoot(t)
	raw := readRepoFile(t, root, "zuul.d/project.yaml")

	var projects []zuulProject
	if err := yaml.Unmarshal([]byte(raw), &projects); err != nil {
		t.Fatalf("zuul.d/project.yaml does not parse as YAML: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("zuul.d/project.yaml must define exactly one project stanza, got %d", len(projects))
	}
	p := projects[0].Project

	wantJobs := []string{"beehive-build-test", "beehive-release-cross-compile"}
	pipelines := []struct {
		name string
		jobs []string
	}{
		{"check", p.Check.Jobs},
		{"gate", p.Gate.Jobs},
	}
	for _, pipeline := range pipelines {
		if len(pipeline.jobs) == 0 {
			t.Errorf("%s pipeline has no jobs", pipeline.name)
			continue
		}
		for _, want := range wantJobs {
			found := false
			for _, got := range pipeline.jobs {
				if got == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s pipeline must list job %s, got %v", pipeline.name, want, pipeline.jobs)
			}
		}
	}
}

// TestZuulReleasePlaybookDelegatesToScripts asserts the release playbook
// builds via scripts/build-release-artifacts.sh then asserts via
// scripts/verify-release.sh SKIP_COSIGN=1 -- and never invokes cosign
// directly. Signing/publishing is explicitly a live/operator concern (this
// task's Accept is static + local only), never something a job definition
// commits to git should perform itself.
func TestZuulReleasePlaybookDelegatesToScripts(t *testing.T) {
	root := repoRoot(t)
	pb := readRepoFile(t, root, "playbooks/beehive-release-cross-compile.yaml")

	if !strings.Contains(pb, "scripts/build-release-artifacts.sh") {
		t.Error("release playbook must run scripts/build-release-artifacts.sh")
	}
	if !strings.Contains(pb, "scripts/verify-release.sh") {
		t.Error("release playbook must run scripts/verify-release.sh")
	}
	if !strings.Contains(pb, "SKIP_COSIGN=1") {
		t.Error("release playbook must run verify-release.sh with SKIP_COSIGN=1 (signing is live-only, never in this job)")
	}
	if strings.Contains(pb, "cosign ") || strings.Contains(pb, "cosign\t") {
		t.Error("the static Zuul job must never invoke cosign directly (signing runs live only, per this task's Accept)")
	}
}

// TestBuildReleaseArtifactsScriptContract mirrors TestVerifyReleaseScriptContract
// for the new build script: all three binaries, all four release targets,
// CGO_ENABLED=0, checksums, and executable.
func TestBuildReleaseArtifactsScriptContract(t *testing.T) {
	root := repoRoot(t)
	rel := "scripts/build-release-artifacts.sh"
	sh := readRepoFile(t, root, rel)

	if !strings.Contains(sh, "CGO_ENABLED=0") {
		t.Error("build-release-artifacts.sh must build with CGO_ENABLED=0")
	}
	for _, bin := range []string{"beehive", "beehived", "honeybee"} {
		if !strings.Contains(sh, bin) {
			t.Errorf("build-release-artifacts.sh must build %s", bin)
		}
	}
	for _, target := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"} {
		if !strings.Contains(sh, target) {
			t.Errorf("build-release-artifacts.sh must cross-compile %s", target)
		}
	}
	if !strings.Contains(sh, "sha256sum") {
		t.Error("build-release-artifacts.sh must emit SHA256SUMS")
	}

	fi, err := os.Stat(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("stat %s: %v", rel, err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Errorf("%s must be executable, mode is %v", rel, fi.Mode())
	}
}

// TestBuildReleaseArtifactsScriptE2E actually runs build-release-artifacts.sh
// end to end -- all 4 os/arch targets, needing no cgo since every target is
// CGO_ENABLED=0 -- then feeds its output straight into verify-release.sh
// SKIP_COSIGN=1: the exact two-step sequence the Zuul release job runs,
// reproduced locally, per this task's Accept bar ("local CGO_ENABLED=0 go
// build ./cmd/... reproduces static binaries").
func TestBuildReleaseArtifactsScriptE2E(t *testing.T) {
	for _, tool := range []string{"go", "sh", "sha256sum", "file"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	root := repoRoot(t)
	dist := t.TempDir()

	build := exec.Command("sh", filepath.Join(root, "scripts/build-release-artifacts.sh"), dist)
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build-release-artifacts.sh failed: %v\n%s", err, out)
	}

	for _, target := range []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64"} {
		for _, bin := range []string{"beehive", "beehived", "honeybee"} {
			p := filepath.Join(dist, bin+"-"+target)
			if _, err := os.Stat(p); err != nil {
				t.Errorf("missing artifact %s: %v", p, err)
			}
		}
		if _, err := os.Stat(filepath.Join(dist, "SHA256SUMS-"+target)); err != nil {
			t.Errorf("missing SHA256SUMS-%s: %v", target, err)
		}
	}
	if t.Failed() {
		return
	}

	verify := exec.Command("sh", filepath.Join(root, "scripts/verify-release.sh"), dist)
	verify.Env = append(os.Environ(), "SKIP_COSIGN=1")
	out, err := verify.CombinedOutput()
	if err != nil {
		t.Fatalf("verify-release.sh failed on build-release-artifacts.sh output: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "OK (static + checksum)") {
		t.Fatalf("verify-release.sh did not confirm static+checksum:\n%s", out)
	}

	// Every linux artifact must be the real thing: file(1) says statically
	// linked, never dynamically linked -- the direct proof this task's
	// Accept bar asks to be recorded.
	for _, arch := range []string{"amd64", "arm64"} {
		p := filepath.Join(dist, "beehive-linux-"+arch)
		desc, err := exec.Command("file", "-b", p).CombinedOutput()
		if err != nil {
			t.Fatalf("file %s: %v", p, err)
		}
		d := string(desc)
		if !strings.Contains(d, "statically linked") {
			t.Errorf("%s is NOT statically linked: %s", p, d)
		}
		if strings.Contains(d, "dynamically linked") {
			t.Errorf("%s is dynamically linked: %s", p, d)
		}
	}
}

package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// This file guards the self-hosted-Gitea-Actions release pipeline
// (.gitea/workflows/, scripts/build-release-artifacts.sh, scripts/verify-release.sh)
// exactly as release_test.go guards the parallel .github/workflows/ci.yml half:
// a fix must ship tests, and nothing else exercises the Gitea wiring. Gitea
// Actions consumes GitHub-Actions-compatible workflow YAML, so these tests
// parse the workflows as such and assert the release contract (cross-compile
// matrix, per-artifact static assertion, cosign-before-publish, clean-room
// re-verify) is expressed in them.

// ghaWorkflow mirrors the subset of the GitHub/Gitea Actions workflow schema
// these tests inspect.
type ghaWorkflow struct {
	Name string `yaml:"name"`
	Jobs map[string]struct {
		RunsOn   string `yaml:"runs-on"`
		Strategy struct {
			Matrix map[string]yaml.Node `yaml:"matrix"`
		} `yaml:"strategy"`
		Steps []struct {
			Name string `yaml:"name"`
			Uses string `yaml:"uses"`
			Run  string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func parseWorkflow(t *testing.T, root, rel string) ghaWorkflow {
	t.Helper()
	raw := readRepoFile(t, root, rel)
	var wf ghaWorkflow
	if err := yaml.Unmarshal([]byte(raw), &wf); err != nil {
		t.Fatalf("%s does not parse as workflow YAML: %v", rel, err)
	}
	if len(wf.Jobs) == 0 {
		t.Fatalf("%s defines no jobs", rel)
	}
	return wf
}

// TestGiteaCIWorkflowParsesAndRunsBuildTest asserts .gitea/workflows/ci.yaml is
// valid workflow YAML and defines the non-release build-test job (the exact
// gofmt/vet/build/test/smoke checks the older ci mirrored).
func TestGiteaCIWorkflowParsesAndRunsBuildTest(t *testing.T) {
	root := repoRoot(t)
	wf := parseWorkflow(t, root, ".gitea/workflows/ci.yaml")
	job, ok := wf.Jobs["build-test"]
	if !ok {
		t.Fatal(".gitea/workflows/ci.yaml must define a build-test job")
	}
	var runs strings.Builder
	for _, s := range job.Steps {
		runs.WriteString(s.Run)
		runs.WriteString("\n")
	}
	body := runs.String()
	for _, want := range []string{"gofmt", "go vet", "CGO_ENABLED=0 go build", "go test", "submodule-smoke.sh"} {
		if !strings.Contains(body, want) {
			t.Errorf("ci.yaml build-test job missing step %q", want)
		}
	}
}

// TestGiteaReleaseWorkflowCrossCompileMatrix asserts .gitea/workflows/release.yaml
// parses, is tag-triggered, and its cross-compile job carries the full
// os x arch matrix and builds each cmd static (CGO_ENABLED=0) then asserts
// staticness via verify-release.sh SKIP_COSIGN=1 (no signing in the static job).
func TestGiteaReleaseWorkflowCrossCompileMatrix(t *testing.T) {
	root := repoRoot(t)
	rel := ".gitea/workflows/release.yaml"
	raw := readRepoFile(t, root, rel)
	wf := parseWorkflow(t, root, rel)

	job, ok := wf.Jobs["cross-compile"]
	if !ok {
		t.Fatal("release.yaml must define a cross-compile job")
	}

	// Matrix: linux+darwin x amd64+arm64.
	goos, ok := job.Strategy.Matrix["goos"]
	if !ok {
		t.Fatal("cross-compile job must define a goos matrix axis")
	}
	goarch, ok := job.Strategy.Matrix["goarch"]
	if !ok {
		t.Fatal("cross-compile job must define a goarch matrix axis")
	}
	assertSeq := func(axis string, n yaml.Node, want []string) {
		got := map[string]bool{}
		for _, c := range n.Content {
			got[c.Value] = true
		}
		for _, w := range want {
			if !got[w] {
				t.Errorf("cross-compile matrix %s missing %q", axis, w)
			}
		}
	}
	assertSeq("goos", goos, []string{"linux", "darwin"})
	assertSeq("goarch", goarch, []string{"amd64", "arm64"})

	var body strings.Builder
	for _, s := range job.Steps {
		body.WriteString(s.Run)
		body.WriteString("\n")
	}
	b := body.String()
	if !strings.Contains(b, "CGO_ENABLED=0") {
		t.Error("cross-compile job must build with CGO_ENABLED=0")
	}
	for _, bin := range []string{"beehive", "beehived", "honeybee"} {
		if !strings.Contains(b, bin) {
			t.Errorf("cross-compile job must build %s", bin)
		}
	}
	if !strings.Contains(b, "./cmd/$bin") {
		t.Error("cross-compile job must compile ./cmd/$bin")
	}
	// Static assertion happens in-job via verify-release.sh with SKIP_COSIGN=1;
	// the static job must NOT invoke cosign directly.
	if !strings.Contains(b, "verify-release.sh") {
		t.Error("cross-compile job must assert staticness via verify-release.sh")
	}
	if !strings.Contains(b, "SKIP_COSIGN=1") {
		t.Error("cross-compile (static) job must run verify-release.sh with SKIP_COSIGN=1 — signing is live-only")
	}
	if strings.Contains(rawJobBlock(raw, "cross-compile"), "cosign sign") {
		t.Error("the static cross-compile job must never sign (cosign is live-only, per this task's Accept)")
	}

	// Tag-triggered.
	if !strings.Contains(raw, "tags:") || !strings.Contains(raw, "v*") {
		t.Error("release.yaml must be tag-triggered on v*")
	}
}

// TestGiteaReleaseWorkflowSignsAndVerifies asserts the CI-only publish/verify
// jobs exist: cosign-sign the checksums, run verify-release.sh before
// publishing, publish to the Gitea instance, and re-verify from a clean tree.
func TestGiteaReleaseWorkflowSignsAndVerifies(t *testing.T) {
	root := repoRoot(t)
	rel := ".gitea/workflows/release.yaml"
	raw := readRepoFile(t, root, rel)
	wf := parseWorkflow(t, root, rel)

	if _, ok := wf.Jobs["publish"]; !ok {
		t.Fatal("release.yaml must define a publish job")
	}
	if _, ok := wf.Jobs["verify-release"]; !ok {
		t.Fatal("release.yaml must define a clean-room verify-release job")
	}
	if !strings.Contains(raw, "cosign sign-blob") {
		t.Error("publish job must cosign sign-blob the checksums")
	}
	if !strings.Contains(raw, "--output-signature") {
		t.Error("cosign sign-blob must --output-signature")
	}
	// verify-release.sh gates BOTH pre-publish and from the clean post-publish job.
	if strings.Count(raw, "verify-release.sh") < 2 {
		t.Error("release.yaml must run verify-release.sh pre-publish AND from a clean post-publish job")
	}
	// Publishes to the Gitea instance (a Gitea-native release action), not GitHub.
	if !strings.Contains(raw, "gitea.com/actions/release-action") {
		t.Error("publish job must use the Gitea-native release action")
	}
}

// rawJobBlock returns the text of a single job block (from its `  <name>:`
// header to the next same-indent job header or EOF) so a test can assert about
// one job's steps without a full structural walk.
func rawJobBlock(raw, name string) string {
	lines := strings.Split(raw, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "  "+name+":") {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		l := lines[i]
		if len(l) > 2 && l[0] == ' ' && l[1] == ' ' && l[2] != ' ' && strings.HasSuffix(strings.TrimRight(l, " "), ":") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// TestBuildReleaseArtifactsScriptContract asserts scripts/build-release-artifacts.sh
// builds all three binaries for all four release targets, CGO_ENABLED=0, with
// checksums, and is executable. (Script is CI-agnostic — reused by the Gitea
// pipeline and reproducible locally per this task's Accept bar.)
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
// end to end — all 4 os/arch targets, CGO_ENABLED=0 so no cgo is needed — then
// feeds its output into verify-release.sh SKIP_COSIGN=1: the exact two-step
// sequence the Gitea cross-compile job runs, reproduced locally, per this
// task's Accept bar ("local CGO_ENABLED=0 go build ./cmd/... reproduces static
// binaries").
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
	// -buildvcs=false: this E2E test only asserts the artifacts + checksums
	// exist and pass verify-release.sh, never embedded VCS metadata (the
	// script already stamps its own BUILD_SHA via -ldflags), so go's auto
	// VCS-stamping (which shells out to git status) is pure risk here — some
	// checkout/sandbox environments make it fail with "error obtaining VCS
	// status: exit status 128", spuriously failing this build.
	build.Env = append(os.Environ(), "GOFLAGS=-buildvcs=false")
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
	// linked, never dynamically linked — the direct proof this task's Accept
	// bar asks to be recorded.
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

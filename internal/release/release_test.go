package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot resolves the module root (two levels up from internal/release).
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("go.mod not found at %s: %v", root, err)
	}
	return root
}

func readRepoFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// TestReleaseWorkflowSignsCertAndVerifies asserts the release job cross-compiles
// statically, captures the Fulcio certificate (without which keyless verify is
// impossible), and re-verifies the artifacts — both before publishing and from a
// clean post-publish runner.
func TestReleaseWorkflowSignsCertAndVerifies(t *testing.T) {
	ci := readRepoFile(t, repoRoot(t), ".github/workflows/ci.yml")

	if !strings.Contains(ci, "CGO_ENABLED=0") {
		t.Error("release build must set CGO_ENABLED=0 (static binaries)")
	}
	if !strings.Contains(ci, "beehive beehived honeybee") {
		t.Error("release must build all of: beehive beehived honeybee")
	}
	if !strings.Contains(ci, "./cmd/$bin") {
		t.Error("release must compile ./cmd/$bin")
	}
	// Keyless signing must emit BOTH the signature and the certificate.
	if !strings.Contains(ci, "--output-signature") {
		t.Error("cosign sign-blob must --output-signature")
	}
	if !strings.Contains(ci, "--output-certificate") {
		t.Error("cosign sign-blob must --output-certificate (keyless verify needs the Fulcio cert)")
	}
	// The verification script must gate the release, both pre-publish (in the
	// release job) and from a clean post-publish runner (the verify-release job).
	if strings.Count(ci, "verify-release.sh") < 2 {
		t.Error("ci must run verify-release.sh pre-publish AND from a clean post-publish runner")
	}
	if !strings.Contains(ci, "verify-release:") {
		t.Error("ci must define the post-publish verify-release job")
	}
	if !strings.Contains(ci, "gh release download") {
		t.Error("the clean-room verify-release job must download the PUBLISHED artifacts")
	}
}

// TestVerifyReleaseScriptContract asserts scripts/verify-release.sh performs the
// three checks (static, checksum, keyless cosign) and is executable.
func TestVerifyReleaseScriptContract(t *testing.T) {
	root := repoRoot(t)
	rel := "scripts/verify-release.sh"
	sh := readRepoFile(t, root, rel)

	// Static: CGO-free build info + linux ELF static-link assertion.
	if !strings.Contains(sh, "CGO_ENABLED=0") {
		t.Error("verify-release.sh must assert the binaries are CGO_ENABLED=0")
	}
	if !strings.Contains(sh, "statically linked") {
		t.Error("verify-release.sh must assert linux binaries are 'statically linked'")
	}
	// Checksums.
	if !strings.Contains(sh, "sha256sum -c") {
		t.Error("verify-release.sh must run sha256sum -c")
	}
	// Keyless cosign with the full flag set (cosign v2 requires cert+identity+issuer).
	if !strings.Contains(sh, "verify-blob") {
		t.Error("verify-release.sh must run cosign verify-blob")
	}
	for _, flag := range []string{"--certificate ", "--certificate-identity", "--certificate-oidc-issuer"} {
		if !strings.Contains(sh, flag) {
			t.Errorf("verify-release.sh cosign call missing %q", strings.TrimSpace(flag))
		}
	}
	fi, err := os.Stat(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("stat %s: %v", rel, err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Errorf("%s must be executable, mode is %v", rel, fi.Mode())
	}
}

// TestReleaseNotesVerifyCommand asserts the documented verify command is the
// complete keyless form (the bare --signature-only form fails under cosign v2).
func TestReleaseNotesVerifyCommand(t *testing.T) {
	rn := readRepoFile(t, repoRoot(t), "docs/RELEASE-NOTES-TEMPLATE.md")
	for _, want := range []string{
		"verify-blob",
		"--signature",
		"--certificate ",
		"--certificate-identity",
		"--certificate-oidc-issuer",
		"sha256sum -c",
	} {
		if !strings.Contains(rn, want) {
			t.Errorf("RELEASE-NOTES-TEMPLATE.md verify block missing %q", strings.TrimSpace(want))
		}
	}
}

// buildHostBins cross-compiles the three commands for the host into dir with
// CGO_ENABLED=0 and writes a SHA256SUMS-<os>-<arch>. Returns the os-arch tag.
func buildHostBins(t *testing.T, root, dir string) string {
	t.Helper()
	tag := runtime.GOOS + "-" + runtime.GOARCH
	names := []string{}
	for _, bin := range []string{"beehive", "beehived", "honeybee"} {
		out := filepath.Join(dir, bin+"-"+tag)
		b := exec.Command("go", "build", "-trimpath", "-o", out, "./cmd/"+bin)
		b.Dir = root
		b.Env = append(os.Environ(), "CGO_ENABLED=0")
		if o, err := b.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", bin, err, o)
		}
		names = append(names, bin+"-"+tag)
	}
	// sha256sum <bins> > SHA256SUMS-<tag>, run inside dir.
	sums, err := os.Create(filepath.Join(dir, "SHA256SUMS-"+tag))
	if err != nil {
		t.Fatalf("create sums: %v", err)
	}
	defer sums.Close()
	sc := exec.Command("sha256sum", names...)
	sc.Dir = dir
	sc.Stdout = sums
	if err := sc.Run(); err != nil {
		t.Fatalf("sha256sum: %v", err)
	}
	return tag
}

// TestVerifyReleaseScriptStaticChecksumE2E actually runs scripts/verify-release.sh
// over freshly cross-compiled artifacts: the real static (CGO_ENABLED=0 +
// statically-linked) and checksum checks must pass. Cosign is skipped (keyless
// verify needs OIDC/network — exercised in-pipeline).
func TestVerifyReleaseScriptStaticChecksumE2E(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("static-ELF assertion is linux-only")
	}
	for _, tool := range []string{"go", "sh", "sha256sum", "file"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	root := repoRoot(t)
	dist := t.TempDir()
	buildHostBins(t, root, dist)

	cmd := exec.Command("sh", filepath.Join(root, "scripts/verify-release.sh"), dist)
	cmd.Env = append(os.Environ(), "SKIP_COSIGN=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("verify-release.sh failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "OK (static + checksum)") {
		t.Fatalf("verify-release.sh did not confirm static+checksum:\n%s", out)
	}

	// Negative control: tamper a binary -> checksum check must fail.
	tampered := filepath.Join(dist, "beehive-"+runtime.GOOS+"-"+runtime.GOARCH)
	f, err := os.OpenFile(tampered, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", tampered, err)
	}
	_, _ = f.WriteString("x")
	f.Close()
	bad := exec.Command("sh", filepath.Join(root, "scripts/verify-release.sh"), dist)
	bad.Env = append(os.Environ(), "SKIP_COSIGN=1")
	if o, err := bad.CombinedOutput(); err == nil {
		t.Fatalf("verify-release.sh passed on a tampered binary:\n%s", o)
	}
}

// TestStaticBuildIsCGOFreeAndStatic is the direct confirmation of the ROI claim:
// a CGO_ENABLED=0 build of cmd/beehive records CGO_ENABLED=0 and (on linux) is a
// statically linked ELF — the two facts the pipeline's static check relies on.
func TestStaticBuildIsCGOFreeAndStatic(t *testing.T) {
	root := repoRoot(t)
	out := filepath.Join(t.TempDir(), "beehive")
	build := exec.Command("go", "build", "-trimpath", "-o", out, "./cmd/beehive")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("static build failed: %v\n%s", err, b)
	}

	info, err := exec.Command("go", "version", "-m", out).CombinedOutput()
	if err != nil {
		t.Fatalf("go version -m: %v", err)
	}
	if !strings.Contains(string(info), "CGO_ENABLED=0") {
		t.Fatalf("cmd/beehive build info does not record CGO_ENABLED=0:\n%s", info)
	}

	if runtime.GOOS != "linux" {
		t.Skip("statically-linked ELF assertion is linux-only (darwin is Mach-O)")
	}
	if _, err := exec.LookPath("file"); err != nil {
		t.Skip("'file' not on PATH")
	}
	desc, err := exec.Command("file", "-b", out).CombinedOutput()
	if err != nil {
		t.Fatalf("file %s: %v", out, err)
	}
	d := string(desc)
	if !strings.Contains(d, "statically linked") {
		t.Fatalf("cmd/beehive is NOT statically linked: %s", d)
	}
	if strings.Contains(d, "dynamically linked") {
		t.Fatalf("cmd/beehive is dynamically linked: %s", d)
	}
}

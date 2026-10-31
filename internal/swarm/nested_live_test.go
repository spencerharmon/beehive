package swarm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/checkpolicy"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// Opt-in deployment probe: use a fresh clone, then the same preparation and
// sandbox boundary as recovery/merged-tree checks. No host-specific defaults.
func TestNestedCheckoutLiveCheck(t *testing.T) {
	url, check := os.Getenv("BEEHIVE_NESTED_CHECK_REPO"), os.Getenv("BEEHIVE_NESTED_CHECK_COMMAND")
	if url == "" || check == "" {
		t.Skip("set BEEHIVE_NESTED_CHECK_REPO and BEEHIVE_NESTED_CHECK_COMMAND for a live probe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	root := t.TempDir()
	cwd := filepath.Join(root, "source")
	if _, err := git.New(root).Run(ctx, "clone", "--no-recurse-submodules", url, cwd); err != nil {
		t.Fatal(err)
	}
	if ref := os.Getenv("BEEHIVE_NESTED_CHECK_REF"); ref != "" {
		if _, err := git.New(cwd).Run(ctx, "checkout", "--detach", ref); err != nil {
			t.Fatal(err)
		}
	}
	head, err := git.New(cwd).RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	pol := checkpolicy.Default()
	r := &Runner{CheckPolicy: &pol}
	sel := &selectt.Selection{Submodule: repo.Submodule{Name: "probe"}, Task: plan.Task{ID: "nested-live-check"}}
	o, err := r.runCheckIn(ctx, sel, check, cwd)
	t.Logf("fresh checkout HEAD=%s\nCheck: %s\n%s", head, check, o.out)
	if err != nil || o.exitErr {
		t.Fatalf("runner-prepared check failed: %v (exit failure=%v)", err, o.exitErr)
	}
	status, err := git.New(cwd).Run(ctx, "submodule", "status", "--recursive")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("prepared nested pins:\n%s", status)
}

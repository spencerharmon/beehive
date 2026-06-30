package editor

import (
	"strings"
	"testing"
)

func rowsText(rows []DiffRow) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.Kind + ":" + string(r.HTML) + "\n")
	}
	return b.String()
}

func TestRenderDiffAddRemoveChange(t *testing.T) {
	old := "alpha\nbeta\ngamma\n"
	new := "alpha\nbeta changed\ndelta\ngamma\n"
	rows := RenderDiff(old, new)
	got := rowsText(rows)
	if !strings.Contains(got, "eq:alpha") {
		t.Errorf("missing equal line:\n%s", got)
	}
	// "beta" -> "beta changed" is a replace: del+add with a column span.
	if !strings.Contains(got, `del:beta`) || !strings.Contains(got, `add:beta<span class="chg"> changed</span>`) {
		t.Errorf("column highlight wrong:\n%s", got)
	}
	if !strings.Contains(got, "add:delta") {
		t.Errorf("missing inserted line:\n%s", got)
	}
}

func TestRenderDiffNoChange(t *testing.T) {
	rows := RenderDiff("same\ntext\n", "same\ntext\n")
	for _, r := range rows {
		if r.Kind != "eq" {
			t.Fatalf("unexpected non-eq row %+v", r)
		}
	}
}

func TestRenderDiffEscapesHTML(t *testing.T) {
	rows := RenderDiff("", "<script>\n")
	if !strings.Contains(rowsText(rows), "add:&lt;script&gt;") {
		t.Fatalf("html not escaped: %s", rowsText(rows))
	}
}

func TestValidateFile(t *testing.T) {
	// The chat-diff editor is generic over ANY repo-relative file: coordination
	// files, submodule files, source, and not-yet-existing new files are all valid
	// targets. Ownership policy (e.g. ROI.md / PLAN.md) is enforced by git hooks at
	// commit time, NOT by path validation. Only paths that escape the repo (or dive
	// into its .git plumbing) are rejected here.
	ok := []string{
		"PLAN.md", "AGENTS.md", "ROI.md",
		"submodules/x/ROI.md", "INFRASTRUCTURE.md",
		"submodules/y/SUBMODULE-LINKS.yaml",
		"internal/web/web.go",         // source
		"submodules/x/secret.txt",     // arbitrary file
		"a/b/c/d/new-file-not-yet.md", // new file, deep path
		"./PLAN.md",                   // cleans to PLAN.md
	}
	for _, f := range ok {
		if err := ValidateFile(f); err != nil {
			t.Errorf("want ok for %q: %v", f, err)
		}
	}
	bad := []string{
		"",                                    // empty
		".",                                   // repo root, not a file
		"..",                                  // parent
		"../etc/passwd",                       // traversal
		"/etc/passwd",                         // absolute
		"submodules/x/../../../escape/ROI.md", // cleans to ../escape/ROI.md (escapes)
		".git",                                // git dir itself
		".git/config",                         // inside git plumbing
	}
	for _, f := range bad {
		if err := ValidateFile(f); err == nil {
			t.Errorf("want error for %q", f)
		}
	}
}

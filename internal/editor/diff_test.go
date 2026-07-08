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
	// The editable set is the union of the repo's declared coordination-file sets:
	// the per-submodule optional files (ROI/INFRASTRUCTURE/RULES/ARTIFACTS/AGENTS),
	// the repo-ROOT instruction files (AGENTS/HONEYBEE/BOOTSTRAP/LOCALS) and the
	// submodule-links registry — both submodule-qualified and root-level.
	ok := []string{
		"submodules/x/ROI.md",
		"INFRASTRUCTURE.md",
		"submodules/y/SUBMODULE-LINKS.yaml",
		"submodules/x/RULES.md",
		"submodules/x/ARTIFACTS.md",
		"submodules/x/AGENTS.md",
		"AGENTS.md",
		"HONEYBEE.md",
		"BOOTSTRAP.md",
		"LOCALS.md",
	}
	for _, f := range ok {
		if err := ValidateFile(f); err != nil {
			t.Errorf("want ok for %q: %v", f, err)
		}
	}
	// PLAN.md is honeybee-owned (reconcile writes it); secrets and code are off
	// limits; traversal must be rejected.
	bad := []string{"PLAN.md", "SECRETS.yaml.gpg", "../etc/passwd", "submodules/x/../../escape/ROI.md.x", "submodules/x/secret.txt"}
	for _, f := range bad {
		if err := ValidateFile(f); err == nil {
			t.Errorf("want error for %q", f)
		}
	}
}

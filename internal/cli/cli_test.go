package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/koopycat/cf-redirect/internal/domain"
	"github.com/koopycat/cf-redirect/internal/planner"
)

func TestRenderPlanIncludesDeterministicMarkersAndCounts(t *testing.T) {
	old := domain.Redirect{ID: "one", Source: "https://old.example", Target: "https://before.example", StatusCode: 301}
	updated := old
	updated.Target = "https://after.example"
	added := domain.New("https://add.example", "https://target.example")
	plan := planner.Plan{Changes: []planner.Change{
		{Kind: planner.Add, After: &added},
		{Kind: planner.Update, Before: &old, After: &updated},
		{Kind: planner.Delete, Before: &old},
	}}
	var output bytes.Buffer
	if err := renderPlan(&output, plan); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Plan: 1 add, 1 update, 1 delete", "+ https://add.example", "~ https://old.example -> https://before.example => https://old.example -> https://after.example", "- https://old.example"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("plan output %q does not contain %q", output.String(), want)
		}
	}
}

func TestFindSourceRequiresExactMatchAndID(t *testing.T) {
	items := []domain.Redirect{{ID: "id-1", Source: "https://example.com/a", Target: "https://target.example", StatusCode: 301}}
	item, err := findSource(items, "https://example.com/a")
	if err != nil || item.ID != "id-1" {
		t.Fatalf("findSource() = %#v, %v", item, err)
	}
	if _, err := findSource(items, "https://example.com"); err == nil {
		t.Fatal("partial source must not resolve")
	}
}

func TestTerminalCommentSanitization(t *testing.T) {
	if got := safeTerminalText("normal\x1b[31mred\x07\nline"); got != "normal[31mredline" {
		t.Fatalf("safeTerminalText() = %q", got)
	}
	items := []domain.Redirect{{Source: "example.com", Target: "https://target.example", StatusCode: 301, Comment: "unsafe\x1b[2Jcomment"}}
	for _, format := range []string{"table", "json", "csv"} {
		var output bytes.Buffer
		if err := renderRedirects(&output, items, format); err != nil {
			t.Fatal(err)
		}
		if strings.ContainsRune(output.String(), '\x1b') {
			t.Fatalf("%s output retained escape: %q", format, output.String())
		}
	}
}

func TestRootIncludesRequiredCommands(t *testing.T) {
	root := NewRootCmd()
	for _, name := range []string{"list", "search", "add", "edit", "delete", "import", "config", "auth", "login", "logout", "status", "tui"} {
		if _, _, err := root.Find([]string{name}); err != nil {
			t.Fatalf("command %q is missing: %v", name, err)
		}
	}
}

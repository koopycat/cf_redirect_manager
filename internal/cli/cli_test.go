package cli

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/koopycat/cf-redirect/internal/domain"
	"github.com/koopycat/cf-redirect/internal/planner"
	"github.com/koopycat/cf-redirect/internal/textsafe"
	"github.com/spf13/cobra"
)

func TestRenderPlanIncludesDeterministicMarkersAndCounts(t *testing.T) {
	old := domain.Redirect{ID: "one", Source: "https://old.example", Target: "https://before.example", StatusCode: 301}
	updated := old
	updated.Target = "https://after.example"
	added := domain.New("https://add.example", "https://target.example")
	plan := planner.Plan{
		Changes: []planner.Change{
			{Kind: planner.Add, After: &added},
			{Kind: planner.Update, Before: &old, After: &updated},
			{Kind: planner.Delete, Before: &old},
		},
		SkippedExisting: 2,
	}
	var output bytes.Buffer
	if err := renderPlan(&output, plan); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Plan: 1 add, 1 update, 1 delete, 2 skipped existing", "+ https://add.example", "~ https://old.example -> https://before.example => https://old.example -> https://after.example", "- https://old.example"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("plan output %q does not contain %q", output.String(), want)
		}
	}
}

func TestRenderPlanShowsAllSkippedImport(t *testing.T) {
	plan := planner.Plan{SkippedExisting: 3}
	if !plan.Empty() {
		t.Fatal("reporting skipped rows must not make a plan actionable")
	}
	var output bytes.Buffer
	if err := renderPlan(&output, plan); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "Plan: 0 add, 0 update, 0 delete, 3 skipped existing\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRenderPlanOmitsSkippedCountWhenZero(t *testing.T) {
	var output bytes.Buffer
	if err := renderPlan(&output, planner.Plan{}); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "Plan: 0 add, 0 update, 0 delete\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

// A failing output must never let a mutation proceed without a visible plan.
func TestRenderPlanFailsOnWriterError(t *testing.T) {
	added := domain.New("https://add.example", "https://target.example")
	plan := planner.Plan{Changes: []planner.Change{{Kind: planner.Add, After: &added}}}
	if err := renderPlan(failingWriter{}, plan); err == nil {
		t.Fatal("renderPlan must report a failing output writer")
	}
}

// Non-terminal stdin must trigger a clear error instead of a hidden prompt.
func TestReadPasswordInteractiveRequiresTTY(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("not-a-tty\n"))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if _, err := readPasswordInteractive(cmd, "Cloudflare API token: "); err == nil {
		t.Fatal("readPasswordInteractive must fail when stdin is not a terminal")
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

func TestRenderPlanSanitizesRemoteText(t *testing.T) {
	item := domain.New("source\x1b[2J", "target\x07")
	plan := planner.Plan{Changes: []planner.Change{{Kind: planner.Add, After: &item}}}
	var output bytes.Buffer
	if err := renderPlan(&output, plan); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(output.String(), '\x1b') || strings.ContainsRune(output.String(), '\x07') {
		t.Fatalf("plan output retained control characters: %q", output.String())
	}
}

func TestTerminalCommentSanitization(t *testing.T) {
	if got := textsafe.StripControls("normal\x1b[31mred\x07\nline"); got != "normal[31mredline" {
		t.Fatalf("StripControls() = %q", got)
	}
	items := []domain.Redirect{{Source: "example.com\x1b[2J", Target: "https://target.example\x07", StatusCode: 301, Comment: "unsafe\x1b[2Jcomment"}}
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

func TestClearCommandRequiresNoArgumentsAndHasMutationGuards(t *testing.T) {
	root := NewRootCmd()
	clear, _, err := root.Find([]string{"clear"})
	if err != nil {
		t.Fatal(err)
	}
	if clear.Args == nil || clear.Args(clear, []string{"unexpected"}) == nil {
		t.Fatal("clear must reject positional arguments")
	}
	for _, name := range []string{"dry-run", "yes"} {
		if clear.Flags().Lookup(name) == nil {
			t.Fatalf("clear is missing --%s", name)
		}
	}
}

func TestRootIncludesRequiredCommands(t *testing.T) {
	root := NewRootCmd()
	for _, name := range []string{"list", "search", "add", "edit", "delete", "clear", "import", "config", "auth", "login", "logout", "status", "tui"} {
		if _, _, err := root.Find([]string{name}); err != nil {
			t.Fatalf("command %q is missing: %v", name, err)
		}
	}
}

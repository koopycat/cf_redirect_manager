package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/koopycat/cf-redirect/internal/domain"
	"github.com/koopycat/cf-redirect/internal/planner"
)

func TestFilterSearchesSourceTargetAndComment(t *testing.T) {
	m := newModel(nil, "account", "list")
	m.items = []domain.Redirect{
		{ID: "one", Source: "https://one.example", Target: "https://target.example", StatusCode: 301, Comment: "primary"},
		{ID: "two", Source: "https://two.example", Target: "https://else.example", StatusCode: 301, Comment: "secondary"},
	}
	m.query = "PRIMARY"
	items := m.filtered()
	if len(items) != 1 || items[0].ID != "one" {
		t.Fatalf("filtered = %#v", items)
	}
}

func TestSearchClampsFilteredSelection(t *testing.T) {
	m := newModel(nil, "account", "list")
	m.loading = false
	m.mode = searchMode
	m.items = []domain.Redirect{
		{ID: "one", Source: "https://one.example", Target: "https://target.example", StatusCode: 301},
		{ID: "two", Source: "https://two.example", Target: "https://target.example", StatusCode: 301},
	}
	m.selected = 1
	m.input.Focus()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("one")})
	got := updated.(model)
	if got.selected != 0 || len(got.filtered()) != 1 {
		t.Fatalf("selection = %d, filtered = %#v", got.selected, got.filtered())
	}
}

func TestPlanRequiresYAndSanitizesComments(t *testing.T) {
	m := newModel(nil, "account", "list")
	m.loading = false
	m.mode = planMode
	before := domain.Redirect{ID: "one", Source: "old.example", Target: "https://old-target.example", StatusCode: 301}
	after := before
	after.Source = "new.example"
	after.Target = "https://new-target.example"
	m.plan = planner.Plan{Changes: []planner.Change{{Kind: planner.Update, Before: &before, After: &after}}}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(model)
	if got.applying || cmd != nil {
		t.Fatal("Enter must not apply a plan")
	}
	view := got.planView()
	for _, want := range []string{before.Source, before.Target, after.Source, after.Target, "Press y"} {
		if !strings.Contains(view, want) {
			t.Fatalf("plan view %q missing %q", view, want)
		}
	}
	if sanitized := safeTerminalText("safe\x1b[2J\ntext"); sanitized != "safe[2Jtext" {
		t.Fatalf("sanitized comment = %q", sanitized)
	}
}

func TestQuitDuringApplyDoesNotExit(t *testing.T) {
	m := newModel(nil, "account", "list")
	m.loading = false
	m.applying = true
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	got := updated.(model)
	if cmd != nil || !got.applying || !strings.Contains(got.status, "continues") {
		t.Fatalf("quit changed apply state: applying=%v status=%q cmd=%v", got.applying, got.status, cmd)
	}
}

func TestCurrentUsesFilteredSelection(t *testing.T) {
	m := newModel(nil, "account", "list")
	m.items = []domain.Redirect{{ID: "one", Source: "https://one.example", Target: "https://target.example", StatusCode: 301}}
	item, ok := m.current()
	if !ok || item.ID != "one" {
		t.Fatalf("current = %#v, %v", item, ok)
	}
}

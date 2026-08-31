package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/koopycat/cf-redirect/internal/domain"
	"github.com/koopycat/cf-redirect/internal/planner"
)

func TestApplyErrorPersistsThroughReloadUntilDismissed(t *testing.T) {
	m := newModel(nil, "account", "list")
	m.width = 80
	m.height = 24
	// Simulate: apply fails, then a successful reload arrives. The error must
	// survive the reload so the user can read it.
	first, _ := m.Update(appliedMsg{err: errors.New("bulk operation ended with status failed")})
	second, _ := first.Update(loadedMsg{items: nil, err: nil})
	got := second.(model)
	if got.err == nil {
		t.Fatal("apply error was cleared by the reload before the user read it")
	}
	if !strings.Contains(got.View(), "bulk operation ended with status failed") {
		t.Fatal("error message not visible in the TUI view")
	}
	// Any key acknowledges and hides the error.
	third, _ := got.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	if third.(model).err != nil {
		t.Fatal("error not dismissed by a key")
	}
}

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

func TestQuitDuringApplyCancelsLocalWait(t *testing.T) {
	m := newModel(nil, "account", "list")
	m.loading = false
	m.applying = true
	ctx, cancel := context.WithCancel(context.Background())
	m.applyCancel = cancel
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	got := updated.(model)
	if cmd != nil || !got.applying || got.applyCancel != nil || !strings.Contains(got.status, "Stopping local wait") {
		t.Fatalf("quit did not start cancellation: applying=%v status=%q cmd=%v", got.applying, got.status, cmd)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("apply context was not cancelled")
	}
}

func TestCancelledApplyShowsHonestExitMessage(t *testing.T) {
	m := newModel(nil, "account", "list")
	m.loading = false
	m.applying = true
	updated, _ := m.Update(appliedMsg{err: fmt.Errorf("apply plan: %w", context.Canceled)})
	got := updated.(model)
	if got.applying || !got.interrupted || got.loading || !strings.Contains(got.status, "Cloudflare operation may still finish") {
		t.Fatalf("cancelled apply state: applying=%v interrupted=%v loading=%v status=%q", got.applying, got.interrupted, got.loading, got.status)
	}
	_, cmd := got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q must exit after local wait is cancelled")
	}
}

func TestApplyingViewShowsLiveProgress(t *testing.T) {
	m := newModel(nil, "account", "list")
	m.width = 100
	m.height = 30
	m.loading = false
	m.applying = true
	m.progress.set("create phase: waiting for operation op-123…")
	view := m.View()
	for _, want := range []string{"create phase", "op-123", "q/esc stops waiting locally"} {
		if !strings.Contains(view, want) {
			t.Fatalf("applying view missing %q: %q", want, view)
		}
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

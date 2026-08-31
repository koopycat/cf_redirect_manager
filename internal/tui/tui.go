// Package tui implements the focused interactive redirect editor.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/koopycat/cf-redirect/internal/app"
	"github.com/koopycat/cf-redirect/internal/cloudflare"
	"github.com/koopycat/cf-redirect/internal/domain"
	"github.com/koopycat/cf-redirect/internal/planner"
	"github.com/koopycat/cf-redirect/internal/textsafe"
)

var (
	rose          = lipgloss.AdaptiveColor{Light: "#7f284f", Dark: "#d4769d"}
	muted         = lipgloss.AdaptiveColor{Light: "#666666", Dark: "#999999"}
	green         = lipgloss.AdaptiveColor{Light: "#287a4b", Dark: "#70c995"}
	amber         = lipgloss.AdaptiveColor{Light: "#8a5b00", Dark: "#e2b451"}
	red           = lipgloss.AdaptiveColor{Light: "#a12a35", Dark: "#f08089"}
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(rose)
	mutedStyle    = lipgloss.NewStyle().Foreground(muted)
	selectedStyle = lipgloss.NewStyle().Foreground(rose).Bold(true)
)

type api interface{ app.RedirectAPI }
type loadedMsg struct {
	items []domain.Redirect
	err   error
}
type appliedMsg struct{ err error }

type progressTracker struct {
	mu      sync.RWMutex
	message string
}

func (p *progressTracker) set(message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.message = message
}

func (p *progressTracker) get() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.message
}

type mode int

const (
	listMode mode = iota
	formMode
	planMode
)

type keys struct {
	add, edit, delete, cancel, quit key.Binding
}

func newKeys() keys {
	return keys{
		add:    key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
		edit:   key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		delete: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
		cancel: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		quit:   key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// redirectItem is the small adapter between the domain model and bubbles/list.
type redirectItem struct{ redirect domain.Redirect }

func (i redirectItem) FilterValue() string {
	return strings.Join([]string{i.redirect.Source, i.redirect.Target, i.redirect.Comment}, " ")
}

type redirectDelegate struct{ narrow bool }

func (d redirectDelegate) Height() int {
	if d.narrow {
		return 2
	}
	return 1
}
func (redirectDelegate) Spacing() int { return 0 }
func (redirectDelegate) Update(tea.Msg, *list.Model) tea.Cmd {
	return nil
}

func (d redirectDelegate) Render(w io.Writer, m list.Model, index int, value list.Item) {
	item, ok := value.(redirectItem)
	if !ok || m.Width() <= 0 {
		return
	}

	width := max(1, m.Width()-2) // reserve the selection marker
	source := safeTerminalText(item.redirect.Source)
	target := safeTerminalText(item.redirect.Target)
	if comment := safeTerminalText(item.redirect.Comment); comment != "" {
		target += mutedStyle.Render("  " + comment)
	}

	var line string
	if d.narrow {
		line = ansi.Truncate(source, width, "…") + "\n  " + mutedStyle.Render("→ ") + ansi.Truncate(target, max(1, width-4), "…")
	} else {
		arrow := mutedStyle.Render("  →  ")
		available := max(2, width-lipgloss.Width(arrow))
		sourceWidth := available / 2
		line = ansi.Truncate(source, sourceWidth, "…") + arrow + ansi.Truncate(target, available-sourceWidth, "…")
	}
	if index == m.Index() && !m.SettingFilter() {
		fmt.Fprint(w, selectedStyle.Render("› "+line)) //nolint:errcheck
		return
	}
	fmt.Fprint(w, "  "+line) //nolint:errcheck
}

type model struct {
	api               api
	accountID, listID string
	items             []domain.Redirect
	redirects         list.Model
	mode              mode
	source, target    textinput.Model
	editing           bool
	plan              planner.Plan
	loading           bool
	applying          bool
	applyCancel       context.CancelFunc
	progress          *progressTracker
	interrupted       bool
	err               error
	status            string
	width, height     int
	spin              spinner.Model
	keys              keys
}

// Run starts the full-screen terminal interface.
func Run(client app.RedirectAPI, accountID, listID string) error {
	m := newModel(client, accountID, listID)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func newModel(client api, accountID, listID string) model {
	keys := newKeys()
	redirects := list.New(nil, redirectDelegate{}, 0, 0)
	redirects.SetShowTitle(false)
	redirects.SetShowFilter(true)
	redirects.SetShowStatusBar(true)
	redirects.SetShowPagination(true)
	redirects.SetShowHelp(true)
	redirects.SetStatusBarItemName("redirect", "redirects")
	redirects.Filter = list.UnsortedFilter
	redirects.FilterInput.Prompt = "Search: "
	redirects.FilterInput.CharLimit = 500
	redirects.KeyMap.Filter = key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search"))
	redirects.KeyMap.NextPage = key.NewBinding(key.WithKeys("right", "l", "pgdown", "f"), key.WithHelp("→/l/pgdn", "next page"))
	redirects.KeyMap.Quit = keys.quit
	redirects.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{keys.add, keys.edit, keys.delete}
	}
	redirects.AdditionalFullHelpKeys = redirects.AdditionalShortHelpKeys
	redirects.Styles.TitleBar = lipgloss.NewStyle()
	redirects.Styles.FilterPrompt = titleStyle
	redirects.Styles.FilterCursor = titleStyle
	redirects.Styles.StatusBar = mutedStyle
	redirects.Styles.StatusEmpty = mutedStyle
	redirects.Styles.StatusBarActiveFilter = lipgloss.NewStyle()
	redirects.Styles.StatusBarFilterCount = mutedStyle
	redirects.Styles.NoItems = mutedStyle
	redirects.Styles.PaginationStyle = lipgloss.NewStyle()
	redirects.Styles.HelpStyle = mutedStyle.PaddingTop(1)
	redirects.Styles.ActivePaginationDot = selectedStyle.SetString("•")
	redirects.Styles.InactivePaginationDot = mutedStyle.SetString("•")
	redirects.Styles.ArabicPagination = mutedStyle
	redirects.Styles.DividerDot = mutedStyle.SetString(" • ")

	source := textinput.New()
	source.Prompt = "Source URL: "
	source.CharLimit = 2048
	target := textinput.New()
	target.Prompt = "Target URL: "
	target.CharLimit = 2048
	s := spinner.New()
	s.Spinner = spinner.Line
	return model{
		api:       client,
		accountID: accountID,
		listID:    listID,
		redirects: redirects,
		source:    source,
		target:    target,
		spin:      s,
		keys:      keys,
		loading:   true,
		status:    "Loading redirects…",
		progress:  &progressTracker{},
	}
}

func (m model) Init() tea.Cmd { return tea.Batch(m.spin.Tick, m.load()) }

func (m model) load() tea.Cmd {
	return func() tea.Msg {
		items, err := m.api.ListItems(context.Background(), m.accountID, m.listID)
		return loadedMsg{items, err}
	}
}

func (m model) apply(ctx context.Context) tea.Cmd {
	plan := m.plan
	progress := m.progress
	return func() tea.Msg {
		executor := app.Executor{
			API:          m.api,
			AccountID:    m.accountID,
			ListID:       m.listID,
			PollInterval: cloudflare.DefaultBulkPollInterval,
			Progress:     progress.set,
		}
		_, err := executor.Apply(ctx, plan)
		return appliedMsg{err}
	}
}

func (m *model) resizeList() {
	m.redirects.SetDelegate(redirectDelegate{narrow: m.width < 75})
	// Header, operation status, and their separator are outside the list.
	m.redirects.SetSize(max(1, m.width), max(1, m.height-3))
}

func redirectItems(items []domain.Redirect) []list.Item {
	result := make([]list.Item, len(items))
	for i, item := range items {
		result[i] = redirectItem{redirect: item}
	}
	return result
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
		m.source.Width = max(1, v.Width-lipgloss.Width(m.source.Prompt)-1)
		m.target.Width = max(1, v.Width-lipgloss.Width(m.target.Prompt)-1)
		m.resizeList()
	case spinner.TickMsg:
		if !m.loading && !m.applying {
			break
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(v)
		cmds = append(cmds, cmd)
	case list.FilterMatchesMsg:
		var cmd tea.Cmd
		m.redirects, cmd = m.redirects.Update(v)
		cmds = append(cmds, cmd)
	case loadedMsg:
		m.loading = false
		// Only adopt a new load error; never clear an apply error the user has
		// not had a chance to read yet.
		if v.err != nil {
			m.err = v.err
		}
		if v.err == nil {
			selectedID := ""
			if selected, ok := m.current(); ok {
				selectedID = selected.ID
			}
			filter := m.redirects.FilterValue()
			m.items = v.items
			m.redirects.SetItems(redirectItems(v.items))
			if filter != "" {
				m.redirects.SetFilterText(filter)
			}
			m.selectID(selectedID)
			if m.status == "Loading redirects…" {
				m.status = fmt.Sprintf("%d redirects", len(v.items))
			}
		}
	case appliedMsg:
		m.applying = false
		if m.applyCancel != nil {
			m.applyCancel()
			m.applyCancel = nil
		}
		m.mode = listMode
		m.plan = planner.Plan{}
		if errors.Is(v.err, context.Canceled) {
			m.err = nil
			m.interrupted = true
			m.status = "Stopped waiting locally; a submitted Cloudflare operation may still finish. Press q/esc to exit, then reopen to verify."
			break
		}
		if v.err != nil {
			m.err = v.err
			m.status = "Apply failed"
		} else {
			m.err = nil
			m.status = "Plan applied"
		}
		// Reload remote state on success or failure so the editor cannot
		// silently retry an invalid plan.
		m.loading = true
		cmds = append(cmds, m.load())
	case tea.KeyMsg:
		if m.applying {
			if key.Matches(v, m.keys.quit) || key.Matches(v, m.keys.cancel) {
				if m.applyCancel != nil {
					m.applyCancel()
					m.applyCancel = nil
					m.status = "Stopping local wait; Cloudflare may still finish a submitted operation…"
				}
			}
			break
		}
		if m.loading {
			if key.Matches(v, m.keys.quit) {
				return m, tea.Quit
			}
			break
		}
		if m.interrupted {
			if key.Matches(v, m.keys.quit) || key.Matches(v, m.keys.cancel) {
				return m, tea.Quit
			}
			break
		}
		// A visible error is a modal screen: any key acknowledges it. The full
		// message stays on screen until the user has read it.
		if m.err != nil {
			m.err = nil
			break
		}
		if m.mode == formMode {
			if key.Matches(v, m.keys.cancel) {
				m.mode = listMode
				m.source.Blur()
				m.target.Blur()
				break
			}
			if v.Type == tea.KeyEnter {
				if m.source.Focused() {
					m.source.Blur()
					cmds = append(cmds, m.target.Focus())
				} else {
					m.makeFormPlan()
				}
				break
			}
			var cmd tea.Cmd
			if m.source.Focused() {
				m.source, cmd = m.source.Update(v)
			} else {
				m.target, cmd = m.target.Update(v)
			}
			cmds = append(cmds, cmd)
			break
		}
		if m.mode == planMode {
			if strings.EqualFold(v.String(), "y") {
				ctx, cancel := context.WithCancel(context.Background())
				m.applying = true
				m.applyCancel = cancel
				m.progress = &progressTracker{}
				m.status = "Starting apply… · q/esc stops waiting locally"
				cmds = append(cmds, m.spin.Tick, m.apply(ctx))
			}
			if key.Matches(v, m.keys.cancel) {
				m.mode = listMode
				m.plan = planner.Plan{}
			}
			break
		}

		if m.redirects.SettingFilter() {
			var cmd tea.Cmd
			m.redirects, cmd = m.redirects.Update(v)
			cmds = append(cmds, cmd)
			break
		}
		switch {
		case key.Matches(v, m.keys.add):
			m.editing = false
			m.source.SetValue("")
			m.target.SetValue("")
			m.target.Blur()
			m.mode = formMode
			cmds = append(cmds, m.source.Focus())
		case key.Matches(v, m.keys.edit):
			if item, ok := m.current(); ok {
				m.editing = true
				m.source.SetValue(item.Source)
				m.target.SetValue(item.Target)
				m.target.Blur()
				m.mode = formMode
				cmds = append(cmds, m.source.Focus())
			}
		case key.Matches(v, m.keys.delete):
			if item, ok := m.current(); ok {
				p, err := planner.DeleteRedirect(m.items, item.ID)
				m.openPlan(p, err)
			}
		default:
			var cmd tea.Cmd
			m.redirects, cmd = m.redirects.Update(v)
			cmds = append(cmds, cmd)
		}
	}
	return m, tea.Batch(cmds...)
}

func (m *model) makeFormPlan() {
	var p planner.Plan
	var err error
	if m.editing {
		item, ok := m.current()
		if !ok {
			return
		}
		p, err = planner.EditRedirect(m.items, item.ID, m.source.Value(), m.target.Value())
	} else {
		p, err = planner.AddRedirect(m.items, domain.New(m.source.Value(), m.target.Value()))
	}
	m.openPlan(p, err)
}

func (m *model) openPlan(p planner.Plan, err error) {
	if err != nil {
		m.err = err
		return
	}
	m.err = nil
	m.plan = p
	m.mode = planMode
}

func (m model) current() (domain.Redirect, bool) {
	item, ok := m.redirects.SelectedItem().(redirectItem)
	if !ok {
		return domain.Redirect{}, false
	}
	return item.redirect, true
}

func (m *model) selectID(id string) {
	for index, item := range m.redirects.VisibleItems() {
		redirect, ok := item.(redirectItem)
		if ok && redirect.redirect.ID == id {
			m.redirects.Select(index)
			return
		}
	}
	m.redirects.ResetSelected()
}

func (m model) View() string {
	if m.width == 0 {
		return "Loading…"
	}
	header := titleStyle.Render("cf-redirect") + "  " + mutedStyle.Render("list "+safeTerminalText(m.listID))
	header = ansi.Truncate(header, max(1, m.width), "…")
	var body string
	switch m.mode {
	case formMode:
		action := "Add redirect"
		if m.editing {
			action = "Edit redirect"
		}
		body = titleStyle.Render(action) + "\n\n" + m.source.View() + "\n" + m.target.View() + "\n\n" + mutedStyle.Render("Enter advances/reviews · Esc cancels")
	case planMode:
		body = m.planView()
	default:
		body = m.redirects.View()
	}
	state := safeTerminalText(m.status)
	if m.applying && m.progress != nil {
		if progress := m.progress.get(); progress != "" {
			state = safeTerminalText(progress) + " · q/esc stops waiting locally"
		}
	}
	if m.loading || m.applying {
		state = m.spin.View() + " " + state
	}
	state = ansi.Truncate(state, max(1, m.width), "…")
	if m.err != nil {
		// Errors render as a dedicated block the user must acknowledge, so the
		// full message stays on screen instead of flashing past in the status.
		body = errorView(m.err, m.width)
	}
	return header + "\n" + mutedStyle.Render(state) + "\n\n" + body
}

// errorView renders an error the user can read at their own pace. It is shown
// until any key is pressed (see Update), so long Cloudflare messages no longer
// flash by.
func errorView(err error, width int) string {
	msg := ansi.Wrap(safeTerminalText(err.Error()), max(1, width-2), "")
	title := lipgloss.NewStyle().Foreground(red).Bold(true).Render("Error")
	block := lipgloss.NewStyle().Foreground(red).Render(msg)
	return title + "\n\n" + block + "\n\n" + mutedStyle.Render("Press any key to dismiss")
}

func (m model) planView() string {
	a, u, d := m.plan.Counts()
	var b strings.Builder
	title := fmt.Sprintf("Review plan  %d add · %d update · %d delete", a, u, d)
	b.WriteString(titleStyle.Render(ansi.Truncate(title, max(1, m.width), "…")) + "\n\n")
	for _, c := range m.plan.Changes {
		var line string
		var style lipgloss.Style
		switch c.Kind {
		case planner.Add:
			line = "+ " + c.After.Source + " → " + c.After.Target
			style = lipgloss.NewStyle().Foreground(green)
		case planner.Update:
			line = "~ " + c.Before.Source + " → " + c.Before.Target + "  =>  " + c.After.Source + " → " + c.After.Target
			style = lipgloss.NewStyle().Foreground(amber)
		case planner.Delete:
			line = "- " + c.Before.Source
			style = lipgloss.NewStyle().Foreground(red)
		}
		line = ansi.Wrap(safeTerminalText(line), max(1, m.width), "/?&=._")
		b.WriteString(style.Render(line))
		b.WriteByte('\n')
	}
	b.WriteString("\n" + mutedStyle.Render("Press y to apply this plan · Esc cancels"))
	return strings.TrimSuffix(b.String(), "\n")
}

func safeTerminalText(value string) string { return textsafe.StripControls(value) }

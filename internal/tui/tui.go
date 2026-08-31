// Package tui implements the focused interactive redirect editor.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/koopycat/cf-redirect/internal/app"
	"github.com/koopycat/cf-redirect/internal/domain"
	"github.com/koopycat/cf-redirect/internal/planner"
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

type mode int

const (
	listMode mode = iota
	searchMode
	formMode
	planMode
)

type keys struct{ up, down, search, add, edit, delete, enter, cancel, quit key.Binding }

func (k keys) ShortHelp() []key.Binding {
	return []key.Binding{k.search, k.add, k.edit, k.delete, k.quit}
}
func (k keys) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.up, k.down, k.search, k.add, k.edit, k.delete}, {k.enter, k.cancel, k.quit}}
}

func newKeys() keys {
	return keys{
		up: key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")), down: key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")), search: key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")), add: key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")), edit: key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")), delete: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")), enter: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "continue")), cancel: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")), quit: key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit"))}
}

type model struct {
	api               api
	accountID, listID string
	items             []domain.Redirect
	selected          int
	query             string
	mode              mode
	input             textinput.Model
	source, target    textinput.Model
	editing           bool
	plan              planner.Plan
	loading           bool
	applying          bool
	err               error
	status            string
	width, height     int
	spin              spinner.Model
	help              help.Model
	keys              keys
}

// Run starts the full-screen terminal interface.
func Run(client app.RedirectAPI, accountID, listID string) error {
	m := newModel(client, accountID, listID)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
func newModel(client api, accountID, listID string) model {
	input := textinput.New()
	input.Prompt = "Search: "
	input.CharLimit = 500
	source := textinput.New()
	source.Prompt = "Source URL: "
	source.CharLimit = 2048
	target := textinput.New()
	target.Prompt = "Target URL: "
	target.CharLimit = 2048
	s := spinner.New()
	s.Spinner = spinner.Line
	return model{api: client, accountID: accountID, listID: listID, input: input, source: source, target: target, spin: s, help: help.New(), keys: newKeys(), loading: true}
}
func (m model) Init() tea.Cmd { return tea.Batch(m.spin.Tick, m.load()) }
func (m model) load() tea.Cmd {
	return func() tea.Msg {
		items, err := m.api.ListItems(context.Background(), m.accountID, m.listID)
		return loadedMsg{items, err}
	}
}
func (m model) apply() tea.Cmd {
	plan := m.plan
	return func() tea.Msg {
		_, err := (app.Executor{API: m.api, AccountID: m.accountID, ListID: m.listID, PollInterval: 500 * time.Millisecond}).Apply(context.Background(), plan)
		return appliedMsg{err}
	}
}
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
		m.input.Width = max(20, v.Width-12)
		m.source.Width = max(20, v.Width-14)
		m.target.Width = max(20, v.Width-14)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(v)
		cmds = append(cmds, cmd)
	case loadedMsg:
		m.loading = false
		m.err = v.err
		if v.err == nil {
			m.items = v.items
			m.status = fmt.Sprintf("%d redirects", len(v.items))
			if m.selected >= len(m.items) {
				m.selected = max(0, len(m.items)-1)
			}
		}
	case appliedMsg:
		m.applying = false
		if v.err != nil {
			m.err = v.err
			m.status = "Apply failed"
		} else {
			m.err = nil
			m.status = "Plan applied"
			m.mode = listMode
			m.loading = true
			cmds = append(cmds, m.load())
		}
	case tea.KeyMsg:
		if m.applying {
			if key.Matches(v, m.keys.quit) {
				m.status = "Apply continues; wait for Cloudflare to finish"
			}
			break
		}
		if m.loading {
			if key.Matches(v, m.keys.quit) {
				return m, tea.Quit
			}
			break
		}
		if m.mode == searchMode {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(v)
			cmds = append(cmds, cmd)
			m.query = m.input.Value()
			m.clampSelection()
			if key.Matches(v, m.keys.cancel) || key.Matches(v, m.keys.enter) {
				m.mode = listMode
				m.input.Blur()
			}
			break
		}
		if m.mode == formMode {
			var cmd tea.Cmd
			if m.source.Focused() {
				m.source, cmd = m.source.Update(v)
			} else {
				m.target, cmd = m.target.Update(v)
			}
			cmds = append(cmds, cmd)
			if key.Matches(v, m.keys.enter) {
				if m.source.Focused() {
					m.source.Blur()
					m.target.Focus()
				} else {
					m.makeFormPlan()
				}
			}
			if key.Matches(v, m.keys.cancel) {
				m.mode = listMode
				m.source.Blur()
				m.target.Blur()
			}
			break
		}
		if m.mode == planMode {
			if strings.EqualFold(v.String(), "y") {
				m.applying = true
				m.status = "Applying plan; quitting is disabled until Cloudflare finishes"
				cmds = append(cmds, m.apply())
			}
			if key.Matches(v, m.keys.cancel) {
				m.mode = listMode
				m.plan = planner.Plan{}
			}
			break
		}
		if key.Matches(v, m.keys.quit) {
			return m, tea.Quit
		}
		if key.Matches(v, m.keys.up) && m.selected > 0 {
			m.selected--
		}
		if key.Matches(v, m.keys.down) && m.selected < len(m.filtered())-1 {
			m.selected++
		}
		if key.Matches(v, m.keys.search) {
			m.mode = searchMode
			m.input.SetValue(m.query)
			m.input.Focus()
		}
		if key.Matches(v, m.keys.add) {
			m.editing = false
			m.source.SetValue("")
			m.target.SetValue("")
			m.mode = formMode
			m.source.Focus()
		}
		if key.Matches(v, m.keys.edit) {
			if item, ok := m.current(); ok {
				m.editing = true
				m.source.SetValue(item.Source)
				m.target.SetValue(item.Target)
				m.mode = formMode
				m.source.Focus()
			}
		}
		if key.Matches(v, m.keys.delete) {
			if item, ok := m.current(); ok {
				p, err := planner.DeleteRedirect(m.items, item.ID)
				m.openPlan(p, err)
			}
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
func (m model) filtered() []domain.Redirect {
	if m.query == "" {
		return m.items
	}
	q := strings.ToLower(m.query)
	var r []domain.Redirect
	for _, i := range m.items {
		if strings.Contains(strings.ToLower(i.Source), q) || strings.Contains(strings.ToLower(i.Target), q) || strings.Contains(strings.ToLower(i.Comment), q) {
			r = append(r, i)
		}
	}
	return r
}
func (m *model) clampSelection() {
	count := len(m.filtered())
	if count == 0 {
		m.selected = 0
	} else if m.selected >= count {
		m.selected = count - 1
	} else if m.selected < 0 {
		m.selected = 0
	}
}

func (m model) current() (domain.Redirect, bool) {
	items := m.filtered()
	if m.selected < 0 || m.selected >= len(items) {
		return domain.Redirect{}, false
	}
	return items[m.selected], true
}
func (m model) View() string {
	if m.width == 0 {
		return "Loading…"
	}
	header := titleStyle.Render("cf-redirect") + "  " + mutedStyle.Render("list "+m.listID)
	var body string
	switch m.mode {
	case searchMode:
		body = m.input.View() + "\n\n" + m.listView()
	case formMode:
		action := "Add redirect"
		if m.editing {
			action = "Edit redirect"
		}
		body = titleStyle.Render(action) + "\n\n" + m.source.View() + "\n" + m.target.View() + "\n\n" + mutedStyle.Render("Enter advances/reviews · Esc cancels")
	case planMode:
		body = m.planView()
	default:
		body = m.listView()
	}
	state := m.status
	if m.loading || m.applying {
		state = m.spin.View() + " " + state
	}
	if m.err != nil {
		state = lipgloss.NewStyle().Foreground(red).Render("Error: " + safeTerminalText(m.err.Error()))
	}
	return header + "\n" + mutedStyle.Render(state) + "\n\n" + body + "\n\n" + m.help.View(m.keys)
}
func (m model) listView() string {
	items := m.filtered()
	if len(items) == 0 {
		return mutedStyle.Render("No redirects match.")
	}
	var b strings.Builder
	for n, item := range items {
		line := item.Source + "  " + mutedStyle.Render("→") + "  " + item.Target
		if m.width < 75 {
			line = item.Source + "\n  " + mutedStyle.Render("→ ") + item.Target
		}
		if n == m.selected {
			line = selectedStyle.Render("› " + line)
		} else {
			line = "  " + line
		}
		b.WriteString(line)
		if item.Comment != "" {
			b.WriteString("  " + mutedStyle.Render(safeTerminalText(item.Comment)))
		}
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}
func (m model) planView() string {
	a, u, d := m.plan.Counts()
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf("Review plan  %d add · %d update · %d delete", a, u, d)) + "\n\n")
	for _, c := range m.plan.Changes {
		switch c.Kind {
		case planner.Add:
			b.WriteString(lipgloss.NewStyle().Foreground(green).Render("+ " + c.After.Source + " → " + c.After.Target))
		case planner.Update:
			b.WriteString(lipgloss.NewStyle().Foreground(amber).Render("~ " + c.Before.Source + " → " + c.Before.Target + "  =>  " + c.After.Source + " → " + c.After.Target))
		case planner.Delete:
			b.WriteString(lipgloss.NewStyle().Foreground(red).Render("- " + c.Before.Source))
		}
		b.WriteByte('\n')
	}
	b.WriteString("\n" + mutedStyle.Render("Press y to apply this plan · Esc cancels"))
	return strings.TrimSuffix(b.String(), "\n")
}
func safeTerminalText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

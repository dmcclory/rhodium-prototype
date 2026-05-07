// Package status renders the modal for setting a custom review status on a
// PR. Opened from any list view via `S`; submission writes to the brain and
// emits StatusSetMsg. The modal lives at app level so the same key works
// from todo and all-PRs alike.
package status

import (
	"fmt"
	"strings"

	"rhodium/internal/gh"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Model is the modal's state. Active is exported so the app can flip it in
// its own routing.
type Model struct {
	Active     bool
	pr         *gh.PR
	current    string
	input      textinput.Model
	statuses   []string
	idx        int
	mode       string // "cycle" | "input" — starts in cycle mode
	inflight   bool
}

// StatusSetMsg lands back on the update loop after the user confirms a status.
type StatusSetMsg struct {
	Repo   string
	PRNum  int
	Status string
}

func New(statuses []string) Model {
	ti := textinput.New()
	ti.Placeholder = "type custom status…"
	ti.Width = 30
	return Model{statuses: statuses, input: ti, mode: "cycle"}
}

var boxStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("63")).
	Padding(1, 2)

// OpenFor captures pr and current status, focuses the cycle mode.
func (m *Model) OpenFor(pr gh.PR, currentStatus string) {
	m.pr = &pr
	m.current = currentStatus
	m.idx = 0
	m.mode = "cycle"
	m.input.SetValue("")
	m.input.Blur()
	m.Active = true
	// Find current status in the list.
	for i, s := range m.statuses {
		if s == currentStatus {
			m.idx = i
			break
		}
	}
}

// Render returns the bordered modal box.
func (m *Model) Render() string {
	if m.pr == nil {
		return ""
	}
	prLabel := fmt.Sprintf("%s#%d", m.pr.Repo, m.pr.Number)
	header := lipgloss.NewStyle().Bold(true).Render("Status: " + prLabel)

	var body string
	if m.mode == "cycle" {
		// Show the configured statuses as a cycle.
		var items []string
		for i, s := range m.statuses {
			prefix := "  "
			if i == m.idx {
				prefix = "▸ "
			}
			suffix := ""
			if s == m.current {
				suffix = " (current)"
			}
			items = append(items, prefix+s+suffix)
		}
		body = strings.Join(items, "\n") + "\n"
		body += lipgloss.NewStyle().Faint(true).Render("up/down: cycle   tab: type custom   enter: set   esc: cancel   clear: empty")
	} else {
		body = m.input.View() + "\n"
		body += lipgloss.NewStyle().Faint(true).Render("enter: set   esc: cancel   (empty = clear)")
	}
	return boxStyle.Render(header + "\n\n" + body)
}

func (m *Model) Footer() string {
	if m.mode == "cycle" {
		return "status picker — up/down: cycle   tab: type custom   enter: set   esc: cancel"
	}
	return "status picker — type status   enter: set   esc: cancel"
}

func (m *Model) Update(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.Active = false
		return nil
	case "enter":
		status := m.selectedStatus()
		m.Active = false
		return func() tea.Msg {
			return StatusSetMsg{
				Repo:   m.pr.Repo,
				PRNum:  m.pr.Number,
				Status: status,
			}
		}
	case "clear":
		// Clear the status.
		m.Active = false
		return func() tea.Msg {
			return StatusSetMsg{
				Repo:   m.pr.Repo,
				PRNum:  m.pr.Number,
				Status: "",
			}
		}
	case "tab":
		if m.mode == "cycle" {
			m.mode = "input"
			return m.input.Focus()
		}
		// Switch back to cycle mode.
		m.mode = "cycle"
		m.input.Blur()
		return nil
	case "up", "k":
		if m.mode == "cycle" && len(m.statuses) > 0 {
			m.idx--
			if m.idx < 0 {
				m.idx = len(m.statuses) - 1
			}
		}
		return nil
	case "down", "j":
		if m.mode == "cycle" && len(m.statuses) > 0 {
			m.idx++
			if m.idx >= len(m.statuses) {
				m.idx = 0
			}
		}
		return nil
	}
	if m.mode == "input" {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return cmd
	}
	return nil
}

func (m *Model) selectedStatus() string {
	if m.mode == "input" {
		return strings.TrimSpace(m.input.Value())
	}
	if m.idx >= 0 && m.idx < len(m.statuses) {
		return m.statuses[m.idx]
	}
	return ""
}

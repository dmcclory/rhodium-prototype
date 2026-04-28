// Package review renders the bordered modal that submits a GitHub PR review
// (APPROVE / REQUEST_CHANGES / COMMENT). It is opened from any list view
// via `A`; submission POSTs to GitHub and emits SubmittedMsg. The modal
// lives at app level so the same key works from todo and all-PRs alike.
package review

import (
	"fmt"
	"strings"

	"rhodium/internal/gh"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Model is the modal's state. Active is exported so the app can flip it in
// its own routing (e.g., when checking whether to dispatch keys here).
type Model struct {
	Active bool

	event    gh.ReviewEvent
	body     textarea.Model
	pr       *gh.PR
	inflight bool
}

// SubmittedMsg lands back on the update loop after gh.SubmitReview returns.
// The app surfaces a status line based on Err.
type SubmittedMsg struct {
	Repo  string
	PRNum int
	Event gh.ReviewEvent
	Err   error
}

// StatusMsg is the modal's bridge to the app footer. Emitted instead of
// poking app state directly so this package stays unaware of app internals.
type StatusMsg struct{ Text string }

func New() Model {
	ti := textarea.New()
	ti.Placeholder = "Review summary (optional for APPROVE, required otherwise)"
	ti.SetHeight(4)
	ti.ShowLineNumbers = false
	return Model{event: gh.ReviewApprove, body: ti}
}

var boxStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("63")).
	Padding(1, 2)

// OpenFor captures pr and focuses the body textarea. Callers in any view
// invoke this to bring up the modal.
func (m *Model) OpenFor(pr gh.PR) tea.Cmd {
	m.pr = &pr
	m.event = gh.ReviewApprove
	m.body.Reset()
	m.Active = true
	return m.body.Focus()
}

// Render returns the bordered modal box; positioning over the active view
// is done by the caller.
func (m *Model) Render() string {
	prLabel := "(no PR)"
	if m.pr != nil {
		prLabel = fmt.Sprintf("%s#%d — %s", m.pr.Repo, m.pr.Number, m.pr.Title)
	}
	status := ""
	if m.inflight {
		status = "  (submitting…)"
	}
	header := lipgloss.NewStyle().Bold(true).Render("Review: "+prLabel) + "\n"
	event := fmt.Sprintf("Event: [%s]   (tab cycles: APPROVE → REQUEST_CHANGES → COMMENT)%s", m.event, status)
	hints := lipgloss.NewStyle().Faint(true).Render("ctrl+s: submit   esc: cancel")
	return boxStyle.Render(header + event + "\n\n" + m.body.View() + "\n" + hints)
}

// Footer text shown while this modal is open.
func (m *Model) Footer() string {
	return "review modal — tab: cycle event   ctrl+s: submit   esc: cancel"
}

// Update routes keys while the modal is open. The body textarea consumes
// everything except the control keys handled here.
func (m *Model) Update(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.Active = false
		m.body.Blur()
		return nil
	case "tab":
		switch m.event {
		case gh.ReviewApprove:
			m.event = gh.ReviewRequestChanges
		case gh.ReviewRequestChanges:
			m.event = gh.ReviewComment
		default:
			m.event = gh.ReviewApprove
		}
		return nil
	case "ctrl+s":
		return m.submit()
	}
	var cmd tea.Cmd
	m.body, cmd = m.body.Update(msg)
	return cmd
}

// submit validates and fires gh.SubmitReview asynchronously. GitHub rejects
// an empty body with REQUEST_CHANGES or COMMENT, so we guard that locally.
func (m *Model) submit() tea.Cmd {
	if m.pr == nil {
		return statusCmd("review: no PR captured")
	}
	body := strings.TrimSpace(m.body.Value())
	if body == "" && m.event != gh.ReviewApprove {
		return statusCmd(fmt.Sprintf("review: %s requires a body", m.event))
	}
	pr := *m.pr
	event := m.event
	m.Active = false
	m.body.Blur()
	return tea.Batch(
		statusCmd(fmt.Sprintf("submitting %s on %s#%d…", event, pr.Repo, pr.Number)),
		func() tea.Msg {
			err := gh.SubmitReview(pr.Repo, pr.Number, event, body)
			return SubmittedMsg{Repo: pr.Repo, PRNum: pr.Number, Event: event, Err: err}
		},
	)
}

func statusCmd(text string) tea.Cmd {
	return func() tea.Msg { return StatusMsg{Text: text} }
}

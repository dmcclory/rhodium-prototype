// Package merge renders the bordered modal that performs PUT
// /pulls/:n/merge (squash / merge / rebase). Shape mirrors the review
// modal — tab cycles the merge method, ctrl+s fires, esc cancels. Lives
// at app level so the same `M` key works from todo and all-PRs alike.
package merge

import (
	"fmt"

	"rhodium/internal/gh"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Model is the modal's state. Active is exported so the app can flip it in
// its own routing (e.g., when checking whether to dispatch keys here).
type Model struct {
	Active bool

	method   gh.MergeMethod
	body     textarea.Model
	pr       *gh.PR
	inflight bool
}

// SubmittedMsg lands back on the update loop after gh.MergePR returns.
type SubmittedMsg struct {
	Repo   string
	PRNum  int
	Method gh.MergeMethod
	Err    error
}

// StatusMsg is the modal's bridge to the app footer.
type StatusMsg struct{ Text string }

func New() Model {
	ti := textarea.New()
	ti.Placeholder = "Commit message (optional — GitHub fills from PR body for squash)"
	ti.SetHeight(4)
	ti.ShowLineNumbers = false
	return Model{method: gh.MergeSquash, body: ti}
}

var boxStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("205")).
	Padding(1, 2)

// OpenFor captures pr and initializes method from the supplied default
// (which the app reads from config).
func (m *Model) OpenFor(pr gh.PR, defaultMethod gh.MergeMethod) tea.Cmd {
	m.pr = &pr
	m.method = defaultMethod
	m.body.Reset()
	m.Active = true
	return m.body.Focus()
}

func (m *Model) Render() string {
	prLabel := "(no PR)"
	if m.pr != nil {
		prLabel = fmt.Sprintf("%s#%d — %s", m.pr.Repo, m.pr.Number, m.pr.Title)
	}
	status := ""
	if m.inflight {
		status = "  (merging…)"
	}
	header := lipgloss.NewStyle().Bold(true).Render("Merge: "+prLabel) + "\n"
	method := fmt.Sprintf("Method: [%s]   (tab cycles: squash → merge → rebase)%s", m.method, status)
	hints := lipgloss.NewStyle().Faint(true).Render("ctrl+s: merge   esc: cancel")
	return boxStyle.Render(header + method + "\n\n" + m.body.View() + "\n" + hints)
}

func (m *Model) Footer() string {
	return "merge modal — tab: cycle method   ctrl+s: merge   esc: cancel"
}

func (m *Model) Update(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.Active = false
		m.body.Blur()
		return nil
	case "tab":
		switch m.method {
		case gh.MergeSquash:
			m.method = gh.MergeMerge
		case gh.MergeMerge:
			m.method = gh.MergeRebase
		default:
			m.method = gh.MergeSquash
		}
		return nil
	case "ctrl+s":
		return m.submit()
	}
	var cmd tea.Cmd
	m.body, cmd = m.body.Update(msg)
	return cmd
}

func (m *Model) submit() tea.Cmd {
	if m.pr == nil {
		return statusCmd("merge: no PR captured")
	}
	pr := *m.pr
	method := m.method
	message := m.body.Value()
	m.Active = false
	m.body.Blur()
	return tea.Batch(
		statusCmd(fmt.Sprintf("merging %s on %s#%d…", method, pr.Repo, pr.Number)),
		func() tea.Msg {
			err := gh.MergePR(pr.Repo, pr.Number, method, message)
			return SubmittedMsg{Repo: pr.Repo, PRNum: pr.Number, Method: method, Err: err}
		},
	)
}

func statusCmd(text string) tea.Cmd {
	return func() tea.Msg { return StatusMsg{Text: text} }
}

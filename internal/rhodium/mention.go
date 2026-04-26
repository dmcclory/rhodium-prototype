package rhodium

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// mentionPicker is a small modal shown over the noting textarea when the
// user types `@` at a word boundary. It lists the repo's contributors
// (top-contributors first); subsequent keystrokes both extend the note text
// and filter the list against `query`. Pressing enter replaces the typed
// `@query` fragment with `@login ` and closes the modal.
//
// Contributors are fetched lazily on first open and cached on the app —
// see app.contributors. A picker opened before the fetch returns shows
// "loading…" until contributorsLoadedMsg lands.
type mentionPicker struct {
	open    bool
	loading bool
	query   string
	list    list.Model
}

type mentionItem struct {
	login         string
	contributions int
}

func (m mentionItem) Title() string {
	return fmt.Sprintf("@%s", m.login)
}
func (m mentionItem) Description() string {
	return fmt.Sprintf("%d contributions", m.contributions)
}
func (m mentionItem) FilterValue() string { return m.login }

func newMentionPicker() mentionPicker {
	l := list.New(nil, compactDelegate(), 0, 0)
	l.SetShowHelp(false)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.Title = ""
	return mentionPicker{list: l}
}

// filterContributors returns items whose login contains query as a
// case-insensitive substring. An empty query matches everything.
func filterContributors(contribs []Contributor, query string) []list.Item {
	q := strings.ToLower(query)
	items := make([]list.Item, 0, len(contribs))
	for _, c := range contribs {
		if q == "" || strings.Contains(strings.ToLower(c.Login), q) {
			items = append(items, mentionItem{login: c.Login, contributions: c.Contributions})
		}
	}
	return items
}

var mentionBoxStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("63")).
	Padding(0, 1)

// Render returns the bordered picker box. Positioning is the caller's job
// (see diffView.View).
func (m *mentionPicker) Render() string {
	if m.loading {
		return mentionBoxStyle.Render("@-mention — loading contributors…")
	}
	return mentionBoxStyle.Render(m.list.View())
}

// loadContributorsCmd kicks off an async contributors fetch. Results land
// as contributorsLoadedMsg and are stashed on app.contributors.
func loadContributorsCmd(repo string) tea.Cmd {
	return func() tea.Msg {
		c, err := listContributors(repo)
		return contributorsLoadedMsg{repo: repo, contributors: c, err: err}
	}
}

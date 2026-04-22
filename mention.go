package main

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// mentionPicker is a small modal shown over the noting textarea when the
// user presses ctrl+a. It lists the repo's contributors (top-contributors
// first) with bubbles' built-in filter; pressing enter inserts `@login ` at
// the textarea's cursor and closes the modal.
//
// Contributors are fetched lazily on first open and cached on the app —
// see app.contributors. A picker opened before the fetch returns shows
// "loading…" until contributorsLoadedMsg lands.
type mentionPicker struct {
	open    bool
	loading bool
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
	l.SetFilteringEnabled(true)
	l.Title = ""
	return mentionPicker{list: l}
}

func contributorsToItems(contribs []Contributor) []list.Item {
	items := make([]list.Item, 0, len(contribs))
	for _, c := range contribs {
		items = append(items, mentionItem{login: c.Login, contributions: c.Contributions})
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

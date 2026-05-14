// Package files renders the per-PR files view: a list of changed files
// grouped by review status (in-progress / unseen / seen) plus a tab that
// shows the active notes for the PR. The package owns its rendering and
// key bindings; the app supplies pre-built items, description, and notes
// content (which require brain/cache lookups).
package files

import (
	"fmt"
	"strings"

	"rhodium/internal/brain"
	"rhodium/internal/gh"
	"rhodium/internal/tui/keys"
	"rhodium/internal/tui/prrow"
	"rhodium/internal/tui/router"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const descPaneHeight = 8

// --- typed action messages ---
//
// The view emits these instead of calling app methods directly. The app's
// Update loop handles them — keeps this package independent of app.

// OpenFileMsg requests the app open the given file in the diff view.
type OpenFileMsg struct{ File gh.FileChange }

// MarkFullyReviewedMsg requests the app call Brain.MarkFullyReviewed for
// the currently-open PR. Emitted by the files view's `M` binding, carrying
// the file paths so the app doesn't need to re-fetch or cache-lookup.
type MarkFullyReviewedMsg struct{ Paths []string }

// OpenCommentsMsg requests the app open the PR-level comments view.
type OpenCommentsMsg struct{}

// RebuildNotesMsg requests the app push fresh notes-tab content via
// SetNotes. Emitted when the user switches to the notes tab so notes
// content is built on demand rather than on every file rebuild.
type RebuildNotesMsg struct{}

// --- Item ---

// Item is one row in the file list. The app populates these via Rebuild
// so this package stays unaware of brain state.
type Item struct {
	File            gh.FileChange
	Status          brain.FileStatus
	NoteCount       int // local Rhodium notes
	GHCommentCount  int // GitHub inline comments from other people
	NeedsCatchUp    bool // PR head moved since this file was last reviewed
}

func (i Item) Title() string {
	s := fmt.Sprintf("%s %s  +%d -%d", i.Status.Glyph(), i.File.Path, i.File.Additions, i.File.Deletions)
	if i.NeedsCatchUp {
		s += "  ↻"
	}
	if i.NoteCount > 0 || i.GHCommentCount > 0 {
		parens := []string{}
		if i.NoteCount > 0 {
			parens = append(parens, fmt.Sprintf("%d %s", i.NoteCount, prrow.Pluralize("note", i.NoteCount)))
		}
		if i.GHCommentCount > 0 {
			parens = append(parens, fmt.Sprintf("%d %s", i.GHCommentCount, prrow.Pluralize("comment", i.GHCommentCount)))
		}
		s += fmt.Sprintf("  (%s)", strings.Join(parens, ", "))
	}
	return s
}
func (i Item) Description() string { return "" }
func (i Item) FilterValue() string { return i.File.Path }

// --- sectionItem ---

// sectionItem is a non-interactive header used to group rows. Action
// bindings ignore it via type assertion.
type sectionItem struct{ label string }

var sectionHeaderStyle = lipgloss.NewStyle().Faint(true).Bold(true)

func (s sectionItem) Title() string       { return sectionHeaderStyle.Render(s.label) }
func (s sectionItem) Description() string { return "" }
func (s sectionItem) FilterValue() string { return "" }

// --- tab ---

type tab int

const (
	tabFiles tab = iota
	tabNotes
)

// --- Model ---

// Model is the files view's state. The app sets BackRoute when entering
// so the back binding returns to whichever list the user came from, and
// AgentBindings so per-config agent actions appear alongside view bindings.
type Model struct {
	list   list.Model
	infoVP viewport.Model
	descVP viewport.Model
	tab    tab

	BackRoute     router.Route
	AgentBindings []keys.Binding
}

func New() Model {
	l := list.New(nil, compactDelegate(), 0, 0)
	l.Title = "Files"
	l.SetShowHelp(false)
	return Model{
		list:   l,
		infoVP: viewport.New(0, 0),
		descVP: viewport.New(0, 0),
	}
}

// Resize splits available height between the file list and description
// pane. Width is shared.
func (m *Model) Resize(w, totalH int) {
	descH := descPaneHeight
	filesH := totalH - descH - 2 // tab bar + separator
	if filesH < 4 {
		filesH = 4
		descH = totalH - filesH - 2
	}
	m.list.SetSize(w, filesH)
	m.descVP.Width = w
	m.descVP.Height = descH
	m.infoVP.Width = w
	m.infoVP.Height = totalH
}

func (m *Model) View() string {
	body := m.tabBar()
	switch m.tab {
	case tabFiles:
		body += m.list.View()
	case tabNotes:
		body += m.infoVP.View()
		return body
	}
	body += "\n" + lipgloss.NewStyle().Faint(true).Render(strings.Repeat("─", m.descVP.Width)) + "\n"
	body += m.descVP.View()
	return body
}

func (m *Model) Footer() string {
	return "1: files  2: notes  l/enter: open  C: comments  h/esc: back  q: quit"
}

// Update routes a key through this view's bindings, falling back to globals,
// then through the underlying list/viewport. globals is supplied by the app
// each call so this package stays unaware of app-flavored binding tables.
func (m *Model) Update(msg tea.Msg, globals []keys.Binding) tea.Cmd {
	key, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return m.delegate(msg)
	}
	filtering := m.list.FilterState() == list.Filtering
	if cmd, matched := keys.Dispatch(key.String(), filtering, m.Bindings(), globals); matched {
		return cmd
	}
	return m.delegate(msg)
}

func (m *Model) Bindings() []keys.Binding {
	return append([]keys.Binding{
		{
			Name: "back", Keys: []string{"esc", "h", "left"},
			Desc: "back", Group: "Navigate",
			Action: func() tea.Cmd {
				m.tab = tabFiles
				return router.Navigate(m.BackRoute)
			},
		},
		{
			Name: "open-file", Keys: []string{"enter", "l", "right"},
			Desc: "open selected file", Group: "Navigate",
			Action: func() tea.Cmd {
				if m.tab != tabFiles {
					return nil
				}
				it, ok := m.list.SelectedItem().(Item)
				if !ok {
					return nil
				}
				file := it.File
				return func() tea.Msg { return OpenFileMsg{File: file} }
			},
		},
		{
			Name: "tab-files", Keys: []string{"1"},
			Desc: "files tab", Group: "View",
			Action: func() tea.Cmd { m.tab = tabFiles; return nil },
		},
		{
			Name: "tab-notes", Keys: []string{"2"},
			Desc: "notes tab", Group: "View",
			Action: func() tea.Cmd {
				m.tab = tabNotes
				return func() tea.Msg { return RebuildNotesMsg{} }
			},
		},
		{
			Name: "comments", Keys: []string{"C"},
			Desc: "view PR comments", Group: "View",
			Action: func() tea.Cmd {
				return func() tea.Msg { return OpenCommentsMsg{} }
			},
		},
		{
			Name: "mark-fully-reviewed", Keys: []string{"M"},
			Desc: "mark PR reviewed", Group: "View",
			Action: func() tea.Cmd {
				return func() tea.Msg {
					var paths []string
					for _, it := range m.list.Items() {
						if fi, ok := it.(Item); ok {
							paths = append(paths, fi.File.Path)
						}
					}
					return MarkFullyReviewedMsg{Paths: paths}
				}
			},
		},
	}, m.AgentBindings...)
}

func (m *Model) delegate(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	if m.tab != tabFiles {
		m.infoVP, cmd = m.infoVP.Update(msg)
		return cmd
	}
	prev := m.list.Index()
	m.list, cmd = m.list.Update(msg)
	skipSectionHeaders(&m.list, prev)
	return cmd
}

// Filtering reports whether the list is in filter-input mode.
func (m *Model) Filtering() bool { return m.list.FilterState() == list.Filtering }

// SetTitle replaces the file-list title (e.g., "Files in repo#123").
func (m *Model) SetTitle(title string) { m.list.Title = title }

// ClearItems empties the file list — used when the app starts a fresh load
// and wants the previous PR's rows to disappear immediately.
func (m *Model) ClearItems() { m.list.SetItems(nil) }

// SetDescription sets the always-visible PR-description pane content.
func (m *Model) SetDescription(content string) {
	m.descVP.SetContent(content)
	m.descVP.GotoTop()
}

// SetNotes sets the notes-tab content. The app builds this on demand
// (in response to RebuildNotesMsg) and pushes it here.
func (m *Model) SetNotes(content string) {
	m.infoVP.SetContent(content)
	m.infoVP.GotoTop()
}

// Rebuild replaces the file list with the supplied items, splitting them
// into in-progress / unseen / seen sections. Selection is restored by path
// when possible.
func (m *Model) Rebuild(items []Item) {
	var savedPath string
	if sel, ok := m.list.SelectedItem().(Item); ok {
		savedPath = sel.File.Path
	}
	var unseen, partial, seen []Item
	for _, it := range items {
		switch it.Status {
		case brain.StatusUnseen:
			unseen = append(unseen, it)
		case brain.StatusPartial:
			partial = append(partial, it)
		case brain.StatusSeen:
			seen = append(seen, it)
		}
	}
	var listItems []list.Item
	needSep := false
	if len(partial) > 0 {
		listItems = append(listItems, sectionItem{label: "── in progress ──"})
		for _, it := range partial {
			listItems = append(listItems, it)
		}
		needSep = true
	}
	if len(unseen) > 0 {
		if needSep {
			listItems = append(listItems, sectionItem{label: "── unseen ──"})
		}
		for _, it := range unseen {
			listItems = append(listItems, it)
		}
		needSep = true
	}
	if len(seen) > 0 {
		if needSep {
			listItems = append(listItems, sectionItem{label: "── seen ──"})
		}
		for _, it := range seen {
			listItems = append(listItems, it)
		}
	}
	m.list.SetItems(listItems)
	if savedPath != "" {
		for i, it := range listItems {
			if fi, ok := it.(Item); ok && fi.File.Path == savedPath {
				m.list.Select(i)
				break
			}
		}
	}
}

// --- tab bar ---

var (
	tabActiveStyle   = lipgloss.NewStyle().Bold(true).Underline(true)
	tabInactiveStyle = lipgloss.NewStyle().Faint(true)
)

func (m *Model) tabBar() string {
	tabs := []struct {
		label string
		t     tab
	}{
		{"[1] Files", tabFiles},
		{"[2] Notes", tabNotes},
	}
	var parts []string
	for _, t := range tabs {
		if t.t == m.tab {
			parts = append(parts, tabActiveStyle.Render(t.label))
		} else {
			parts = append(parts, tabInactiveStyle.Render(t.label))
		}
	}
	return strings.Join(parts, "  ") + "\n"
}

// --- list helpers ---

func compactDelegate() list.DefaultDelegate {
	d := list.NewDefaultDelegate()
	d.ShowDescription = false
	d.SetSpacing(0)
	return d
}

// skipSectionHeaders nudges the cursor past non-interactive sectionItem
// headers. Direction is inferred from whether the index went up or down.
func skipSectionHeaders(l *list.Model, prevIdx int) {
	items := l.Items()
	cur := l.Index()
	if cur >= len(items) {
		return
	}
	if _, ok := items[cur].(sectionItem); !ok {
		return
	}
	dir := 1
	if cur < prevIdx {
		dir = -1
	}
	next := cur + dir
	if next >= 0 && next < len(items) {
		l.Select(next)
	}
}

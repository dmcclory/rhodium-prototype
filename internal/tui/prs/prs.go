// Package prs renders the all-PRs view: every cached PR grouped into
// "mine" / "in progress" / "new" sections. The package owns its
// rendering and key bindings; the app builds Items (which require brain
// queries) and handles the typed action messages this view emits.
package prs

import (
	"fmt"
	"strings"

	"rhodium/internal/gh"
	"rhodium/internal/tui/keys"
	"rhodium/internal/tui/prrow"
	"rhodium/internal/tui/router"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- typed action messages ---

// OpenPRMsg requests the app open the given PR's files view.
type OpenPRMsg struct{ PR gh.PR }

// ReviewMsg requests the app open the review modal for PR.
type ReviewMsg struct{ PR gh.PR }

// MergeMsg requests the app open the merge modal for PR.
type MergeMsg struct{ PR gh.PR }

// CommentsMsg requests the app open the PR-level comments view, fetching
// comments first if needed.
type CommentsMsg struct{ PR gh.PR }

// ScrutinyToggleMsg requests the app flip the scrutiny flag for PR. The
// app reads current state, writes the inverse to the brain, and rebuilds
// — keeps brain mutation out of view code.
type ScrutinyToggleMsg struct{ PR gh.PR }

// OpenStatusMsg requests the app open the status picker modal for PR.
type OpenStatusMsg struct{ PR gh.PR }

// --- Item ---

// Item is one row in the PR list. The app populates these via Rebuild so
// this package stays unaware of brain state.
type Item struct {
	PR             gh.PR
	Summary        string // e.g. "3 new", "✓ caught up, ↻ 2/5"
	NoteCount      int
	GHCommentCount int
	Scrutinized    bool
	ReviewStatus   string // user-set custom status
	Cols           prrow.Cols // populated by Rebuild for column alignment
}

func (i Item) Title() string {
	var b strings.Builder
	if i.Cols.AnyScrutiny {
		if i.Scrutinized {
			b.WriteString("[S] ")
		} else {
			b.WriteString("    ")
		}
	}
	b.WriteString(prrow.PadRight(prrow.RepoNumStr(i.PR), i.Cols.RepoNum))
	b.WriteString("  ")
	b.WriteString(prrow.PadRight(prrow.RenderSystemStatus(i.PR), i.Cols.SysStatus))
	b.WriteString("  ")
	b.WriteString(prrow.PadRight(prrow.RenderReviewStatus(i.ReviewStatus), i.Cols.RevStatus))
	b.WriteString("  ")
	b.WriteString(prrow.PadRight(truncate(i.PR.Title, prrow.MaxTitleWidth), i.Cols.Title))
	b.WriteString("  ")
	b.WriteString(prrow.PadRight("@"+i.PR.Author, i.Cols.Author))
	var parts []string
	if i.Summary != "" {
		parts = append(parts, i.Summary)
	}
	if i.NoteCount > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", i.NoteCount, prrow.Pluralize("note", i.NoteCount)))
	}
	if i.GHCommentCount > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", i.GHCommentCount, prrow.Pluralize("comment", i.GHCommentCount)))
	}
	if len(parts) > 0 {
		b.WriteString("  (")
		b.WriteString(strings.Join(parts, ", "))
		b.WriteString(")")
	}
	return b.String()
}
func (i Item) Description() string { return "" }
func (i Item) FilterValue() string { return i.Title() }

// --- sectionItem ---

type sectionItem struct{ label string }

var sectionHeaderStyle = lipgloss.NewStyle().Faint(true).Bold(true)

func (s sectionItem) Title() string       { return sectionHeaderStyle.Render(s.label) }
func (s sectionItem) Description() string { return "" }
func (s sectionItem) FilterValue() string { return "" }

// --- Model ---

type Model struct {
	list list.Model
}

func New() Model {
	l := list.New(nil, compactDelegate(), 0, 0)
	l.SetShowHelp(false)
	return Model{list: l}
}

func (m *Model) Resize(w, h int) { m.list.SetSize(w, h) }

func (m *Model) View() string { return m.list.View() }

func (m *Model) Footer() string {
	return "l/enter: open  A: review  M: merge  C: comments  S: status  s: scrutiny  h/esc: back to todo  q: quit"
}

// Update routes a key through this view's bindings, falling back to globals,
// then through the underlying list. globals is supplied by the app each call
// so this package stays unaware of app-flavored binding tables.
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
	return []keys.Binding{
		{
			Name: "back", Keys: []string{"esc", "h", "left"},
			Desc: "back to todo", Group: "Navigate",
			Action: func() tea.Cmd {
				return router.Navigate(router.RouteTodo)
			},
		},
		{
			Name: "open-pr", Keys: []string{"enter", "l", "right"},
			Desc: "open selected PR", Group: "Navigate",
			Action: func() tea.Cmd {
				return m.emitSelected(func(pr gh.PR) tea.Msg { return OpenPRMsg{PR: pr} })
			},
		},
		{
			Name: "review", Keys: []string{"A"},
			Desc: "open review modal (approve / request-changes / comment)", Group: "View",
			Action: func() tea.Cmd {
				return m.emitSelected(func(pr gh.PR) tea.Msg { return ReviewMsg{PR: pr} })
			},
		},
		{
			Name: "merge", Keys: []string{"M"},
			Desc: "open merge modal (squash / merge / rebase)", Group: "View",
			Action: func() tea.Cmd {
				return m.emitSelected(func(pr gh.PR) tea.Msg { return MergeMsg{PR: pr} })
			},
		},
		{
			Name: "comments", Keys: []string{"C"},
			Desc: "view PR comments", Group: "View",
			Action: func() tea.Cmd {
				return m.emitSelected(func(pr gh.PR) tea.Msg { return CommentsMsg{PR: pr} })
			},
		},
		{
			Name: "status", Keys: []string{"S"},
			Desc: "set review status on PR", Group: "View",
			Action: func() tea.Cmd {
				return m.emitSelected(func(pr gh.PR) tea.Msg { return OpenStatusMsg{PR: pr} })
			},
		},
		{
			Name: "scrutiny", Keys: []string{"s"},
			Desc: "toggle scrutiny on selected PR", Group: "View",
			Action: func() tea.Cmd {
				return m.emitSelected(func(pr gh.PR) tea.Msg { return ScrutinyToggleMsg{PR: pr} })
			},
		},
	}
}

// emitSelected wraps the common pattern: read the selected Item, return a
// tea.Cmd that emits the action message — or nil if no Item is selected
// (e.g. cursor is on a section header).
func (m *Model) emitSelected(make func(gh.PR) tea.Msg) tea.Cmd {
	it, ok := m.list.SelectedItem().(Item)
	if !ok {
		return nil
	}
	return func() tea.Msg { return make(it.PR) }
}

func (m *Model) delegate(msg tea.Msg) tea.Cmd {
	prev := m.list.Index()
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	skipSectionHeaders(&m.list, prev)
	return cmd
}

// Filtering reports whether the list is in filter-input mode.
func (m *Model) Filtering() bool { return m.list.FilterState() == list.Filtering }

// SetTitle replaces the list title (e.g., "PRs (12, refreshing…)").
func (m *Model) SetTitle(title string) { m.list.Title = title }

// Rebuild replaces the list with the supplied items, partitioned into
// "mine" / "in progress" / "new" sections. Column widths are computed
// over the full set so columns line up across sections. Selection is
// restored by repo+number when possible.
//
// reviewStatuses maps pr_key → user-set status (may be nil).
func (m *Model) Rebuild(mine, inProgress, untouched []Item, reviewStatuses map[string]string) {
	var savedKey string
	if sel, ok := m.list.SelectedItem().(Item); ok {
		savedKey = prKey(sel.PR)
	}

	all := make([]Item, 0, len(mine)+len(inProgress)+len(untouched))
	all = append(all, mine...)
	all = append(all, inProgress...)
	all = append(all, untouched...)

	prs := make([]gh.PR, len(all))
	anyScrutiny := false
	for i, it := range all {
		prs[i] = it.PR
		if it.Scrutinized {
			anyScrutiny = true
		}
	}
	cols := prrow.ComputeCols(prs, anyScrutiny, reviewStatuses)
	setCols := func(items []Item) {
		for i := range items {
			items[i].Cols = cols
			items[i].ReviewStatus = reviewStatuses[prKey(items[i].PR)]
		}
	}
	setCols(mine)
	setCols(inProgress)
	setCols(untouched)

	var items []list.Item
	appendSection := func(label string, entries []Item) {
		if len(entries) == 0 {
			return
		}
		items = append(items, sectionItem{label: label})
		for _, it := range entries {
			items = append(items, it)
		}
	}
	appendSection("── mine ──", mine)
	appendSection("── in progress ──", inProgress)
	appendSection("── new ──", untouched)
	m.list.SetItems(items)
	if savedKey != "" {
		for i, it := range items {
			if pi, ok := it.(Item); ok && prKey(pi.PR) == savedKey {
				m.list.Select(i)
				break
			}
		}
	}
}

func prKey(pr gh.PR) string { return fmt.Sprintf("%s#%d", pr.Repo, pr.Number) }

// --- helpers ---

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

// truncate clips s to at most max bytes, replacing the tail with an
// ellipsis. Byte-based — preserves the legacy behavior of the prs view's
// title rendering. Multi-byte unicode in titles may render slightly off.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// Package todo renders the todo dashboard: a compact list of PRs with
// outstanding work, grouped into "needs attention" and "new" sections.
// The package owns its rendering and key bindings; the app is responsible
// for assembling the per-row data (which requires brain queries) and for
// handling the typed action messages this view emits.
package todo

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
//
// The view emits these instead of calling app methods directly. The app's
// Update loop handles them — keeps this package independent of app.

// OpenPRMsg requests the app open the given PR's files view.
type OpenPRMsg struct{ PR gh.PR }

// ReviewMsg requests the app open the review modal for PR.
type ReviewMsg struct{ PR gh.PR }

// MergeMsg requests the app open the merge modal for PR.
type MergeMsg struct{ PR gh.PR }

// CommentsMsg requests the app open the PR-level comments view, fetching
// comments first if needed.
type CommentsMsg struct{ PR gh.PR }

// OpenStatusMsg requests the app open the status picker modal for PR.
type OpenStatusMsg struct{ PR gh.PR }

// --- Item ---

// Item is one compact row in the todo dashboard. Tags are populated in a
// stable order ("in-progress", "catch-up", "unseen", "notes", "done") so
// suffix rendering is deterministic.
type Item struct {
	PR             gh.PR
	Tags           []string
	ReviewStatus   string // user-set custom status
	Done           int    // catch-up progress — files done
	Total          int    // catch-up total — files
	LinesDone      int    // catch-up progress — lines done
	LinesTotal     int    // catch-up total — lines
	Remaining      int    // unseen hunk count — ignored when files not loaded
	Notes          int    // notes attached to this PR
	NotesNow       int    // "now" urgency notes
	NotesSoon      int    // "soon" urgency notes
	GHComments     int    // GitHub inline comments from other people
	Cols           prrow.Cols // populated by Rebuild for column alignment
}

func (i Item) Title() string {
	var suffix []string
	for _, t := range i.Tags {
		switch t {
		case "in-progress":
			if i.Remaining > 0 {
				suffix = append(suffix, fmt.Sprintf("%d new", i.Remaining))
			} else {
				suffix = append(suffix, "in progress")
			}
		case "catch-up":
			if i.LinesTotal > 0 {
				suffix = append(suffix, fmt.Sprintf("catch-up %d/%d files, %d/%d lines", i.Done, i.Total, i.LinesDone, i.LinesTotal))
			} else {
				suffix = append(suffix, fmt.Sprintf("catch-up %d/%d", i.Done, i.Total))
			}
		case "unseen":
			suffix = append(suffix, "unseen")
		case "notes":
			parts := []string{fmt.Sprintf("%d %s", i.Notes, prrow.Pluralize("note", i.Notes))}
			urgencyParts := []string{}
			if i.NotesNow > 0 {
				urgencyParts = append(urgencyParts, fmt.Sprintf("!%d", i.NotesNow))
			}
			if i.NotesSoon > 0 {
				urgencyParts = append(urgencyParts, fmt.Sprintf("·%d", i.NotesSoon))
			}
			if len(urgencyParts) > 0 {
				parts = append(parts, strings.Join(urgencyParts, " "))
			}
			if i.GHComments > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", i.GHComments, prrow.Pluralize("comment", i.GHComments)))
			}
			suffix = append(suffix, strings.Join(parts, ", "))
		case "done":
			suffix = append(suffix, "✓ done")
		case "comments":
			suffix = append(suffix, fmt.Sprintf("%d %s", i.GHComments, prrow.Pluralize("comment", i.GHComments)))
		}
	}
	var b strings.Builder
	b.WriteString(prrow.PadRight(prrow.RepoNumStr(i.PR), i.Cols.RepoNum))
	b.WriteString("  ")
	b.WriteString(prrow.PadRight(prrow.RenderSystemStatus(i.PR), i.Cols.SysStatus))
	b.WriteString("  ")
	b.WriteString(prrow.PadRight(prrow.RenderReviewStatus(i.ReviewStatus), i.Cols.RevStatus))
	b.WriteString("  ")
	b.WriteString(prrow.PadRight(prrow.TruncateDisplay(i.PR.Title, prrow.MaxTitleWidth), i.Cols.Title))
	b.WriteString("  ")
	b.WriteString(prrow.PadRight("@"+i.PR.Author, i.Cols.Author))
	if len(suffix) > 0 {
		b.WriteString("  [")
		b.WriteString(strings.Join(suffix, ", "))
		b.WriteString("]")
	}
	return b.String()
}
func (i Item) Description() string { return "" }
func (i Item) FilterValue() string { return i.Title() }

// --- sectionItem ---

// sectionItem is a non-interactive header used to group rows. Action
// bindings ignore it via type assertion.
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
	l.Title = "Todo"
	l.SetShowHelp(false)
	return Model{list: l}
}

func (m *Model) Resize(w, h int) { m.list.SetSize(w, h) }

func (m *Model) View() string { return m.list.View() }

func (m *Model) Footer() string {
	return "l/enter: open  A: review  M: merge  C: comments  S: status  a: all PRs  q: quit"
}

// Update routes a message through bindings (with globals as fallback) then
// through the underlying list. globals is supplied by the app each call so
// this package stays independent of app-flavored binding tables.
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
			Name: "all-prs", Keys: []string{"a"},
			Desc: "all PRs", Group: "Navigate",
			Action: func() tea.Cmd {
				return router.Navigate(router.RoutePRs)
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

// Filtering reports whether the list is currently in filter-input mode.
// Exposed so the app can chain filter awareness into its own logic if
// needed (today, only Update consults it).
func (m *Model) Filtering() bool { return m.list.FilterState() == list.Filtering }

// Rebuild replaces the list contents with the supplied actionable + new
// item buckets, computes shared column widths, and updates the title. The
// app supplies the data because building Item requires brain queries this
// package shouldn't have access to.
//
// reviewStatuses maps pr_key → user-set status (may be nil).
// Selection is restored by PR key when possible.
func (m *Model) Rebuild(actionable, newPRs []Item, outstandingCount int, reviewStatuses map[string]string) {
	var savedKey string
	if sel, ok := m.list.SelectedItem().(Item); ok {
		savedKey = prKey(sel.PR)
	}

	prs := make([]gh.PR, 0, len(actionable)+len(newPRs))
	for _, it := range actionable {
		prs = append(prs, it.PR)
	}
	for _, it := range newPRs {
		prs = append(prs, it.PR)
	}
	cols := prrow.ComputeCols(prs, false, reviewStatuses)
	for i := range actionable {
		actionable[i].Cols = cols
	}
	for i := range newPRs {
		newPRs[i].Cols = cols
	}
	// Stamp review statuses onto items.
	for i := range actionable {
		actionable[i].ReviewStatus = reviewStatuses[prKey(actionable[i].PR)]
	}
	for i := range newPRs {
		newPRs[i].ReviewStatus = reviewStatuses[prKey(newPRs[i].PR)]
	}

	var items []list.Item
	switch {
	case len(actionable) > 0:
		items = append(items, sectionItem{label: "── needs attention ──"})
		for _, it := range actionable {
			items = append(items, it)
		}
		if len(newPRs) > 0 {
			items = append(items, sectionItem{label: "── new ──"})
			for _, it := range newPRs {
				items = append(items, it)
			}
		}
	case len(newPRs) > 0:
		items = append(items, sectionItem{label: "── ✓ caught up — new PRs below ──"})
		for _, it := range newPRs {
			items = append(items, it)
		}
	default:
		items = append(items, sectionItem{label: "── ✓ nothing to do ──"})
	}

	m.list.SetItems(items)
	if outstandingCount == 0 {
		m.list.Title = "✓ All caught up"
	} else {
		m.list.Title = fmt.Sprintf("Todo (%d)", outstandingCount)
	}

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

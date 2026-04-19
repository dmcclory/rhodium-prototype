package main

import (
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
)

// view identifies which sub-model is currently focused.
type view int

const (
	viewTodo view = iota
	viewPRs
	viewFiles
	viewDiff
)

var appStyle = lipgloss.NewStyle().Padding(0, 1)

// sectionItem is a non-interactive header used to group list entries
// into "in progress" / "unseen" buckets. Enter/l handlers ignore it via
// type assertion.
type sectionItem struct{ label string }

var sectionHeaderStyle = lipgloss.NewStyle().Faint(true).Bold(true)

func (s sectionItem) Title() string       { return sectionHeaderStyle.Render(s.label) }
func (s sectionItem) Description() string { return "" }
func (s sectionItem) FilterValue() string { return "" }

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

func compactDelegate() list.DefaultDelegate {
	d := list.NewDefaultDelegate()
	d.ShowDescription = false
	d.SetSpacing(0)
	return d
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// hashLine wraps a single string as a single-line "+"-prefixed hunk body
// and runs it through the hunk hasher. Used for note line anchoring.
func hashLine(s string) string {
	return hashHunkBody([]string{"+" + s})
}

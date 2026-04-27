package rhodium

import (
	"strings"

	"rhodium/internal/diff"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// overlay composites fg on top of bg starting at column x, row y. Both are
// treated as ANSI-styled text — the x/ansi package slices on display width
// without shredding escape sequences. Rows/columns that fall outside bg are
// clipped; short bg lines are padded with spaces so the fg box still sits
// over a clean rectangle.
func overlay(bg, fg string, x, y int) string {
	bgLines := strings.Split(bg, "\n")
	fgLines := strings.Split(fg, "\n")

	fgW := 0
	for _, l := range fgLines {
		if w := ansi.StringWidth(l); w > fgW {
			fgW = w
		}
	}

	for i, fgLine := range fgLines {
		row := y + i
		if row < 0 || row >= len(bgLines) {
			continue
		}
		bgLine := bgLines[row]
		bgW := ansi.StringWidth(bgLine)
		if x > bgW {
			bgLine += strings.Repeat(" ", x-bgW)
			bgW = x
		}

		left := ansi.Truncate(bgLine, x, "")
		right := ""
		if x+fgW < bgW {
			right = ansi.TruncateLeft(bgLine, x+fgW, "")
		}

		fgPad := fgLine
		if w := ansi.StringWidth(fgLine); w < fgW {
			fgPad = fgLine + strings.Repeat(" ", fgW-w)
		}
		bgLines[row] = left + fgPad + right
	}
	return strings.Join(bgLines, "\n")
}

// view identifies which sub-model is currently focused.
type view int

const (
	viewTodo view = iota
	viewPRs
	viewFiles
	viewDiff
	viewComments
)

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
	return diff.HashHunkBody([]string{"+" + s})
}

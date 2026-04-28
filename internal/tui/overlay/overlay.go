// Package overlay composites a foreground rendering on top of a background.
// Used for app-level modals (help, review, merge) and view-level overlays
// (the diff view's @-mention picker over the note textarea).
package overlay

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Render composites fg on top of bg starting at column x, row y. Both are
// treated as ANSI-styled text — the x/ansi package slices on display width
// without shredding escape sequences. Rows/columns that fall outside bg
// are clipped; short bg lines are padded with spaces so the fg box still
// sits over a clean rectangle.
func Render(bg, fg string, x, y int) string {
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

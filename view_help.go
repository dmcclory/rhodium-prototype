package main

import (
	"fmt"
	"sort"
	"strings"

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

// helpOverlay is app-level UI state: when open, the main view is replaced
// with a centered modal listing bindings for the currently-active view plus
// the always-on global bindings. `?` toggles it open, `esc` or `?` closes.
type helpOverlay struct {
	open bool
}

var (
	helpBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63")).
			Padding(1, 2)

	helpTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("63")).
			MarginBottom(1)

	helpGroupStyle = lipgloss.NewStyle().
			Bold(true).
			Underline(true).
			MarginTop(1)

	helpKeyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("205")).
			Bold(true)

	helpFootStyle = lipgloss.NewStyle().
			Faint(true).
			MarginTop(1)
)

// Render returns the bordered help box alone — positioning/compositing over
// the background view is done by overlay() in app.View.
func (h *helpOverlay) Render(a *app) string {
	var bindings []Binding
	switch a.activeView {
	case viewTodo:
		bindings = a.todo.bindings(a)
	case viewPRs:
		bindings = a.prs.bindings(a)
	case viewFiles:
		bindings = a.files.bindings(a)
	case viewDiff:
		bindings = a.diff.bindings(a)
	}
	bindings = append(bindings, globalBindings()...)

	body := renderHelpBody(a, bindings)
	return helpBoxStyle.Render(body)
}

func renderHelpBody(a *app, bindings []Binding) string {
	viewLabel := map[view]string{
		viewTodo:  "Todo",
		viewPRs:   "All PRs",
		viewFiles: "Files",
		viewDiff:  "Diff",
	}[a.activeView]

	var b strings.Builder
	b.WriteString(helpTitleStyle.Render(fmt.Sprintf("Keys — %s view", viewLabel)))
	b.WriteByte('\n')

	groups := groupBy(bindings)
	for _, g := range groupOrder {
		entries, ok := groups[g]
		if !ok || len(entries) == 0 {
			continue
		}
		b.WriteString(helpGroupStyle.Render(g))
		b.WriteByte('\n')
		for _, e := range entries {
			fmt.Fprintf(&b, "  %-16s  %s\n", helpKeyStyle.Render(e.keys), e.desc)
		}
	}

	b.WriteString(helpFootStyle.Render("? / esc — close"))
	return b.String()
}

type helpEntry struct{ keys, desc string }

// groupBy collects bindings into display groups, deduplicating by Name so a
// binding that appears in both the view table and globals (shouldn't happen
// in practice, but cheap to guard) only renders once.
func groupBy(bindings []Binding) map[string][]helpEntry {
	seen := map[string]bool{}
	out := map[string][]helpEntry{}
	for _, b := range bindings {
		if b.Desc == "" {
			continue
		}
		if seen[b.Name] {
			continue
		}
		seen[b.Name] = true
		g := b.Group
		if g == "" {
			g = "Global"
		}
		out[g] = append(out[g], helpEntry{
			keys: prettyKeys(b.Keys),
			desc: b.Desc,
		})
	}
	for k := range out {
		sort.SliceStable(out[k], func(i, j int) bool { return out[k][i].desc < out[k][j].desc })
	}
	return out
}

// prettyKeys formats a binding's key list for display. Substitutes symbols
// for common Vim-style keys so the modal is easier to scan than raw strings
// like "shift+tab" or " ".
func prettyKeys(keys []string) string {
	repl := map[string]string{
		" ":         "space",
		"down":      "↓",
		"up":        "↑",
		"left":      "←",
		"right":     "→",
		"enter":     "↵",
		"tab":       "tab",
		"shift+tab": "⇧tab",
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		if r, ok := repl[k]; ok {
			out[i] = r
		} else {
			out[i] = k
		}
	}
	return strings.Join(out, " / ")
}

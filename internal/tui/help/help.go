// Package help renders the centered keys-help overlay. It owns no view-
// specific knowledge: the app composes the active view's bindings (plus
// globals) and a label, and Render returns the bordered help box.
// Compositing the box over the background is done by the caller.
package help

import (
	"fmt"
	"sort"
	"strings"

	"rhodium/internal/tui/keys"
	"rhodium/internal/tui/styles"

	"github.com/charmbracelet/lipgloss"
)

// Model is the help overlay's state. Open is exported so the app can flip
// it from its own keypress handling — `?` toggles open, `esc`/`q`/`ctrl+c`
// close.
type Model struct {
	Open bool
}

func New() Model { return Model{} }

var (
	groupStyle = lipgloss.NewStyle().
			Bold(true).
			Underline(true).
			MarginTop(1)

	keyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("205")).
			Bold(true)

	footStyle = lipgloss.NewStyle().
			Faint(true).
			MarginTop(1)
)

// Render returns the bordered help box. viewLabel names the active view in
// the title; bindings is the active view's bindings concatenated with the
// always-on globals (the app composes the slice so this package stays
// unaware of the view enum).
func (m *Model) Render(viewLabel string, bindings []keys.Binding) string {
	return styles.HelpBox.Render(renderBody(viewLabel, bindings))
}

func renderBody(viewLabel string, bindings []keys.Binding) string {
	var b strings.Builder
	b.WriteString(styles.HelpTitle.Render(fmt.Sprintf("Keys — %s view", viewLabel)))
	b.WriteByte('\n')

	groups := groupBy(bindings)
	for _, g := range keys.GroupOrder {
		entries, ok := groups[g]
		if !ok || len(entries) == 0 {
			continue
		}
		b.WriteString(groupStyle.Render(g))
		b.WriteByte('\n')
		for _, e := range entries {
			fmt.Fprintf(&b, "  %-16s  %s\n", keyStyle.Render(e.keys), e.desc)
		}
	}

	b.WriteString(footStyle.Render("? / esc — close"))
	return b.String()
}

type entry struct{ keys, desc string }

// groupBy collects bindings into display groups, deduplicating by Name so a
// binding that appears in both the view table and globals (shouldn't happen
// in practice, but cheap to guard) only renders once.
func groupBy(bindings []keys.Binding) map[string][]entry {
	seen := map[string]bool{}
	out := map[string][]entry{}
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
		out[g] = append(out[g], entry{
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
func prettyKeys(ks []string) string {
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
	out := make([]string, len(ks))
	for i, k := range ks {
		if r, ok := repl[k]; ok {
			out[i] = r
		} else {
			out[i] = k
		}
	}
	return strings.Join(out, " / ")
}

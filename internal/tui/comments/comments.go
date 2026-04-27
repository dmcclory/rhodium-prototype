// Package comments renders the read-only PR-level comments view: issue
// comments (the general "leave a comment" stream), reviews (with their
// APPROVED / CHANGES_REQUESTED / COMMENTED state), and inline review
// comments anchored to path:line. The fetch is kicked off when the PR is
// opened, so this view is normally instant; if the fetch hasn't returned
// yet it shows a "loading" placeholder.
//
// Routing: opened by `C` from the PR list, todo list, or files view; `h`/
// esc returns to whichever view it was opened from. The diff view doesn't
// open this view — inline comments are rendered next to local notes there.
package comments

import (
	"fmt"
	"strings"

	"rhodium/internal/gh"
	"rhodium/internal/tui/keys"
	"rhodium/internal/tui/router"
	"rhodium/internal/tui/styles"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Model is the comments view's state. ReturnTo records which route the user
// arrived from so the back binding takes them back where they were; the app
// sets it before navigating in.
type Model struct {
	vp       viewport.Model
	ReturnTo router.Route // RoutePRs / RouteTodo / RouteFiles
}

func New() Model {
	return Model{vp: viewport.New(0, 0)}
}

func (m *Model) Resize(w, h int) {
	m.vp.Width = w
	m.vp.Height = h
}

func (m *Model) View() string { return m.vp.View() }

func (m *Model) Footer() string {
	return "↑/↓: scroll  h/esc: back  q: quit"
}

// Update routes a key through this view's bindings, falling back to globals,
// then through the underlying viewport. globals is supplied by the app each
// call so this package stays unaware of app-flavored binding tables.
func (m *Model) Update(msg tea.Msg, globals []keys.Binding) tea.Cmd {
	if key, ok := msg.(tea.KeyMsg); ok {
		if cmd, matched := keys.Dispatch(key.String(), false, m.Bindings(), globals); matched {
			return cmd
		}
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return cmd
}

func (m *Model) Bindings() []keys.Binding {
	return []keys.Binding{
		{
			Name: "back", Keys: []string{"esc", "h", "left"},
			Desc: "back", Group: "Navigate",
			Action: func() tea.Cmd {
				return router.Navigate(m.ReturnTo)
			},
		},
	}
}

// Rebuild renders the cached comments for pr into the viewport. loaded=false
// renders a "loading…" placeholder; loaded=true with len(comments)==0 is the
// distinct "GitHub returned no comments" state.
func (m *Model) Rebuild(pr *gh.PR, comments []gh.Comment, loaded bool) {
	if pr == nil {
		m.vp.SetContent("(no PR selected)")
		return
	}
	header := lipgloss.NewStyle().Bold(true).Render(
		fmt.Sprintf("Comments on %s#%d", pr.Repo, pr.Number),
	)
	if !loaded {
		m.vp.SetContent(header + "\n\n(loading…)")
		return
	}
	if len(comments) == 0 {
		m.vp.SetContent(header + "\n\n(no comments yet)")
		return
	}

	var b strings.Builder
	b.WriteString(header)
	b.WriteString(fmt.Sprintf("  %s\n\n", lipgloss.NewStyle().Faint(true).Render(
		fmt.Sprintf("(%d)", len(comments)),
	)))
	for i, c := range comments {
		b.WriteString(renderComment(c))
		if i < len(comments)-1 {
			b.WriteByte('\n')
		}
	}
	m.vp.SetContent(b.String())
	m.vp.GotoTop()
}

// renderComment formats a single gh.Comment for the PR-level view. Header
// line carries the author, timestamp, and a per-type tag (review state,
// inline file:line, or "comment"); body is indented two spaces.
func renderComment(c gh.Comment) string {
	var tag string
	switch c.Type {
	case "review":
		switch c.State {
		case "APPROVED":
			tag = styles.StatusApproved.Render("APPROVED")
		case "CHANGES_REQUESTED":
			tag = styles.StatusChanges.Render("CHANGES_REQUESTED")
		case "DISMISSED":
			tag = lipgloss.NewStyle().Faint(true).Render("DISMISSED")
		default:
			tag = styles.StatusReview.Render("COMMENTED")
		}
	case "inline":
		tag = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Render(
			fmt.Sprintf("%s:%d", c.Path, c.Line),
		)
	default:
		tag = lipgloss.NewStyle().Faint(true).Render("comment")
	}
	header := fmt.Sprintf("@%s · %s · %s",
		lipgloss.NewStyle().Bold(true).Render(c.Author),
		lipgloss.NewStyle().Faint(true).Render(formatTime(c.CreatedAt)),
		tag,
	)
	body := strings.TrimRight(c.Body, "\n")
	if body == "" {
		body = lipgloss.NewStyle().Faint(true).Render("(no body)")
	}
	indented := "  " + strings.ReplaceAll(body, "\n", "\n  ")
	return header + "\n" + indented + "\n"
}

// formatTime trims a GitHub ISO8601 timestamp to "YYYY-MM-DD HH:MM" so
// rows stay compact. Falls back to the raw value if parsing fails.
func formatTime(s string) string {
	if len(s) >= 16 {
		return s[:10] + " " + s[11:16]
	}
	return s
}

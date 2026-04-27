package rhodium

import (
	"fmt"
	"rhodium/internal/brain"
	"rhodium/internal/gh"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- commentsView ---
//
// Read-only view of every comment GitHub has on the selected PR: issue
// comments (the general "leave a comment" stream), reviews (with their
// APPROVED / CHANGES_REQUESTED / COMMENTED state), and inline review
// comments anchored to path:line. The fetch is kicked off when the PR is
// opened, so this view is normally instant; if the fetch hasn't returned
// yet it shows a "loading" placeholder.
//
// Routing: opened by `C` from the PR list, todo list, or files view; `h`/
// esc returns to whichever view it was opened from. The diff view doesn't
// open this view — inline comments are rendered next to local notes there.

type commentsView struct {
	vp       viewport.Model
	returnTo view // viewPRs / viewTodo / viewFiles
}

func newCommentsView() commentsView {
	return commentsView{vp: viewport.New(0, 0)}
}

func (v *commentsView) Resize(w, h int) {
	v.vp.Width = w
	v.vp.Height = h
}

func (v *commentsView) View(a *app) string {
	return v.vp.View()
}

func (v *commentsView) Footer(a *app) string {
	return "↑/↓: scroll  h/esc: back  q: quit"
}

func (v *commentsView) Update(a *app, msg tea.Msg) tea.Cmd {
	if key, ok := msg.(tea.KeyMsg); ok {
		if cmd, matched := dispatch(a, key.String(), false, v.bindings(a), globalBindings()); matched {
			return cmd
		}
	}
	var cmd tea.Cmd
	v.vp, cmd = v.vp.Update(msg)
	return cmd
}

func (v *commentsView) bindings(a *app) []Binding {
	return []Binding{
		{
			Name: "back", Keys: []string{"esc", "h", "left"},
			Desc: "back", Group: "Navigate",
			Action: func(a *app) tea.Cmd {
				a.layout.focus(v.returnTo)
				return nil
			},
		},
	}
}

// rebuild renders the cached comments for the selected PR into the
// viewport. Called whenever the comments view is opened or new comments
// land while the view is active.
func (v *commentsView) rebuild(a *app) {
	if a.session.selectedPR == nil {
		v.vp.SetContent("(no PR selected)")
		return
	}
	pr := *a.session.selectedPR
	comments, ok := a.cache.prComments[brain.PRKey(pr.Repo, pr.Number)]

	header := lipgloss.NewStyle().Bold(true).Render(
		fmt.Sprintf("Comments on %s#%d", pr.Repo, pr.Number),
	)
	if !ok {
		v.vp.SetContent(header + "\n\n(loading…)")
		return
	}
	if len(comments) == 0 {
		v.vp.SetContent(header + "\n\n(no comments yet)")
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
	v.vp.SetContent(b.String())
	v.vp.GotoTop()
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
			tag = statusApprovedStyle.Render("APPROVED")
		case "CHANGES_REQUESTED":
			tag = statusChangesStyle.Render("CHANGES_REQUESTED")
		case "DISMISSED":
			tag = lipgloss.NewStyle().Faint(true).Render("DISMISSED")
		default:
			tag = statusReviewStyle.Render("COMMENTED")
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

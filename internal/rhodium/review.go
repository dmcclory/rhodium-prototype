package rhodium

import (
	"fmt"
	"rhodium/internal/gh"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// reviewModal is a small overlay that lets the reviewer submit a PR review
// (approve / request-changes / comment-only). The event type is cycled with
// tab so the most-common case (APPROVE, body-less) is just `A tab ctrl+s`
// — or even `A ctrl+s` since APPROVE is the default.
//
// The modal lives on app rather than any one view so it can be opened from
// either the Todo dashboard or the All-PRs list with the same `A` key.
type reviewModal struct {
	open     bool
	event    gh.ReviewEvent
	body     textarea.Model
	pr       *gh.PR // captured at open time so re-selects don't shift target
	inflight bool
}

func newReviewModal() reviewModal {
	ti := textarea.New()
	ti.Placeholder = "Review summary (optional for APPROVE, required otherwise)"
	ti.SetHeight(4)
	ti.ShowLineNumbers = false
	return reviewModal{event: gh.ReviewApprove, body: ti}
}

var reviewBoxStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("63")).
	Padding(1, 2)

// openReview captures pr and focuses the body textarea. Callers in any view
// invoke this to bring up the modal.
func (a *app) openReview(pr gh.PR) tea.Cmd {
	a.review.pr = &pr
	a.review.event = gh.ReviewApprove
	a.review.body.Reset()
	a.review.open = true
	return a.review.body.Focus()
}

// renderReviewModal returns the bordered modal box; positioning over the
// active view is done in app.View.
func (a *app) renderReviewModal() string {
	prLabel := "(no PR)"
	if a.review.pr != nil {
		prLabel = fmt.Sprintf("%s#%d — %s", a.review.pr.Repo, a.review.pr.Number, a.review.pr.Title)
	}
	status := ""
	if a.review.inflight {
		status = "  (submitting…)"
	}
	header := lipgloss.NewStyle().Bold(true).Render("Review: "+prLabel) + "\n"
	event := fmt.Sprintf("Event: [%s]   (tab cycles: APPROVE → REQUEST_CHANGES → COMMENT)%s", a.review.event, status)
	hints := lipgloss.NewStyle().Faint(true).Render("ctrl+s: submit   esc: cancel")
	return reviewBoxStyle.Render(header + event + "\n\n" + a.review.body.View() + "\n" + hints)
}

// updateReviewKeys routes keys while the review modal is open. The body
// textarea consumes everything except the control keys listed here.
func (a *app) updateReviewKeys(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		a.review.open = false
		a.review.body.Blur()
		return nil
	case "tab":
		// Cycle APPROVE → REQUEST_CHANGES → COMMENT → APPROVE.
		switch a.review.event {
		case gh.ReviewApprove:
			a.review.event = gh.ReviewRequestChanges
		case gh.ReviewRequestChanges:
			a.review.event = gh.ReviewComment
		default:
			a.review.event = gh.ReviewApprove
		}
		return nil
	case "ctrl+s":
		return a.submitReviewFromModal()
	}
	var cmd tea.Cmd
	a.review.body, cmd = a.review.body.Update(msg)
	return cmd
}

// submitReviewFromModal validates and fires gh.SubmitReview asynchronously.
// GitHub rejects an empty body with REQUEST_CHANGES or COMMENT, so we
// guard that locally rather than letting the round-trip error back.
func (a *app) submitReviewFromModal() tea.Cmd {
	if a.review.pr == nil {
		a.status.msg = "review: no PR captured"
		return nil
	}
	body := strings.TrimSpace(a.review.body.Value())
	if body == "" && a.review.event != gh.ReviewApprove {
		a.status.msg = fmt.Sprintf("review: %s requires a body", a.review.event)
		return nil
	}
	pr := *a.review.pr
	event := a.review.event
	a.review.inflight = true
	a.status.msg = fmt.Sprintf("submitting %s on %s#%d…", event, pr.Repo, pr.Number)
	// Close the modal immediately — the async result lands on the status line.
	a.review.open = false
	a.review.body.Blur()
	a.review.inflight = false
	return func() tea.Msg {
		err := gh.SubmitReview(pr.Repo, pr.Number, event, body)
		return reviewSubmittedMsg{repo: pr.Repo, prNum: pr.Number, event: event, err: err}
	}
}

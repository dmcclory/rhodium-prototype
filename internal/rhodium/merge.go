package rhodium

import (
	"fmt"
	"rhodium/internal/gh"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// mergeModal is the overlay for PUT /pulls/:n/merge. Shape mirrors
// reviewModal (review.go) — tab cycles the merge method, ctrl+s fires,
// esc cancels. Kept on *app so both the todo and all-PRs lists open it
// with the same `M` key.
type mergeModal struct {
	open     bool
	method   gh.MergeMethod
	body     textarea.Model
	pr       *gh.PR
	inflight bool
}

func newMergeModal() mergeModal {
	ti := textarea.New()
	ti.Placeholder = "Commit message (optional — GitHub fills from PR body for squash)"
	ti.SetHeight(4)
	ti.ShowLineNumbers = false
	return mergeModal{method: gh.MergeSquash, body: ti}
}

var mergeBoxStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("205")).
	Padding(1, 2)

// openMerge captures pr and initializes method from the config default.
func (a *app) openMerge(pr gh.PR) tea.Cmd {
	a.merge.pr = &pr
	a.merge.method = gh.MergeMethod(a.cfg.MergeMethodResolved())
	a.merge.body.Reset()
	a.merge.open = true
	return a.merge.body.Focus()
}

func (a *app) renderMergeModal() string {
	prLabel := "(no PR)"
	if a.merge.pr != nil {
		prLabel = fmt.Sprintf("%s#%d — %s", a.merge.pr.Repo, a.merge.pr.Number, a.merge.pr.Title)
	}
	status := ""
	if a.merge.inflight {
		status = "  (merging…)"
	}
	header := lipgloss.NewStyle().Bold(true).Render("Merge: "+prLabel) + "\n"
	method := fmt.Sprintf("Method: [%s]   (tab cycles: squash → merge → rebase)%s", a.merge.method, status)
	hints := lipgloss.NewStyle().Faint(true).Render("ctrl+s: merge   esc: cancel")
	return mergeBoxStyle.Render(header + method + "\n\n" + a.merge.body.View() + "\n" + hints)
}

func (a *app) updateMergeKeys(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		a.merge.open = false
		a.merge.body.Blur()
		return nil
	case "tab":
		switch a.merge.method {
		case gh.MergeSquash:
			a.merge.method = gh.MergeMerge
		case gh.MergeMerge:
			a.merge.method = gh.MergeRebase
		default:
			a.merge.method = gh.MergeSquash
		}
		return nil
	case "ctrl+s":
		return a.submitMergeFromModal()
	}
	var cmd tea.Cmd
	a.merge.body, cmd = a.merge.body.Update(msg)
	return cmd
}

func (a *app) submitMergeFromModal() tea.Cmd {
	if a.merge.pr == nil {
		a.status.msg = "merge: no PR captured"
		return nil
	}
	pr := *a.merge.pr
	method := a.merge.method
	message := a.merge.body.Value()
	a.status.msg = fmt.Sprintf("merging %s on %s#%d…", method, pr.Repo, pr.Number)
	a.merge.open = false
	a.merge.body.Blur()
	return func() tea.Msg {
		err := gh.MergePR(pr.Repo, pr.Number, method, message)
		return mergeSubmittedMsg{repo: pr.Repo, prNum: pr.Number, method: method, err: err}
	}
}

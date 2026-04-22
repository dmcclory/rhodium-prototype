package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- prsView ---

type prsView struct {
	list   list.Model
	review reviewModal
}

// reviewModal is a small overlay that lets the reviewer submit a PR review
// (approve / request-changes / comment-only). The event type is cycled with
// tab so the most-common case (APPROVE, body-less) is just `A tab ctrl+s`
// — or even `A ctrl+s` since APPROVE is the default.
type reviewModal struct {
	open     bool
	event    ReviewEvent
	body     textarea.Model
	pr       *PR // captured at open time so re-selects don't shift target
	inflight bool
}

func newReviewModal() reviewModal {
	ti := textarea.New()
	ti.Placeholder = "Review summary (optional for APPROVE, required otherwise)"
	ti.SetHeight(4)
	ti.ShowLineNumbers = false
	return reviewModal{event: ReviewApprove, body: ti}
}

func newPRsView() prsView {
	l := list.New(nil, compactDelegate(), 0, 0)
	l.SetShowHelp(false)
	return prsView{list: l, review: newReviewModal()}
}

func (v *prsView) Resize(w, h int) { v.list.SetSize(w, h) }

var reviewBoxStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("63")).
	Padding(1, 2)

func (v *prsView) View(a *app) string {
	body := v.list.View()
	if !v.review.open {
		return body
	}
	box := v.renderReviewModal()
	boxW := lipgloss.Width(box)
	boxH := lipgloss.Height(box)
	x := (a.width - boxW) / 2
	if x < 0 {
		x = 0
	}
	y := (a.height - boxH) / 2
	if y < 0 {
		y = 0
	}
	return overlay(body, box, x, y)
}

func (v *prsView) renderReviewModal() string {
	prLabel := "(no PR)"
	if v.review.pr != nil {
		prLabel = fmt.Sprintf("%s#%d — %s", v.review.pr.Repo, v.review.pr.Number, v.review.pr.Title)
	}
	status := ""
	if v.review.inflight {
		status = "  (submitting…)"
	}
	header := lipgloss.NewStyle().Bold(true).Render("Review: "+prLabel) + "\n"
	event := fmt.Sprintf("Event: [%s]   (tab cycles: APPROVE → REQUEST_CHANGES → COMMENT)%s", v.review.event, status)
	hints := lipgloss.NewStyle().Faint(true).Render("ctrl+s: submit   esc: cancel")
	return reviewBoxStyle.Render(header + event + "\n\n" + v.review.body.View() + "\n" + hints)
}

func (v *prsView) Footer(a *app) string {
	if v.review.open {
		return "review modal — tab: cycle event   ctrl+s: submit   esc: cancel"
	}
	return "l/enter: open  A: approve/review  s: scrutiny  h/esc: back to todo  q: quit"
}

func (v *prsView) Update(a *app, msg tea.Msg) tea.Cmd {
	if v.review.open {
		if key, ok := msg.(tea.KeyMsg); ok {
			return v.updateReviewKeys(a, key)
		}
		return nil
	}
	key, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return v.delegate(msg)
	}
	filtering := v.list.FilterState() == list.Filtering
	if cmd, matched := dispatch(a, key.String(), filtering, v.bindings(a), globalBindings()); matched {
		return cmd
	}
	return v.delegate(msg)
}

// updateReviewKeys routes keys while the review modal is open. The body
// textarea consumes everything except the control keys listed here.
func (v *prsView) updateReviewKeys(a *app, msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		v.review.open = false
		v.review.body.Blur()
		return nil
	case "tab":
		// Cycle APPROVE → REQUEST_CHANGES → COMMENT → APPROVE.
		switch v.review.event {
		case ReviewApprove:
			v.review.event = ReviewRequestChanges
		case ReviewRequestChanges:
			v.review.event = ReviewComment
		default:
			v.review.event = ReviewApprove
		}
		return nil
	case "ctrl+s":
		return v.submitReviewFromModal(a)
	}
	var cmd tea.Cmd
	v.review.body, cmd = v.review.body.Update(msg)
	return cmd
}

// submitReviewFromModal validates and fires submitReview asynchronously.
// GitHub rejects an empty body with REQUEST_CHANGES or COMMENT, so we
// guard that locally rather than letting the round-trip error back.
func (v *prsView) submitReviewFromModal(a *app) tea.Cmd {
	if v.review.pr == nil {
		a.statusMsg = "review: no PR captured"
		return nil
	}
	body := strings.TrimSpace(v.review.body.Value())
	if body == "" && v.review.event != ReviewApprove {
		a.statusMsg = fmt.Sprintf("review: %s requires a body", v.review.event)
		return nil
	}
	pr := *v.review.pr
	event := v.review.event
	v.review.inflight = true
	a.statusMsg = fmt.Sprintf("submitting %s on %s#%d…", event, pr.Repo, pr.Number)
	// Close the modal immediately — the async result lands on the status line.
	v.review.open = false
	v.review.body.Blur()
	v.review.inflight = false
	return func() tea.Msg {
		err := submitReview(pr.Repo, pr.Number, event, body)
		return reviewSubmittedMsg{repo: pr.Repo, prNum: pr.Number, event: event, err: err}
	}
}

func (v *prsView) bindings(a *app) []Binding {
	return []Binding{
		{
			Name: "back", Keys: []string{"esc", "h", "left"},
			Desc: "back to todo", Group: "Navigate",
			Action: func(a *app) tea.Cmd {
				a.activeView = viewTodo
				return nil
			},
		},
		{
			Name: "open-pr", Keys: []string{"enter", "l", "right"},
			Desc: "open selected PR", Group: "Navigate",
			Action: func(a *app) tea.Cmd {
				if it, ok := v.list.SelectedItem().(prItem); ok {
					return a.openPR(it.pr)
				}
				return nil
			},
		},
		{
			Name: "review", Keys: []string{"A"},
			Desc: "open review modal (approve / request-changes / comment)", Group: "View",
			Action: func(a *app) tea.Cmd {
				it, ok := v.list.SelectedItem().(prItem)
				if !ok {
					return nil
				}
				pr := it.pr
				v.review.pr = &pr
				v.review.event = ReviewApprove
				v.review.body.Reset()
				v.review.open = true
				return v.review.body.Focus()
			},
		},
		{
			Name: "scrutiny", Keys: []string{"s"},
			Desc: "toggle scrutiny on selected PR", Group: "View",
			Action: func(a *app) tea.Cmd {
				it, ok := v.list.SelectedItem().(prItem)
				if !ok {
					return nil
				}
				on := !it.scrutinized
				a.brain.SetScrutiny(it.pr.Repo, it.pr.Number, on)
				v.rebuild(a)
				if on {
					a.statusMsg = fmt.Sprintf("scrutiny ON for %s#%d — full diffs, no catch-up shortcuts", it.pr.Repo, it.pr.Number)
				} else {
					a.statusMsg = fmt.Sprintf("scrutiny OFF for %s#%d", it.pr.Repo, it.pr.Number)
				}
				return nil
			},
		},
	}
}

func (v *prsView) delegate(msg tea.Msg) tea.Cmd {
	prev := v.list.Index()
	var cmd tea.Cmd
	v.list, cmd = v.list.Update(msg)
	skipSectionHeaders(&v.list, prev)
	return cmd
}

// mergePRs appends PRs whose (repo, number) aren't already in a.allPRs
// and returns just the newly-added ones, so callers can kick off file
// prefetch without redundantly re-fetching PRs already loaded.
func mergePRs(a *app, prs []PR) []PR {
	seen := make(map[string]bool, len(a.allPRs))
	for _, p := range a.allPRs {
		seen[prKey(p.Repo, p.Number)] = true
	}
	var added []PR
	for _, p := range prs {
		k := prKey(p.Repo, p.Number)
		if seen[k] {
			continue
		}
		seen[k] = true
		a.allPRs = append(a.allPRs, p)
		added = append(added, p)
	}
	return added
}

func (v *prsView) rebuild(a *app) {
	var savedKey string
	if sel, ok := v.list.SelectedItem().(prItem); ok {
		savedKey = prKey(sel.pr.Repo, sel.pr.Number)
	}

	var inProgress, untouched []prItem
	for _, pr := range a.allPRs {
		it := prItem{pr: pr, noteCount: a.brain.NoteCountForPR(pr.Repo, pr.Number), scrutinized: a.brain.IsScrutinized(pr.Repo, pr.Number)}
		// A PR is "in progress" if the brain has any marks for it, even
		// before we've fetched its file list. This keeps already-touched
		// PRs from popping between buckets during startup prefetch.
		looked := a.brain.HasAnyMarks(pr.Repo, pr.Number)
		if files, ok := a.prFiles[prKey(pr.Repo, pr.Number)]; ok {
			unseen := a.brain.UnseenCount(pr.Repo, pr.Number, files)
			if unseen == 0 {
				it.summary = "✓ caught up"
			} else {
				it.summary = fmt.Sprintf("%d new", unseen)
			}
			if session := a.brain.ActiveSession(pr.Repo, pr.Number); session != nil {
				it.summary += fmt.Sprintf(", ↻ %d/%d", session.FilesDone, session.FilesTotal)
			} else {
				reviewedStates := a.brain.AllFileReviewedStates(pr.Repo, pr.Number)
				catchUpCount := 0
				for _, f := range files {
					if s := reviewedStates[f.Path]; s.HeadSHA != "" && (s.HeadSHA != pr.HeadSHA || s.BaseSHA != pr.BaseSHA) {
						catchUpCount++
					}
				}
				if catchUpCount > 0 {
					it.summary += fmt.Sprintf(", %d ↻", catchUpCount)
				}
			}
		}
		if looked {
			inProgress = append(inProgress, it)
		} else {
			untouched = append(untouched, it)
		}
	}

	var items []list.Item
	if len(inProgress) > 0 {
		items = append(items, sectionItem{label: "── in progress ──"})
		for _, it := range inProgress {
			items = append(items, it)
		}
	}
	if len(untouched) > 0 {
		if len(inProgress) > 0 {
			items = append(items, sectionItem{label: "── new ──"})
		}
		for _, it := range untouched {
			items = append(items, it)
		}
	}
	v.list.SetItems(items)
	if savedKey != "" {
		for i, it := range items {
			if pi, ok := it.(prItem); ok && prKey(pi.pr.Repo, pi.pr.Number) == savedKey {
				v.list.Select(i)
				break
			}
		}
	}

	// Todo list is a filtered view over the same data — rebuild in lockstep.
	a.todo.rebuild(a)
}

func (v *prsView) filtering() bool { return v.list.FilterState() == list.Filtering }

// --- prItem ---

type prItem struct {
	pr          PR
	summary     string
	noteCount   int
	scrutinized bool
}

func (i prItem) Title() string {
	head := fmt.Sprintf("%s#%d  %s  @%s", i.pr.Repo, i.pr.Number, i.pr.Title, i.pr.Author)
	if i.scrutinized {
		head = "[S] " + head
	}
	var parts []string
	if i.summary != "" {
		parts = append(parts, i.summary)
	}
	if i.noteCount > 0 {
		parts = append(parts, fmt.Sprintf("%d notes", i.noteCount))
	}
	if len(parts) > 0 {
		head += "  (" + strings.Join(parts, ", ") + ")"
	}
	return head
}
func (i prItem) Description() string { return "" }
func (i prItem) FilterValue() string { return i.Title() }

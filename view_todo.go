package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// --- todoView ---

type todoView struct {
	list list.Model
}

func newTodoView() todoView {
	l := list.New(nil, compactDelegate(), 0, 0)
	l.Title = "Todo"
	l.SetShowHelp(false)
	return todoView{list: l}
}

func (v *todoView) Resize(w, h int) { v.list.SetSize(w, h) }

func (v *todoView) View(a *app) string { return v.list.View() }

func (v *todoView) Footer(a *app) string {
	return "l/enter: open  A: approve/review  M: merge  a: all PRs  q: quit"
}

// Update handles keys for the todo view. Returns the command to run.
func (v *todoView) Update(a *app, msg tea.Msg) tea.Cmd {
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

func (v *todoView) bindings(a *app) []Binding {
	return []Binding{
		{
			Name: "all-prs", Keys: []string{"a"},
			Desc: "all PRs", Group: "Navigate",
			Action: func(a *app) tea.Cmd {
				a.activeView = viewPRs
				return nil
			},
		},
		{
			Name: "open-pr", Keys: []string{"enter", "l", "right"},
			Desc: "open selected PR", Group: "Navigate",
			Action: func(a *app) tea.Cmd {
				if it, ok := v.list.SelectedItem().(todoItem); ok {
					return a.openPR(it.pr)
				}
				return nil
			},
		},
		{
			Name: "review", Keys: []string{"A"},
			Desc: "open review modal (approve / request-changes / comment)", Group: "View",
			Action: func(a *app) tea.Cmd {
				it, ok := v.list.SelectedItem().(todoItem)
				if !ok {
					return nil
				}
				return a.openReview(it.pr)
			},
		},
		{
			Name: "merge", Keys: []string{"M"},
			Desc: "open merge modal (squash / merge / rebase)", Group: "View",
			Action: func(a *app) tea.Cmd {
				it, ok := v.list.SelectedItem().(todoItem)
				if !ok {
					return nil
				}
				return a.openMerge(it.pr)
			},
		},
	}
}

func (v *todoView) delegate(msg tea.Msg) tea.Cmd {
	prev := v.list.Index()
	var cmd tea.Cmd
	v.list, cmd = v.list.Update(msg)
	skipSectionHeaders(&v.list, prev)
	return cmd
}

// rebuild walks a.allPRs and emits a todoItem for each PR with
// outstanding work. Groups into two sections: "needs attention"
// (in-progress, catch-up, notes) and "new" (never-touched). When
// there's nothing actionable, shows a "caught up" header so the list
// isn't a wall of "unseen" rows with no context.
func (v *todoView) rebuild(a *app) {
	var savedKey string
	if sel, ok := v.list.SelectedItem().(todoItem); ok {
		savedKey = prKey(sel.pr.Repo, sel.pr.Number)
	}

	var actionable, newPRs []todoItem
	for _, pr := range a.allPRs {
		key := prKey(pr.Repo, pr.Number)
		ti := buildTodoItem(a, pr)

		// Pin PRs to "needs attention" once they first appear there —
		// prevents the list from shifting under the user as they mark
		// things reviewed.
		isActionableNow := ti != nil && !(len(ti.tags) == 1 && ti.tags[0] == "unseen")
		if isActionableNow {
			a.pinnedAttention[key] = true
		}

		if a.pinnedAttention[key] {
			if ti == nil {
				ti = &todoItem{pr: pr, tags: []string{"done"}}
			}
			actionable = append(actionable, *ti)
			continue
		}
		if ti == nil {
			continue
		}
		newPRs = append(newPRs, *ti)
	}

	var items []list.Item
	switch {
	case len(actionable) > 0:
		items = append(items, sectionItem{label: "── needs attention ──"})
		for _, it := range actionable {
			items = append(items, it)
		}
		if len(newPRs) > 0 {
			items = append(items, sectionItem{label: "── new ──"})
			for _, it := range newPRs {
				items = append(items, it)
			}
		}
	case len(newPRs) > 0:
		items = append(items, sectionItem{label: "── ✓ caught up — new PRs below ──"})
		for _, it := range newPRs {
			items = append(items, it)
		}
	default:
		items = append(items, sectionItem{label: "── ✓ nothing to do ──"})
	}

	v.list.SetItems(items)
	outstanding := a.outstandingPRCount()
	if outstanding == 0 {
		v.list.Title = "✓ All caught up"
	} else {
		v.list.Title = fmt.Sprintf("Todo (%d)", outstanding)
	}

	if savedKey != "" {
		for i, it := range items {
			if pi, ok := it.(todoItem); ok && prKey(pi.pr.Repo, pi.pr.Number) == savedKey {
				v.list.Select(i)
				break
			}
		}
	}
}

func (v *todoView) filtering() bool { return v.list.FilterState() == list.Filtering }

// --- todoItem ---

// todoItem is a compact row in the todo dashboard view. Only PRs with
// outstanding work (in-progress, catch-up, unseen, notes) become todoItems.
type todoItem struct {
	pr        PR
	tags      []string // in stable order: in-progress, catch-up, unseen, notes
	done      int      // catch-up progress — ignored when catch-up absent
	total     int
	remaining int // unseen hunk count — ignored when files not loaded
	notes     int
}

func (i todoItem) Title() string {
	var suffix []string
	for _, t := range i.tags {
		switch t {
		case "in-progress":
			if i.remaining > 0 {
				suffix = append(suffix, fmt.Sprintf("%d new", i.remaining))
			} else {
				suffix = append(suffix, "in progress")
			}
		case "catch-up":
			suffix = append(suffix, fmt.Sprintf("catch-up %d/%d", i.done, i.total))
		case "unseen":
			suffix = append(suffix, "unseen")
		case "notes":
			suffix = append(suffix, fmt.Sprintf("%d notes", i.notes))
		case "done":
			suffix = append(suffix, "✓ done")
		}
	}
	head := fmt.Sprintf("%s#%d  %s  @%s", i.pr.Repo, i.pr.Number, i.pr.Title, i.pr.Author)
	if len(suffix) > 0 {
		head += "  [" + strings.Join(suffix, ", ") + "]"
	}
	return head
}
func (i todoItem) Description() string { return "" }
func (i todoItem) FilterValue() string { return i.Title() }

// buildTodoItem returns a todoItem for pr if it needs attention, or nil otherwise.
func buildTodoItem(a *app, pr PR) *todoItem {
	if !a.prHasOutstandingWork(pr) {
		return nil
	}
	notes := a.brain.NoteCountForPR(pr.Repo, pr.Number)
	cu := a.brain.ActiveSession(pr.Repo, pr.Number)
	touched := a.brain.HasAnyMarks(pr.Repo, pr.Number) ||
		len(a.brain.AllFileReviewedStates(pr.Repo, pr.Number)) > 0

	files, filesLoaded := a.prFiles[prKey(pr.Repo, pr.Number)]
	var remaining int
	if filesLoaded {
		remaining = a.brain.UnseenCount(pr.Repo, pr.Number, files)
	}

	it := todoItem{pr: pr, notes: notes, remaining: remaining}
	if touched && cu == nil {
		if !filesLoaded || remaining > 0 {
			it.tags = append(it.tags, "in-progress")
		}
	}
	if cu != nil {
		it.tags = append(it.tags, "catch-up")
		it.done = cu.FilesDone
		it.total = cu.FilesTotal
	}
	if !touched && cu == nil {
		it.tags = append(it.tags, "unseen")
	}
	if notes > 0 {
		it.tags = append(it.tags, "notes")
	}
	return &it
}

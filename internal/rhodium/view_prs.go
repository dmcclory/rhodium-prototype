package rhodium

import (
	"fmt"
	"rhodium/internal/brain"
	"rhodium/internal/gh"
	"rhodium/internal/tui/keys"
	"rhodium/internal/tui/prrow"
	"rhodium/internal/tui/router"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// --- prsView ---

type prsView struct {
	list list.Model
}

func newPRsView() prsView {
	l := list.New(nil, compactDelegate(), 0, 0)
	l.SetShowHelp(false)
	return prsView{list: l}
}

func (v *prsView) Resize(w, h int) { v.list.SetSize(w, h) }

func (v *prsView) View(a *app) string {
	return v.list.View()
}

func (v *prsView) Footer(a *app) string {
	return "l/enter: open  A: approve/review  M: merge  C: comments  s: scrutiny  h/esc: back to todo  q: quit"
}

func (v *prsView) Update(a *app, msg tea.Msg) tea.Cmd {
	key, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return v.delegate(msg)
	}
	filtering := v.list.FilterState() == list.Filtering
	if cmd, matched := keys.Dispatch(key.String(), filtering, v.bindings(a), globalBindings(a)); matched {
		return cmd
	}
	return v.delegate(msg)
}

func (v *prsView) bindings(a *app) []keys.Binding {
	return []keys.Binding{
		{
			Name: "back", Keys: []string{"esc", "h", "left"},
			Desc: "back to todo", Group: "Navigate",
			Action: func() tea.Cmd {
				return router.Navigate(router.RouteTodo)
			},
		},
		{
			Name: "open-pr", Keys: []string{"enter", "l", "right"},
			Desc: "open selected PR", Group: "Navigate",
			Action: func() tea.Cmd {
				if it, ok := v.list.SelectedItem().(prItem); ok {
					return a.openPR(it.pr)
				}
				return nil
			},
		},
		{
			Name: "review", Keys: []string{"A"},
			Desc: "open review modal (approve / request-changes / comment)", Group: "View",
			Action: func() tea.Cmd {
				it, ok := v.list.SelectedItem().(prItem)
				if !ok {
					return nil
				}
				return a.openReview(it.pr)
			},
		},
		{
			Name: "merge", Keys: []string{"M"},
			Desc: "open merge modal (squash / merge / rebase)", Group: "View",
			Action: func() tea.Cmd {
				it, ok := v.list.SelectedItem().(prItem)
				if !ok {
					return nil
				}
				return a.openMerge(it.pr)
			},
		},
		{
			Name: "comments", Keys: []string{"C"},
			Desc: "view PR comments", Group: "View",
			Action: func() tea.Cmd {
				it, ok := v.list.SelectedItem().(prItem)
				if !ok {
					return nil
				}
				a.session.selectedPR = &it.pr
				if _, cached := a.cache.prComments[brain.PRKey(it.pr.Repo, it.pr.Number)]; !cached {
					return tea.Batch(loadCommentsCmd(it.pr), a.openComments(router.RoutePRs))
				}
				return a.openComments(router.RoutePRs)
			},
		},
		{
			Name: "scrutiny", Keys: []string{"s"},
			Desc: "toggle scrutiny on selected PR", Group: "View",
			Action: func() tea.Cmd {
				it, ok := v.list.SelectedItem().(prItem)
				if !ok {
					return nil
				}
				on := !it.scrutinized
				a.brain.SetScrutiny(it.pr.Repo, it.pr.Number, on)
				v.rebuild(a)
				if on {
					a.status.msg = fmt.Sprintf("scrutiny ON for %s#%d — full diffs, no catch-up shortcuts", it.pr.Repo, it.pr.Number)
				} else {
					a.status.msg = fmt.Sprintf("scrutiny OFF for %s#%d", it.pr.Repo, it.pr.Number)
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

// mergePRs appends PRs whose (repo, number) aren't already in a.cache.allPRs
// and returns just the newly-added ones, so callers can kick off file
// prefetch without redundantly re-fetching PRs already loaded.
func mergePRs(a *app, prs []gh.PR) []gh.PR {
	seen := make(map[string]bool, len(a.cache.allPRs))
	for _, p := range a.cache.allPRs {
		seen[brain.PRKey(p.Repo, p.Number)] = true
	}
	var added []gh.PR
	for _, p := range prs {
		k := brain.PRKey(p.Repo, p.Number)
		if seen[k] {
			continue
		}
		seen[k] = true
		a.cache.allPRs = append(a.cache.allPRs, p)
		added = append(added, p)
	}
	return added
}

func (v *prsView) rebuild(a *app) {
	var savedKey string
	if sel, ok := v.list.SelectedItem().(prItem); ok {
		savedKey = brain.PRKey(sel.pr.Repo, sel.pr.Number)
	}

	me := a.cfg.GitHubUser
	// Build all items flat first so we can compute column widths over the
	// whole set, then partition into buckets afterwards.
	allItems := make([]prItem, 0, len(a.cache.allPRs))
	bucket := make([]int, 0, len(a.cache.allPRs)) // 0=mine, 1=in-progress, 2=untouched
	for _, pr := range a.cache.allPRs {
		it := prItem{pr: pr, noteCount: a.brain.NoteCountForPR(pr.Repo, pr.Number), scrutinized: a.brain.IsScrutinized(pr.Repo, pr.Number)}
		// A PR is "in progress" if the brain has any marks for it, even
		// before we've fetched its file list. This keeps already-touched
		// PRs from popping between buckets during startup prefetch.
		looked := a.brain.HasAnyMarks(pr.Repo, pr.Number)
		if files, ok := a.cache.prFiles[brain.PRKey(pr.Repo, pr.Number)]; ok {
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
		switch {
		case me != "" && pr.Author == me:
			bucket = append(bucket, 0)
		case looked:
			bucket = append(bucket, 1)
		default:
			bucket = append(bucket, 2)
		}
		allItems = append(allItems, it)
	}

	prs := make([]gh.PR, len(allItems))
	anyScrutiny := false
	for i, it := range allItems {
		prs[i] = it.pr
		if it.scrutinized {
			anyScrutiny = true
		}
	}
	cols := prrow.ComputeCols(prs, anyScrutiny)
	for i := range allItems {
		allItems[i].cols = cols
	}

	var mine, inProgress, untouched []prItem
	for i, it := range allItems {
		switch bucket[i] {
		case 0:
			mine = append(mine, it)
		case 1:
			inProgress = append(inProgress, it)
		default:
			untouched = append(untouched, it)
		}
	}

	var items []list.Item
	appendSection := func(label string, entries []prItem) {
		if len(entries) == 0 {
			return
		}
		items = append(items, sectionItem{label: label})
		for _, it := range entries {
			items = append(items, it)
		}
	}
	appendSection("── mine ──", mine)
	appendSection("── in progress ──", inProgress)
	appendSection("── new ──", untouched)
	v.list.SetItems(items)
	if savedKey != "" {
		for i, it := range items {
			if pi, ok := it.(prItem); ok && brain.PRKey(pi.pr.Repo, pi.pr.Number) == savedKey {
				v.list.Select(i)
				break
			}
		}
	}

	// Todo list is a filtered view over the same data — rebuild in lockstep.
	a.rebuildTodo()
}

func (v *prsView) filtering() bool { return v.list.FilterState() == list.Filtering }

// --- prItem ---

type prItem struct {
	pr          gh.PR
	summary     string
	noteCount   int
	scrutinized bool
	cols        prrow.Cols
}

func (i prItem) Title() string {
	var b strings.Builder
	if i.cols.AnyScrutiny {
		if i.scrutinized {
			b.WriteString("[S] ")
		} else {
			b.WriteString("    ")
		}
	}
	b.WriteString(prrow.PadRight(prrow.RepoNumStr(i.pr), i.cols.RepoNum))
	b.WriteString("  ")
	b.WriteString(prrow.PadRight(prrow.RenderStatus(i.pr), i.cols.Status))
	b.WriteString("  ")
	b.WriteString(prrow.PadRight(truncate(i.pr.Title, prrow.MaxTitleWidth), i.cols.Title))
	b.WriteString("  ")
	b.WriteString(prrow.PadRight("@"+i.pr.Author, i.cols.Author))
	var parts []string
	if i.summary != "" {
		parts = append(parts, i.summary)
	}
	if i.noteCount > 0 {
		parts = append(parts, fmt.Sprintf("%d notes", i.noteCount))
	}
	if len(parts) > 0 {
		b.WriteString("  (")
		b.WriteString(strings.Join(parts, ", "))
		b.WriteString(")")
	}
	return b.String()
}
func (i prItem) Description() string { return "" }
func (i prItem) FilterValue() string { return i.Title() }

package rhodium

import (
	"fmt"

	"rhodium/internal/brain"
	"rhodium/internal/gh"
	"rhodium/internal/tui/prs"

	tea "github.com/charmbracelet/bubbletea"
)

// rebuildPRs walks a.cache.allPRs, computes per-PR summary/note/scrutiny
// data via brain queries, partitions into mine / in-progress / new, and
// hands the result to the prs view. The todo view is a filtered slice of
// the same data, so this also kicks rebuildTodo to keep them in lockstep.
func (a *app) rebuildPRs() {
	me := a.cfg.GitHubUser
	var mine, inProgress, untouched []prs.Item
	for _, pr := range a.cache.allPRs {
		it := prs.Item{
			PR:          pr,
			NoteCount:   a.brain.NoteCountForPR(pr.Repo, pr.Number),
			Scrutinized: a.brain.IsScrutinized(pr.Repo, pr.Number),
		}
		// A PR is "in progress" if the brain has any marks for it, even
		// before we've fetched its file list. This keeps already-touched
		// PRs from popping between buckets during startup prefetch.
		looked := a.brain.HasAnyMarks(pr.Repo, pr.Number)
		if files, ok := a.cache.prFiles[brain.PRKey(pr.Repo, pr.Number)]; ok {
			unseen := a.brain.UnseenCount(pr.Repo, pr.Number, files)
			if unseen == 0 {
				it.Summary = "✓ caught up"
			} else {
				it.Summary = fmt.Sprintf("%d new", unseen)
			}
			if session := a.brain.ActiveSession(pr.Repo, pr.Number); session != nil {
				it.Summary += fmt.Sprintf(", ↻ %d/%d", session.FilesDone, session.FilesTotal)
			} else {
				reviewedStates := a.brain.AllFileReviewedStates(pr.Repo, pr.Number)
				catchUpCount := 0
				for _, f := range files {
					if s := reviewedStates[f.Path]; s.HeadSHA != "" && (s.HeadSHA != pr.HeadSHA || s.BaseSHA != pr.BaseSHA) {
						catchUpCount++
					}
				}
				if catchUpCount > 0 {
					it.Summary += fmt.Sprintf(", %d ↻", catchUpCount)
				}
			}
		}
		switch {
		case me != "" && pr.Author == me:
			mine = append(mine, it)
		case looked:
			inProgress = append(inProgress, it)
		default:
			untouched = append(untouched, it)
		}
	}
	a.prs.Rebuild(mine, inProgress, untouched)
	// Todo list is a filtered view over the same data — rebuild in lockstep.
	a.rebuildTodo()
}

// toggleScrutiny flips the brain's scrutiny flag for pr, rebuilds the PR
// list (so the [S] glyph and bucketing update), and reports the new state
// on the status line. Handler for prs.ScrutinyToggleMsg — keeps brain
// mutation out of the view.
func (a *app) toggleScrutiny(pr gh.PR) tea.Cmd {
	on := !a.brain.IsScrutinized(pr.Repo, pr.Number)
	a.brain.SetScrutiny(pr.Repo, pr.Number, on)
	a.rebuildPRs()
	if on {
		a.status.msg = fmt.Sprintf("scrutiny ON for %s#%d — full diffs, no catch-up shortcuts", pr.Repo, pr.Number)
	} else {
		a.status.msg = fmt.Sprintf("scrutiny OFF for %s#%d", pr.Repo, pr.Number)
	}
	return nil
}

// mergePRs appends PRs whose (repo, number) aren't already in
// a.cache.allPRs and returns just the newly-added ones, so callers can
// kick off file prefetch without redundantly re-fetching PRs already
// loaded.
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

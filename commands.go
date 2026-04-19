package main

import (
	"fmt"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// loadRepoPRsCmd fetches PRs for a single repo and returns a prsLoadedMsg.
// Runs one per repo via tea.Batch so each repo renders independently — the
// in-progress bucket stays stable at the top; untouched PRs fill in below as
// they arrive.
func loadRepoPRsCmd(repo string) tea.Cmd {
	return func() tea.Msg {
		prs, err := listPRs(repo)
		if err != nil {
			return prsLoadedMsg{err: fmt.Errorf("%s: %w", repo, err)}
		}
		return prsLoadedMsg{prs: prs}
	}
}

func loadFilesCmd(pr PR) tea.Cmd {
	return func() tea.Msg {
		return fetchOne(pr)
	}
}

// autoAdvanceCmd checks each file in a PR for implicit review eligibility.
// For files where the reviewer has marks but the PR hasn't changed since their
// last review (b1==b2, f1==f2), it auto-advances the brain state. Also creates
// a catch-up session if there are files needing catch-up.
func autoAdvanceCmd(brain *Brain, pr PR, files []FileChange) tea.Cmd {
	return func() tea.Msg {
		states := brain.AllFileReviewedStates(pr.Repo, pr.Number)
		if len(states) == 0 {
			return autoAdvanceMsg{prKey: prKey(pr.Repo, pr.Number)}
		}

		// Count files needing catch-up (reviewed at a different head/base).
		var needsCatchUp int
		for _, fc := range files {
			s, ok := states[fc.Path]
			if !ok || s.HeadSHA == "" {
				continue
			}
			if s.HeadSHA != pr.HeadSHA || s.BaseSHA != pr.BaseSHA {
				needsCatchUp++
			}
		}

		// Create a catch-up session if there are files to catch up on
		// and no active session already exists for this head.
		if needsCatchUp > 0 {
			existing := brain.ActiveCatchUp(pr.Repo, pr.Number)
			if existing == nil || existing.NewHead != pr.HeadSHA {
				// Find a representative old head (from any reviewed file).
				var oldHead, oldBase string
				for _, s := range states {
					if s.HeadSHA != "" {
						oldHead = s.HeadSHA
						oldBase = s.BaseSHA
						break
					}
				}
				brain.CreateCatchUp(pr.Repo, pr.Number, oldHead, pr.HeadSHA, oldBase, pr.BaseSHA, needsCatchUp)
			}
		}

		// Auto-advance files that haven't actually changed.
		var advanced []string
		session := brain.ActiveCatchUp(pr.Repo, pr.Number)
		for _, fc := range files {
			s, ok := states[fc.Path]
			if !ok || s.HeadSHA == "" {
				continue
			}
			if s.HeadSHA == pr.HeadSHA && s.BaseSHA == pr.BaseSHA {
				continue
			}
			if s.BaseSHA != pr.BaseSHA && s.BaseSHA != "" {
				continue // rebase — skip auto-advance
			}

			hunks := parseHunks(fc.Patch)
			marks := brain.HunkMarks(pr.Repo, pr.Number, fc.Path)
			if len(hunks) == 0 {
				brain.SetFileReviewed(pr.Repo, pr.Number, fc.Path, pr.HeadSHA, pr.BaseSHA)
				advanced = append(advanced, fc.Path)
				if session != nil {
					brain.CatchUpAdvanceFile(session.ID)
				}
				continue
			}

			allMarked := true
			for _, h := range hunks {
				if !marks[h.Hash] {
					allMarked = false
					break
				}
			}
			if allMarked {
				brain.SetFileReviewed(pr.Repo, pr.Number, fc.Path, pr.HeadSHA, pr.BaseSHA)
				advanced = append(advanced, fc.Path)
				if session != nil {
					brain.CatchUpAdvanceFile(session.ID)
				}
			}
		}
		return autoAdvanceMsg{prKey: prKey(pr.Repo, pr.Number), advancedFiles: advanced}
	}
}

func fetchOne(pr PR) filesLoadedMsg {
	files, err := listPRFiles(pr.Repo, pr.Number)
	if err != nil {
		return filesLoadedMsg{pr: pr, err: err}
	}
	return filesLoadedMsg{pr: pr, files: files}
}

func prefetchAllCmd(prs []PR) tea.Cmd {
	const workers = 4
	return func() tea.Msg {
		jobs := make(chan PR)
		done := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for pr := range jobs {
					program.Send(fetchOne(pr))
				}
			}()
		}
		go func() {
			for _, pr := range prs {
				jobs <- pr
			}
			close(jobs)
			wg.Wait()
			close(done)
		}()
		<-done
		return prefetchDoneMsg{}
	}
}

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
// Three reasons a file can be auto-advanced without the reviewer opening it:
//
//   - all current hunks are already marked in the brain (locally decidable),
//   - the patch had no reviewable hunks (binary, huge, pure rename),
//   - rev-update: file content at the reviewer's last-seen head is byte-equal
//     to content at the PR's current head, so a rebase / force-push moved
//     SHAs but didn't actually change this file (needs a content fetch).
//
// The rev-update probe is why this function does I/O: one gh call per
// candidate file to fetch f1 at the old head. Files not eligible for rev-
// update are skipped and left for the per-file flow in view_diff to classify.
// We also notice "forget" — paths recorded in file_reviews that no longer
// appear in the current PR files — and silently advance them.
func autoAdvanceCmd(brain *Brain, pr PR, files []FileChange) tea.Cmd {
	return func() tea.Msg {
		states := brain.AllFileReviewedStates(pr.Repo, pr.Number)
		if len(states) == 0 {
			return autoAdvanceMsg{prKey: prKey(pr.Repo, pr.Number)}
		}

		// Index current files by path for O(1) forget-detection.
		currentByPath := make(map[string]FileChange, len(files))
		for _, fc := range files {
			currentByPath[fc.Path] = fc
		}

		// Partition reviewed files into three buckets:
		//   alreadyCurrent: s.HeadSHA == pr.HeadSHA && s.BaseSHA == pr.BaseSHA
		//     → nothing to do, not a catch-up candidate.
		//   forgotten:     path not in currentByPath
		//     → auto-advance with an advanceForget reason.
		//   drifted:       SHAs moved since last review
		//     → evaluate via decideAdvance (may need a content fetch).
		var drifted []FileChange
		var forgotten []string
		for path, s := range states {
			if s.HeadSHA == "" {
				continue
			}
			if fc, ok := currentByPath[path]; ok {
				if s.HeadSHA == pr.HeadSHA && s.BaseSHA == pr.BaseSHA {
					continue
				}
				drifted = append(drifted, fc)
			} else {
				forgotten = append(forgotten, path)
			}
		}

		// Probe the drifted bucket. `unresolved` is the count that neither
		// local-marks nor rev-update could advance — these still need the
		// reviewer and count toward the catch-up session target.
		results := probeAdvance(brain, pr, drifted, states, 4)
		var advanced []string
		var unresolved int
		for _, fc := range drifted {
			r, ok := results[fc.Path]
			if !ok || r == advanceNone {
				unresolved++
				continue
			}
			advanced = append(advanced, fc.Path)
		}
		advanced = append(advanced, forgotten...)

		// Create a catch-up session sized to the *unresolved* count — files
		// we're about to silently advance below don't need the reviewer,
		// so they shouldn't inflate the session's total.
		if unresolved > 0 {
			existing := brain.ActiveCatchUp(pr.Repo, pr.Number)
			if existing == nil || existing.NewHead != pr.HeadSHA {
				var oldHead, oldBase string
				for _, s := range states {
					if s.HeadSHA != "" {
						oldHead = s.HeadSHA
						oldBase = s.BaseSHA
						break
					}
				}
				brain.CreateCatchUp(pr.Repo, pr.Number, oldHead, pr.HeadSHA, oldBase, pr.BaseSHA, unresolved)
			}
		}

		// Commit advances.
		session := brain.ActiveCatchUp(pr.Repo, pr.Number)
		for _, path := range advanced {
			brain.SetFileReviewed(pr.Repo, pr.Number, path, pr.HeadSHA, pr.BaseSHA)
			if session != nil && !contains(forgotten, path) {
				// Forgotten files weren't included in the session target;
				// don't advance the counter for them.
				brain.CatchUpAdvanceFile(session.ID)
			}
		}

		return autoAdvanceMsg{prKey: prKey(pr.Repo, pr.Number), advancedFiles: advanced}
	}
}

// probeAdvance runs decideAdvance for each drifted file, fetching f1 content
// (at the reviewer's last-seen head) only when needed — i.e., when the local
// mark check didn't already decide. Fetches are parallelized across `workers`
// goroutines since each is a separate `gh api` round-trip.
func probeAdvance(brain *Brain, pr PR, drifted []FileChange, states map[string]FileReviewState, workers int) map[string]advanceReason {
	out := make(map[string]advanceReason, len(drifted))
	var mu sync.Mutex

	// First pass: resolve everything that doesn't need a fetch.
	var needsFetch []FileChange
	for _, fc := range drifted {
		hunks := parseHunks(fc.Patch)
		marks := brain.HunkMarks(pr.Repo, pr.Number, fc.Path)
		// Call decideAdvance with empty content — if it returns anything
		// other than advanceNone, we don't need the fetch at all.
		r := decideAdvance(hunks, marks, "", "")
		if r != advanceNone {
			out[fc.Path] = r
			continue
		}
		needsFetch = append(needsFetch, fc)
	}

	if len(needsFetch) == 0 || workers < 1 {
		return out
	}

	jobs := make(chan FileChange)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for fc := range jobs {
				s := states[fc.Path]
				// We need f1 (old head) and f2 (current head). newContent
				// comes from the current blob if we have its SHA — cheaper
				// than another ref lookup. Fall back to ref fetch otherwise.
				var f2 string
				if fc.Blob != "" {
					f2, _ = fetchBlob(pr.Repo, fc.Blob)
				}
				if f2 == "" {
					f2, _ = fetchFileAtRef(pr.Repo, fc.Path, pr.HeadSHA)
				}
				f1, _ := fetchFileAtRef(pr.Repo, fc.Path, s.HeadSHA)

				hunks := parseHunks(fc.Patch)
				marks := brain.HunkMarks(pr.Repo, pr.Number, fc.Path)
				r := decideAdvance(hunks, marks, f1, f2)
				mu.Lock()
				out[fc.Path] = r
				mu.Unlock()
			}
		}()
	}
	for _, fc := range needsFetch {
		jobs <- fc
	}
	close(jobs)
	wg.Wait()
	return out
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

package rhodium

import (
	"fmt"
	"rhodium/internal/brain"
	"rhodium/internal/diff"
	"rhodium/internal/gh"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// loadRepoPRsCmd fetches PRs for a single repo and returns a prsLoadedMsg.
// Runs one per repo via tea.Batch so each repo renders independently — the
// in-progress bucket stays stable at the top; untouched PRs fill in below as
// they arrive.
func loadRepoPRsCmd(repo string) tea.Cmd {
	return func() tea.Msg {
		prs, err := gh.ListPRs(repo)
		if err != nil {
			return prsLoadedMsg{err: fmt.Errorf("%s: %w", repo, err)}
		}
		return prsLoadedMsg{prs: prs}
	}
}

func loadFilesCmd(pr gh.PR) tea.Cmd {
	return func() tea.Msg {
		return fetchOne(pr)
	}
}

// loadCommentsCmd fetches the three GH comment streams for a PR. Errors
// surface as commentsLoadedMsg.err so the UI can flash a status; a partial
// success (some streams empty) still returns a usable comments slice.
func loadCommentsCmd(pr gh.PR) tea.Cmd {
	return func() tea.Msg {
		comments, err := gh.FetchPRComments(pr.Repo, pr.Number)
		return commentsLoadedMsg{repo: pr.Repo, prNum: pr.Number, comments: comments, err: err}
	}
}

// refreshRepoCmd re-lists PRs for a repo on the periodic remote-refresh
// tick. Unlike loadRepoPRsCmd, the result is a prsRefreshedMsg so the
// handler updates live-status fields on existing rows rather than
// re-running the cold-start prefetch.
func refreshRepoCmd(repo string) tea.Cmd {
	return func() tea.Msg {
		prs, err := gh.ListPRs(repo)
		return prsRefreshedMsg{repo: repo, prs: prs, err: err}
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
func autoAdvanceCmd(b *brain.Brain, pr gh.PR, files []gh.FileChange) tea.Cmd {
	return func() tea.Msg {
		states := b.AllFileReviewedStates(pr.Repo, pr.Number)
		if len(states) == 0 {
			// Fresh PR — no prior review history. Create a review session
			// over all files so line-count progress tracking is available
			// from the start, not only during catch-up.
			ensureReviewSession(b, pr, files)
			return autoAdvanceMsg{prKey: brain.PRKey(pr.Repo, pr.Number)}
		}

		drifted, forgotten := partitionReviewedFiles(states, files, pr)

		// Probe the drifted bucket. `unresolvedFiles` are the ones that
		// neither local-marks nor rev-update could advance — these still
		// need the reviewer and become the session's file list.
		results := probeAdvance(b, pr, drifted, states, 4)
		var advanced []string
		var unresolvedFiles []gh.FileChange
		for _, fc := range drifted {
			r, ok := results[fc.Path]
			if !ok || r == advanceNone {
				unresolvedFiles = append(unresolvedFiles, fc)
				continue
			}
			advanced = append(advanced, fc.Path)
		}
		advanced = append(advanced, forgotten...)

		ensureCatchUpSession(b, pr, states, unresolvedFiles)
		commitAdvances(b, pr, advanced)

		return autoAdvanceMsg{prKey: brain.PRKey(pr.Repo, pr.Number), advancedFiles: advanced}
	}
}

// partitionReviewedFiles classifies each reviewed file into one of three
// states: still on the current head (skipped), forgotten (path no longer
// in the PR — silently advance), or drifted (SHAs moved — needs probing).
// Returns the drifted FileChanges + forgotten paths.
func partitionReviewedFiles(states map[string]brain.FileReviewState, files []gh.FileChange, pr gh.PR) (drifted []gh.FileChange, forgotten []string) {
	currentByPath := make(map[string]gh.FileChange, len(files))
	for _, fc := range files {
		currentByPath[fc.Path] = fc
	}
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
	return drifted, forgotten
}

// ensureCatchUpSession snapshots a review session over the still-
// unresolved files so their path list is stable even if the PR moves
// mid-review. No-op if there's already an active session pointing at
// the same goal SHAs, or if there's nothing left to review.
func ensureCatchUpSession(b *brain.Brain, pr gh.PR, states map[string]brain.FileReviewState, unresolvedFiles []gh.FileChange) {
	if len(unresolvedFiles) == 0 {
		return
	}
	existing := b.ActiveSession(pr.Repo, pr.Number)
	if existing != nil && existing.GoalHead == pr.HeadSHA && existing.GoalBase == pr.BaseSHA {
		return
	}
	var oldHead, oldBase string
	for _, s := range states {
		if s.HeadSHA != "" {
			oldHead = s.HeadSHA
			oldBase = s.BaseSHA
			break
		}
	}
	sessionFiles := make([]brain.SessionFile, 0, len(unresolvedFiles))
	for _, fc := range unresolvedFiles {
		lineCount := fc.Additions + fc.Deletions
		sessionFiles = append(sessionFiles, brain.SessionFile{Path: fc.Path, LineCount: lineCount})
	}
	b.CreateSession(pr.Repo, pr.Number, oldHead, oldBase, pr.HeadSHA, pr.BaseSHA, sessionFiles)
}

// ensureReviewSession creates an initial review session for a PR that has
// no prior review history. This gives line-count progress tracking from the
// very first file the reviewer opens, not only during catch-up. No-op if
// an active session already exists.
func ensureReviewSession(b *brain.Brain, pr gh.PR, files []gh.FileChange) {
	if len(files) == 0 {
		return
	}
	if b.ActiveSession(pr.Repo, pr.Number) != nil {
		return
	}
	sessionFiles := make([]brain.SessionFile, 0, len(files))
	for _, fc := range files {
		lineCount := fc.Additions + fc.Deletions
		sessionFiles = append(sessionFiles, brain.SessionFile{Path: fc.Path, LineCount: lineCount})
	}
	b.CreateSession(pr.Repo, pr.Number, "", "", pr.HeadSHA, pr.BaseSHA, sessionFiles)
}

// commitAdvances writes the file_reviews row for each advanced path,
// and also marks the path done within the active session if it was
// part of one — keeps the session counter consistent with file_reviews.
func commitAdvances(b *brain.Brain, pr gh.PR, advanced []string) {
	session := b.ActiveSession(pr.Repo, pr.Number)
	sessionPaths := map[string]bool{}
	if session != nil {
		for _, sf := range b.SessionFiles(session.ID) {
			sessionPaths[sf.Path] = true
		}
	}
	for _, path := range advanced {
		b.SetFileReviewed(pr.Repo, pr.Number, path, pr.HeadSHA, pr.BaseSHA, brain.MarkAuto)
		if session != nil && sessionPaths[path] {
			b.SetSessionFileDone(session.ID, path, true)
		}
	}
}

// probeAdvance runs decideAdvance for each drifted file, fetching f1 content
// (at the reviewer's last-seen head) only when needed — i.e., when the local
// mark check didn't already decide. Fetches are parallelized across `workers`
// goroutines since each is a separate `gh api` round-trip.
func probeAdvance(b *brain.Brain, pr gh.PR, drifted []gh.FileChange, states map[string]brain.FileReviewState, workers int) map[string]advanceReason {
	out := make(map[string]advanceReason, len(drifted))
	var mu sync.Mutex

	// First pass: resolve everything that doesn't need a fetch.
	var needsFetch []gh.FileChange
	for _, fc := range drifted {
		hunks := diff.ParseHunks(fc.Patch)
		marks := b.HunkMarks(pr.Repo, pr.Number, fc.Path)
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

	jobs := make(chan gh.FileChange)
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
					f2, _ = gh.FetchBlob(pr.Repo, fc.Blob)
				}
				if f2 == "" {
					f2, _ = gh.FetchFileAtRef(pr.Repo, fc.Path, pr.HeadSHA)
				}
				f1, _ := gh.FetchFileAtRef(pr.Repo, fc.Path, s.HeadSHA)

				hunks := diff.ParseHunks(fc.Patch)
				marks := b.HunkMarks(pr.Repo, pr.Number, fc.Path)
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

func fetchOne(pr gh.PR) filesLoadedMsg {
	files, err := gh.ListPRFiles(pr.Repo, pr.Number)
	if err != nil {
		return filesLoadedMsg{pr: pr, err: err}
	}
	return filesLoadedMsg{pr: pr, files: files}
}

func fetchComments(pr gh.PR) commentsLoadedMsg {
	comments, err := gh.FetchPRComments(pr.Repo, pr.Number)
	return commentsLoadedMsg{repo: pr.Repo, prNum: pr.Number, comments: comments, err: err}
}

func prefetchAllCmd(prs []gh.PR) tea.Cmd {
	const workers = 4
	return func() tea.Msg {
		jobs := make(chan gh.PR)
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

func prefetchCommentsCmd(prs []gh.PR) tea.Cmd {
	const workers = 4
	return func() tea.Msg {
		jobs := make(chan gh.PR)
		done := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for pr := range jobs {
					program.Send(fetchComments(pr))
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

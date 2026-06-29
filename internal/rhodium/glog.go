package rhodium

import (
	"fmt"

	"rhodium/internal/brain"
	"rhodium/internal/gh"
	coreglog "rhodium/internal/glog"
	glogview "rhodium/internal/tui/glog"
	"rhodium/internal/tui/router"

	tea "github.com/charmbracelet/bubbletea"
)

// enterGlog points the glog view at the selected PR. If the commit data is
// already cached it computes the rollup from current marks and shows it
// immediately; otherwise it shows an empty (loading) view and kicks off the
// fetch. Called from openPR (when the configured default lens is "commits")
// and from the NavigatedMsg handler (the `g` toggle from the files view).
func (a *app) enterGlog() tea.Cmd {
	pr := a.session.selectedPR
	if pr == nil {
		return nil
	}
	a.glog.BackRoute = a.session.listOrigin
	key := brain.PRKey(pr.Repo, pr.Number)
	if cd, ok := a.cache.prCommits[key]; ok {
		rollups := a.computeRollups(pr, cd.commits, cd.files)
		// Returning to the same PR's glog → keep cursor/expansion, just
		// refresh badges. Otherwise it's a fresh open.
		if cur := a.glog.PR(); cur != nil && brain.PRKey(cur.Repo, cur.Number) == key {
			a.glog.RefreshRollups(rollups)
		} else {
			a.glog.SetCommits(pr, rollups)
		}
		return nil
	}
	a.glog.SetCommits(pr, nil) // empty until the fetch lands
	return loadCommitsCmd(*pr)
}

// loadCommitsCmd fetches a PR's commits and, for each, the files it
// introduced. One ListPRCommits call plus one FetchCommitFiles per commit —
// review-scale PRs are small, and commit SHAs are immutable so the result is
// cached for the session.
func loadCommitsCmd(pr gh.PR) tea.Cmd {
	return func() tea.Msg {
		commits, err := gh.ListPRCommits(pr.Repo, pr.Number)
		if err != nil {
			return commitsLoadedMsg{pr: pr, err: err}
		}
		files := make(map[string][]gh.FileChange, len(commits))
		for _, c := range commits {
			fcs, err := gh.FetchCommitFiles(pr.Repo, c.SHA)
			if err != nil {
				return commitsLoadedMsg{pr: pr, err: err}
			}
			files[c.SHA] = fcs
		}
		return commitsLoadedMsg{pr: pr, commits: commits, files: files}
	}
}

func (a *app) onCommitsLoaded(msg commitsLoadedMsg) tea.Cmd {
	if msg.err != nil {
		a.status.msg = "error: " + msg.err.Error()
		return nil
	}
	key := brain.PRKey(msg.pr.Repo, msg.pr.Number)
	a.cache.prCommits[key] = commitData{commits: msg.commits, files: msg.files}
	// Only refresh the view if we're still on this PR.
	if a.session.selectedPR != nil && brain.PRKey(a.session.selectedPR.Repo, a.session.selectedPR.Number) == key {
		pr := msg.pr
		a.glog.SetCommits(&pr, a.computeRollups(&pr, msg.commits, msg.files))
	}
	return nil
}

// onGlogOpenHunk drills from glog into a commit-scoped file diff: it shows the
// selected commit's version of the file (not the latest), positioned at the
// chosen hunk, and arms the commit-walker so the file's history can be
// scrubbed with [ / ] / L.
func (a *app) onGlogOpenHunk(msg glogview.OpenHunkMsg) tea.Cmd {
	pr := a.session.selectedPR
	if pr == nil {
		return nil
	}
	cd, ok := a.cache.prCommits[brain.PRKey(pr.Repo, pr.Number)]
	if !ok {
		return nil
	}
	walk := a.buildWalk(cd, msg.Path, msg.CommitSHA)
	if walk == nil {
		return nil
	}
	a.session.walk = walk
	a.session.selectedFile = msg.Path
	a.diff.BackRoute = router.RouteGlog
	a.layout.focus(router.RouteDiff)
	a.diff.OpenCommitFile(a.brain, pr, walk.stops[walk.idx].fc, ghInlineForFile(a, msg.Path), msg.HunkHash)
	return nil
}

// buildWalk collects, in commit order, the commits that touched path (with
// their commit-scoped FileChange), plus the PR-level latest version, and
// positions the walker at startSHA.
func (a *app) buildWalk(cd commitData, path, startSHA string) *commitWalk {
	w := &commitWalk{path: path}
	for _, c := range cd.commits {
		if fc, ok := findFile(cd.files[c.SHA], path); ok {
			w.stops = append(w.stops, walkStop{sha: c.SHA, title: c.Title, fc: fc})
		}
	}
	if len(w.stops) == 0 {
		return nil
	}
	for i, s := range w.stops {
		if s.sha == startSHA {
			w.idx = i
			break
		}
	}
	pr := a.session.selectedPR
	if fc, ok := findFile(a.cache.prFiles[brain.PRKey(pr.Repo, pr.Number)], path); ok {
		w.latest = fc
	}
	return w
}

// walkKey handles the commit-walker keys while a glog-drilled file diff is
// open. Returns (cmd, true) when the key was a walk key.
func (a *app) walkKey(key tea.KeyMsg) (tea.Cmd, bool) {
	w := a.session.walk
	switch key.String() {
	case "]":
		return a.walkTo(w.idx + 1), true
	case "[":
		if w.idx == latestIdx {
			return a.walkTo(len(w.stops) - 1), true
		}
		return a.walkTo(w.idx - 1), true
	case "L":
		return a.walkToLatest(), true
	}
	return nil, false
}

func (a *app) walkTo(i int) tea.Cmd {
	w := a.session.walk
	if i < 0 {
		i = 0
	}
	if i >= len(w.stops) {
		i = len(w.stops) - 1
	}
	if i == w.idx {
		return nil
	}
	w.idx = i
	a.diff.OpenCommitFile(a.brain, a.session.selectedPR, w.stops[i].fc, ghInlineForFile(a, w.path), "")
	return nil
}

// walkToLatest jumps to the PR-level base..head view of the file via the
// normal Open path (so catch-up etc. apply to the aggregate diff).
func (a *app) walkToLatest() tea.Cmd {
	w := a.session.walk
	if w.idx == latestIdx || w.latest.Path == "" {
		return nil
	}
	w.idx = latestIdx
	return a.diff.Open(a.brain, a.session.selectedPR, w.latest, ghInlineForFile(a, w.path))
}

func findFile(files []gh.FileChange, path string) (gh.FileChange, bool) {
	for _, f := range files {
		if f.Path == path {
			return f, true
		}
	}
	return gh.FileChange{}, false
}

// strip renders the walker's footer prefix shown over the diff footer.
func (w *commitWalk) strip() string {
	if w.idx == latestIdx {
		return fmt.Sprintf("walk · %s · latest (PR diff) · [: commit  L: latest", w.path)
	}
	s := w.stops[w.idx]
	return fmt.Sprintf("walk · %s · commit %d/%d %s · [/]: prev/next  L: latest", w.path, w.idx+1, len(w.stops), shortSHA(s.sha))
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// computeRollups gathers the marks for every path the commits touch and runs
// the Tier-1 hash-intersection rollup. Marks are read fresh so badges reflect
// the latest review state each time glog is entered.
func (a *app) computeRollups(pr *gh.PR, commits []gh.Commit, files map[string][]gh.FileChange) []coreglog.CommitRollup {
	marksByPath := map[string]map[string]int{}
	for _, fcs := range files {
		for _, f := range fcs {
			if _, done := marksByPath[f.Path]; done {
				continue
			}
			marksByPath[f.Path] = a.brain.HunkMarks(pr.Repo, pr.Number, f.Path)
		}
	}
	return coreglog.Rollup(commits, files, marksByPath)
}

package main

// tea.Msg types used by the TUI. Grouped here (rather than next to the
// handlers) so it's easy to see the full set of async events the update
// loop has to handle.

type prsLoadedMsg struct {
	prs []PR
	err error
}

type filesLoadedMsg struct {
	pr    PR
	files []FileChange
	err   error
}

type prefetchDoneMsg struct{}

type autoAdvanceMsg struct {
	prKey         string
	advancedFiles []string
}

type blobLoadedMsg struct {
	content string
	err     error
}

type catchUpLoadedMsg struct {
	path  string
	files []FileChange
	err   error
}

type diamondClassifiedMsg struct {
	path    string
	class   Class   // top-level class, for status line
	diamond Diamond // four corners, kept for segment rendering
	result  *Result // slow-path segmentation; nil if classification failed
	patch   string  // whole-file delta patch, only filled for ShownAsDiff2 top-level
	err     error
}

// pollTickMsg fires on a slow interval while a PR is selected, prompting
// the TUI to re-read marks/notes from the brain. Primary purpose: pick up
// changes written by an nvim running in a separate tmux pane/window.
//
// gen is a generation counter incremented on each openPR; stale ticks
// (from previous PR sessions) compare unequal and are discarded, so we
// never have more than one live tick loop.
type pollTickMsg struct{ gen int }

// editorDoneMsg fires when the editor process exits (ExecProcess path)
// or the tmux command returns (tmux path — typically immediately after
// spawning the pane/window).
type editorDoneMsg struct {
	err error
}

// actionDoneMsg fires when a configured agent action (interactive tmux
// spawn, or fallback inline exec) completes. `action` is the action name
// so the status bar can distinguish which harness finished.
type actionDoneMsg struct {
	action string
	err    error
}

// inlineNotesReadyMsg carries parsed agent notes from a oneshot action
// back onto the update loop so SaveAgentNote runs on the main goroutine
// (keeps brain writes serialized with the rest of the update cycle).
type inlineNotesReadyMsg struct {
	action string
	pr     PR
	notes  []agentNote
}

// notePublishedMsg lands back on the update loop after a single inline
// comment POST finishes. noteID is the local notes.id; ghID is whatever
// GitHub returned (0 on error). err is nil on success.
type notePublishedMsg struct {
	noteID int64
	ghID   int64
	err    error
}

// reviewSubmittedMsg lands back on the update loop after submitReview
// returns. event echoes what we asked for (for the status line); err is
// nil on success.
type reviewSubmittedMsg struct {
	repo   string
	prNum  int
	event  ReviewEvent
	err    error
}

// mergeSubmittedMsg lands back on the update loop after mergePR returns.
// The PR is not removed from allPRs here — the next repo refresh drops it
// when GitHub stops listing it as open.
type mergeSubmittedMsg struct {
	repo   string
	prNum  int
	method MergeMethod
	err    error
}

// contributorsLoadedMsg lands back on the update loop after a contributors
// fetch completes. Results are cached on *app keyed by repo.
type contributorsLoadedMsg struct {
	repo         string
	contributors []Contributor
	err          error
}

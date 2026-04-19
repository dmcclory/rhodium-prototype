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
	class   Class
	diamond Diamond
	patch   string
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

package rhodium

import (
	"fmt"

	"rhodium/internal/gh"
	tuidiff "rhodium/internal/tui/diff"

	tea "github.com/charmbracelet/bubbletea"
)

// openInEditor handles diff.OpenEditorMsg: resolve the PR's worktree,
// stamp a status message, and run launchEditor with the file/line. The
// view doesn't have config or worktree handles itself, so this lives on
// the app.
func (a *app) openInEditor(m tuidiff.OpenEditorMsg) tea.Cmd {
	worktree, err := resolveWorktree(a.cfg, m.PR.Repo, m.PR.Number)
	if err != nil {
		a.status.msg = "open: " + err.Error()
		return nil
	}
	prKeyStr := fmt.Sprintf("%s#%d", m.PR.Repo, m.PR.Number)
	a.status.msg = fmt.Sprintf("opening %s:%d in %s", m.File, m.Line, worktree)
	return launchEditor(a.cfg, worktree, m.File, prKeyStr, m.Line)
}

// loadContributorsCmd kicks off an async contributors fetch. Results
// land as contributorsLoadedMsg and are stashed on the diff view so the
// next @-mention picker open is instant.
func loadContributorsCmd(repo string) tea.Cmd {
	return func() tea.Msg {
		c, err := gh.ListContributors(repo)
		return contributorsLoadedMsg{repo: repo, contributors: c, err: err}
	}
}

// publishNote handles diff.PublishNoteMsg: POST a single note as a
// GitHub inline review comment, returning notePublishedMsg back onto
// the update loop.
func (a *app) publishNote(m tuidiff.PublishNoteMsg) tea.Cmd {
	pr := m.PR
	noteID := m.NoteID
	body := m.Body
	commit := m.Commit
	path := m.Path
	line := m.Line
	return func() tea.Msg {
		ghID, err := gh.PostInlineComment(pr.Repo, pr.Number, gh.InlineComment{
			Body:     body,
			Path:     path,
			CommitID: commit,
			Line:     line,
		})
		return notePublishedMsg{noteID: noteID, ghID: ghID, err: err}
	}
}

package main

import "fmt"

func (m *model) currentFile() (FileChange, bool) {
	if m.selectedPR == nil {
		return FileChange{}, false
	}
	for _, f := range m.prFiles[prKey(m.selectedPR.Repo, m.selectedPR.Number)] {
		if f.Path == m.selectedFile {
			return f, true
		}
	}
	return FileChange{}, false
}

func firstUnmarked(hunks []Hunk, marks map[string]bool) int {
	for i, h := range hunks {
		if !marks[h.Hash] {
			return i
		}
	}
	return 0
}

func hashLine(s string) string {
	h := hashHunkBody([]string{"+" + s})
	return h
}

// advanceCatchUpSession advances the active catch-up session by one file.
func (m *model) advanceCatchUpSession() {
	if m.catchUpSession != nil {
		m.brain.CatchUpAdvanceFile(m.catchUpSession.ID)
		m.catchUpSession = m.brain.ActiveCatchUp(m.selectedPR.Repo, m.selectedPR.Number)
	}
}

func (m *model) saveMarks() {
	if m.selectedPR == nil || m.selectedFile == "" {
		return
	}
	if err := m.brain.SetHunkMarks(m.selectedPR.Repo, m.selectedPR.Number, m.selectedFile, m.currentMarks); err != nil {
		m.statusMsg = "save error: " + err.Error()
		return
	}
	// Record the PR head SHA we're reviewing against so catch-up diffs
	// know what version we last saw.
	if m.selectedPR.HeadSHA != "" {
		m.brain.SetFileReviewed(m.selectedPR.Repo, m.selectedPR.Number, m.selectedFile, m.selectedPR.HeadSHA, m.selectedPR.BaseSHA)
	}
}

func (m model) footer() string {
	if m.statusMsg != "" {
		return m.statusMsg
	}
	switch m.view {
	case viewDiff:
		if m.noting {
			return fmt.Sprintf("line %d  ctrl+d: save  esc: cancel", m.noteLineNo)
		}
		marked := 0
		for _, h := range m.currentHunks {
			if m.currentMarks[h.Hash] {
				marked++
			}
		}
		total := len(m.currentHunks)
		cur := m.hunkIdx + 1
		if total == 0 {
			cur = 0
		}
		modeHint := ""
		if m.catchUpOldHead != "" {
			if m.catchUpMode {
				modeHint = fmt.Sprintf("  [catch-up %s since %s]  d: full diff", m.catchUpClass, shortSHA(m.catchUpOldHead))
			} else {
				modeHint = "  [full diff]  d: catch-up"
			}
		}
		return fmt.Sprintf("hunk %d/%d  marked %d/%d%s  ↑/↓: nav  j/k: cursor  space: toggle+next  m: mark all  c: note  o: open in editor  u: unmark  h: back", cur, total, marked, total, modeHint)
	case viewFiles:
		return "1: files  2: notes  l/enter: open  h/esc: back  q: quit"
	case viewTodo:
		return "l/enter: open  a: all PRs  q: quit"
	default:
		return "l/enter: open  s: scrutiny  h/esc: back to todo  q: quit"
	}
}

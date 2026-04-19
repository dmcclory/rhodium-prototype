package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m model) viewDiff() string {
	if m.noting {
		return m.renderNotingView()
	}
	return m.diff.View()
}

// openFile loads a file into the diff view: parse hunks, seed marks from the
// brain, show patch view immediately, then kick off blob fetch for full file.
//
// If the PR head has moved since this file was last reviewed, we enter catch-up
// mode: fetch only the delta and show that instead of the full PR diff.
func (m *model) openFile(fc FileChange) tea.Cmd {
	m.selectedFile = fc.Path
	m.view = viewDiff
	m.blobContent = ""
	m.catchUpMode = false
	m.catchUpOldHead = ""
	m.catchUpOldBase = ""
	m.catchUpClass = ClassB1B2F1F2
	m.catchUpPatch = ""

	// Check if this file was previously reviewed at an older head/base.
	revState := m.brain.FileReviewedState(m.selectedPR.Repo, m.selectedPR.Number, fc.Path)
	// Scrutinized PRs always show the full diff — no catch-up shortcuts.
	scrutinized := m.brain.IsScrutinized(m.selectedPR.Repo, m.selectedPR.Number)
	needsCatchUp := !scrutinized && revState.HeadSHA != "" && (revState.HeadSHA != m.selectedPR.HeadSHA || revState.BaseSHA != m.selectedPR.BaseSHA)

	m.currentHunks = parseHunks(fc.Patch)
	m.currentMarks = m.brain.HunkMarks(m.selectedPR.Repo, m.selectedPR.Number, fc.Path)
	m.currentNotes = m.brain.NotesForFile(m.selectedPR.Repo, m.selectedPR.Number, fc.Path)
	m.hunkIdx = firstUnmarked(m.currentHunks, m.currentMarks)
	m.redrawDiff()
	m.jumpToCurrentHunk()

	var cmds []tea.Cmd

	if needsCatchUp {
		m.catchUpOldHead = revState.HeadSHA
		m.catchUpOldBase = revState.BaseSHA
		repo := m.selectedPR.Repo
		oldHead := revState.HeadSHA
		oldBase := revState.BaseSHA
		newHead := m.selectedPR.HeadSHA
		newBase := m.selectedPR.BaseSHA
		path := fc.Path
		rebased := oldBase != newBase && oldBase != ""

		if rebased {
			// Rebase detected — fetch file at all 4 corners and classify.
			m.statusMsg = fmt.Sprintf("classifying diamond (rebase %s→%s)", shortSHA(oldBase), shortSHA(newBase))
			cmds = append(cmds, func() tea.Msg {
				b1, _ := fetchFileAtRef(repo, path, oldBase)
				f1, _ := fetchFileAtRef(repo, path, oldHead)
				b2, _ := fetchFileAtRef(repo, path, newBase)
				f2, _ := fetchFileAtRef(repo, path, newHead)
				d := Diamond{B1: b1, F1: f1, B2: b2, F2: f2}
				class := Classify(d, nil)
				// Get the catch-up patch via compare for shown classes.
				var patch string
				if !class.Hidden() {
					views := class.Views()
					if len(views) > 0 {
						// Fetch compare diff for the primary view's corners.
						from := d.Get(views[0].From)
						to := d.Get(views[0].To)
						_ = from // We use the compare API for the patch.
						_ = to
					}
					// Use compare API between old tip and new tip for the patch.
					files, _ := fetchCompare(repo, oldHead, newHead)
					for _, f := range files {
						if f.Path == path {
							patch = f.Patch
							break
						}
					}
				}
				return diamondClassifiedMsg{path: path, class: class, diamond: d, patch: patch}
			})
		} else {
			// No rebase — use compare API (fast path, b1==b2).
			m.statusMsg = fmt.Sprintf("loading catch-up diff %s..%s", shortSHA(oldHead), shortSHA(newHead))
			cmds = append(cmds, func() tea.Msg {
				files, err := fetchCompare(repo, oldHead, newHead)
				return catchUpLoadedMsg{path: path, files: files, err: err}
			})
		}
	}

	if fc.Blob != "" {
		repo := m.selectedPR.Repo
		sha := fc.Blob
		cmds = append(cmds, func() tea.Msg {
			content, err := fetchBlob(repo, sha)
			return blobLoadedMsg{content: content, err: err}
		})
	}

	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

func (m *model) redrawDiff() {
	if len(m.currentHunks) == 0 {
		m.diff.SetContent("(no hunks — nothing to review)")
		m.hunkLines = nil
		return
	}
	var body string
	var lines []int
	var lmap []int
	if m.blobContent != "" {
		body, lines, lmap = renderFullFile(m.blobContent, m.currentHunks, m.currentMarks, m.hunkIdx, m.currentNotes, m.cursorLine)
	} else {
		body, lines, lmap = renderHunks(m.currentHunks, m.currentMarks, m.hunkIdx, m.currentNotes, m.cursorLine)
	}
	m.diff.SetContent(body)
	m.diffLines = strings.Split(body, "\n")
	m.hunkLines = lines
	m.lineMap = lmap
}

func (m *model) jumpToCurrentHunk() {
	if m.hunkIdx < 0 || m.hunkIdx >= len(m.hunkLines) {
		return
	}
	target := m.hunkLines[m.hunkIdx]
	m.cursorLine = target
	m.diff.SetYOffset(target)
}

func (m *model) allHunksMarked() bool {
	for _, h := range m.currentHunks {
		if !m.currentMarks[h.Hash] {
			return false
		}
	}
	return len(m.currentHunks) > 0
}

func (m *model) renderNotingView() string {
	_, v := appStyle.GetFrameSize()
	totalH := m.height - v - 1 // minus footer
	taHeight := m.noteInput.Height() + 2
	diffH := totalH - taHeight

	contentLines := m.diffLines

	// Where does the cursor sit relative to the current scroll?
	screenPos := m.cursorLine - m.diff.YOffset
	yOff := m.diff.YOffset

	// If cursor is too close to the bottom, scroll up to make room for the
	// textarea right under the cursor line.
	maxScreenPos := diffH - taHeight - 1
	if maxScreenPos < 0 {
		maxScreenPos = 0
	}
	if screenPos > maxScreenPos {
		yOff = m.cursorLine - maxScreenPos
		screenPos = maxScreenPos
	}
	if yOff < 0 {
		yOff = 0
	}

	// Lines above textarea: from yOff to cursor line (inclusive).
	aboveCount := screenPos + 1
	// Lines below textarea fill the rest.
	belowCount := diffH - aboveCount

	var b strings.Builder
	// Above section.
	for i := 0; i < aboveCount; i++ {
		idx := yOff + i
		if idx < len(contentLines) {
			b.WriteString(contentLines[idx])
		}
		b.WriteByte('\n')
	}
	// Textarea.
	b.WriteString(m.noteInput.View())
	b.WriteByte('\n')
	// Below section.
	belowStart := yOff + aboveCount
	for i := 0; i < belowCount; i++ {
		idx := belowStart + i
		if idx < len(contentLines) {
			b.WriteString(contentLines[idx])
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func (m *model) restoreDiffSize() {
	h, v := appStyle.GetFrameSize()
	m.diff.Width = m.width - h
	m.diff.Height = m.height - v - 1
}

func (m *model) moveCursor(delta int) {
	next := m.cursorLine + delta
	if next < 0 {
		next = 0
	}
	if max := len(m.lineMap) - 1; max >= 0 && next > max {
		next = max
	}
	m.cursorLine = next
	m.redrawDiff()
	// Scroll viewport to keep cursor visible.
	if m.cursorLine < m.diff.YOffset {
		m.diff.SetYOffset(m.cursorLine)
	} else if m.cursorLine >= m.diff.YOffset+m.diff.Height {
		m.diff.SetYOffset(m.cursorLine - m.diff.Height + 1)
	}
}

func (m *model) cursorFileLine() int {
	if m.cursorLine < 0 || m.cursorLine >= len(m.lineMap) {
		return 0
	}
	return m.lineMap[m.cursorLine]
}

func (m *model) cursorLineHash(lineNo int) string {
	if m.blobContent == "" {
		return ""
	}
	lines := strings.Split(m.blobContent, "\n")
	idx := lineNo - 1
	if idx < 0 || idx >= len(lines) {
		return ""
	}
	return hashLine(lines[idx])
}

// openInEditor launches the configured editor at the current hunk's location
// in a worktree dedicated to this PR. Returns a tea.Cmd that either suspends
// the TUI (no tmux) or spawns a tmux pane/window.
func (m *model) openInEditor() (tea.Cmd, error) {
	if m.selectedPR == nil || m.selectedFile == "" {
		return nil, fmt.Errorf("nothing selected")
	}
	worktree, err := resolveWorktree(m.cfg, m.selectedPR.Repo, m.selectedPR.Number)
	if err != nil {
		return nil, err
	}
	line := 1
	if m.hunkIdx >= 0 && m.hunkIdx < len(m.currentHunks) {
		if _, n := hunkLines(m.currentHunks[m.hunkIdx].Header); n > 0 {
			line = n
		}
	}
	prKey := fmt.Sprintf("%s#%d", m.selectedPR.Repo, m.selectedPR.Number)
	m.statusMsg = fmt.Sprintf("opening %s:%d in %s", m.selectedFile, line, worktree)
	return launchEditor(m.cfg, worktree, m.selectedFile, prKey, line), nil
}

// updateDiffNotingKeys handles keys while the note textarea is focused.
func (m *model) updateDiffNotingKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.noting = false
		m.noteInput.Blur()
		m.restoreDiffSize()
		return m, nil
	case "ctrl+d":
		body := strings.TrimSpace(m.noteInput.Value())
		m.noting = false
		m.noteInput.Blur()
		m.restoreDiffSize()
		if body != "" {
			if err := m.brain.SaveNote(m.selectedPR.Repo, m.selectedPR.Number, m.selectedFile, m.noteLineNo, m.noteLineHash, body); err != nil {
				m.statusMsg = "save note: " + err.Error()
			} else {
				m.currentNotes = m.brain.NotesForFile(m.selectedPR.Repo, m.selectedPR.Number, m.selectedFile)
				m.redrawDiff()
			}
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.noteInput, cmd = m.noteInput.Update(msg)
	return m, cmd
}

// updateDiffKeys handles keys in the diff view (not noting mode).
func (m *model) updateDiffKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc":
		m.view = viewFiles
		m.rebuildFileItems()
		m.rebuildPRItems()
		return m, nil
	case "n", "down", "tab":
		if len(m.currentHunks) > 0 && m.hunkIdx < len(m.currentHunks)-1 {
			m.hunkIdx++
			m.redrawDiff()
			m.jumpToCurrentHunk()
		}
		return m, nil
	case "p", "up", "shift+tab":
		if m.hunkIdx > 0 {
			m.hunkIdx--
			m.redrawDiff()
			m.jumpToCurrentHunk()
		}
		return m, nil
	case " ", "x":
		if m.hunkIdx >= 0 && m.hunkIdx < len(m.currentHunks) {
			h := m.currentHunks[m.hunkIdx]
			if m.currentMarks == nil {
				m.currentMarks = map[string]bool{}
			}
			if m.currentMarks[h.Hash] {
				delete(m.currentMarks, h.Hash)
			} else {
				m.currentMarks[h.Hash] = true
			}
			m.saveMarks()
			if m.hunkIdx < len(m.currentHunks)-1 {
				m.hunkIdx++
			}
			m.redrawDiff()
			m.jumpToCurrentHunk()
		}
		return m, nil
	case "h", "left":
		m.view = viewFiles
		m.rebuildFileItems()
		m.rebuildPRItems()
		return m, nil
	case "m":
		if m.currentMarks == nil {
			m.currentMarks = map[string]bool{}
		}
		for _, h := range m.currentHunks {
			m.currentMarks[h.Hash] = true
		}
		m.saveMarks()
		m.redrawDiff()
		m.jumpToCurrentHunk()
		return m, nil
	case "enter", "right":
		if m.allHunksMarked() {
			m.view = viewFiles
			m.rebuildFileItems()
			m.rebuildPRItems()
		}
		return m, nil
	case "j":
		m.moveCursor(1)
		return m, nil
	case "k":
		m.moveCursor(-1)
		return m, nil
	case "u":
		m.currentMarks = map[string]bool{}
		m.saveMarks()
		m.redrawDiff()
		m.hunkIdx = 0
		m.jumpToCurrentHunk()
		m.statusMsg = "cleared marks on " + m.selectedFile
		return m, nil
	case "d":
		if m.catchUpOldHead == "" {
			return m, nil
		}
		fc, ok := m.currentFile()
		if !ok {
			return m, nil
		}
		if m.catchUpMode {
			m.catchUpMode = false
			m.currentHunks = parseHunks(fc.Patch)
			m.currentMarks = m.brain.HunkMarks(m.selectedPR.Repo, m.selectedPR.Number, fc.Path)
			m.hunkIdx = firstUnmarked(m.currentHunks, m.currentMarks)
			m.statusMsg = "full diff  (d: catch-up diff)"
		} else {
			m.catchUpMode = true
			m.currentHunks = parseHunks(m.catchUpPatch)
			m.currentMarks = m.brain.HunkMarks(m.selectedPR.Repo, m.selectedPR.Number, fc.Path)
			m.hunkIdx = firstUnmarked(m.currentHunks, m.currentMarks)
			m.statusMsg = fmt.Sprintf("catch-up [%s]: changes since %s  (d: full diff)", m.catchUpClass, shortSHA(m.catchUpOldHead))
		}
		m.redrawDiff()
		m.jumpToCurrentHunk()
		return m, nil
	case "c":
		lineNo := m.cursorFileLine()
		if lineNo == 0 {
			m.statusMsg = "cursor not on a file line"
			return m, nil
		}
		m.noting = true
		m.noteLineNo = lineNo
		m.noteLineHash = m.cursorLineHash(lineNo)
		m.noteInput.Reset()
		return m, m.noteInput.Focus()
	case "o":
		cmd, err := m.openInEditor()
		if err != nil {
			m.statusMsg = "open: " + err.Error()
			return m, nil
		}
		return m, cmd
	}
	// Fall through to let the viewport handle scrolling keys.
	var cmd tea.Cmd
	m.diff, cmd = m.diff.Update(msg)
	return m, cmd
}

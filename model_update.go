package main

import (
	"fmt"
	"reflect"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// pollInterval controls how often the TUI re-reads brain state to pick up
// external mutations (e.g. an nvim instance in another tmux pane marking a
// hunk). SQLite reads are cheap; 500ms feels snappy without being wasteful.
const pollInterval = 500 * time.Millisecond

func pollTickCmd(gen int) tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg { return pollTickMsg{gen: gen} })
}

func (m *model) handlePRsLoaded(msg prsLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.statusMsg = "error: " + msg.err.Error()
		return m, nil
	}
	for _, p := range msg.prs {
		m.freshKeys[prKey(p.Repo, p.Number)] = true
	}
	added := m.mergePRs(msg.prs)
	m.rebuildPRItems()
	m.prs.Title = fmt.Sprintf("PRs (%d, loading files…)", len(m.allPRs))
	go m.brain.SetPRCache(m.allPRs)
	return m, prefetchAllCmd(added)
}

func (m *model) handleFilesLoaded(msg filesLoadedMsg) (tea.Model, tea.Cmd) {
	m.loadingFiles = false
	if msg.err != nil {
		m.statusMsg = "error: " + msg.err.Error()
		return m, nil
	}
	key := prKey(msg.pr.Repo, msg.pr.Number)
	m.prFiles[key] = msg.files
	m.rebuildPRItems()
	if m.selectedPR != nil && prKey(m.selectedPR.Repo, m.selectedPR.Number) == key {
		m.rebuildFileItems()
		m.files.Title = fmt.Sprintf("Files in %s#%d", msg.pr.Repo, msg.pr.Number)
	}
	pr := msg.pr
	files := msg.files
	if m.brain.IsScrutinized(pr.Repo, pr.Number) {
		return m, nil
	}
	return m, autoAdvanceCmd(m.brain, pr, files)
}

func (m *model) handleAutoAdvance(msg autoAdvanceMsg) (tea.Model, tea.Cmd) {
	if len(msg.advancedFiles) > 0 {
		m.rebuildPRItems()
		if m.selectedPR != nil && prKey(m.selectedPR.Repo, m.selectedPR.Number) == msg.prKey {
			m.rebuildFileItems()
			m.catchUpSession = m.brain.ActiveCatchUp(m.selectedPR.Repo, m.selectedPR.Number)
		}
		m.statusMsg = fmt.Sprintf("✓ auto-caught-up %d files", len(msg.advancedFiles))
	}
	return m, nil
}

func (m *model) handleCatchUpLoaded(msg catchUpLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.statusMsg = "catch-up: " + msg.err.Error()
		return m, nil
	}
	if m.view != viewDiff || m.selectedFile != msg.path {
		return m, nil
	}
	var deltaFC *FileChange
	for _, f := range msg.files {
		if f.Path == msg.path {
			deltaFC = &f
			break
		}
	}
	if deltaFC == nil || deltaFC.Patch == "" {
		m.catchUpMode = false
		m.catchUpClass = ClassB1B2__F1F2
		m.statusMsg = fmt.Sprintf("✓ %s: %s (auto-caught-up)", m.selectedFile, ClassB1B2__F1F2)
		m.brain.SetFileReviewed(m.selectedPR.Repo, m.selectedPR.Number, m.selectedFile, m.selectedPR.HeadSHA, m.selectedPR.BaseSHA)
		m.advanceCatchUpSession()
		return m, nil
	}
	m.catchUpMode = true
	m.catchUpClass = ClassB1B2
	m.catchUpPatch = deltaFC.Patch
	m.currentHunks = parseHunks(deltaFC.Patch)
	m.currentMarks = m.brain.HunkMarks(m.selectedPR.Repo, m.selectedPR.Number, m.selectedFile)
	m.hunkIdx = firstUnmarked(m.currentHunks, m.currentMarks)
	m.statusMsg = fmt.Sprintf("catch-up [%s]: f1→f2 since %s  (d: full diff)", ClassB1B2, shortSHA(m.catchUpOldHead))
	m.redrawDiff()
	m.jumpToCurrentHunk()
	return m, nil
}

func (m *model) handleDiamondClassified(msg diamondClassifiedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.statusMsg = "classify: " + msg.err.Error()
		return m, nil
	}
	if m.view != viewDiff || m.selectedFile != msg.path {
		return m, nil
	}
	m.catchUpClass = msg.class

	if msg.class.Hidden() {
		m.catchUpMode = false
		label := msg.class.String()
		if msg.class.IsForget() {
			label = "FORGET — base absorbed feature"
		}
		m.statusMsg = fmt.Sprintf("✓ %s: %s (auto-caught-up)", m.selectedFile, label)
		m.brain.SetFileReviewed(m.selectedPR.Repo, m.selectedPR.Number, m.selectedFile, m.selectedPR.HeadSHA, m.selectedPR.BaseSHA)
		m.advanceCatchUpSession()
		return m, nil
	}

	m.catchUpMode = true
	if msg.patch != "" {
		m.catchUpPatch = msg.patch
		m.currentHunks = parseHunks(msg.patch)
	}
	m.currentMarks = m.brain.HunkMarks(m.selectedPR.Repo, m.selectedPR.Number, m.selectedFile)
	m.hunkIdx = firstUnmarked(m.currentHunks, m.currentMarks)
	views := msg.class.Views()
	viewLabel := ""
	if len(views) > 0 {
		viewLabel = fmt.Sprintf("%s→%s", views[0].From, views[0].To)
	}
	m.statusMsg = fmt.Sprintf("catch-up [%s]: %s  (d: full diff)", msg.class, viewLabel)
	m.redrawDiff()
	m.jumpToCurrentHunk()
	return m, nil
}

func (m *model) handleBlobLoaded(msg blobLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.statusMsg = "blob: " + msg.err.Error()
		return m, nil
	}
	if m.view == viewDiff {
		m.blobContent = msg.content
		if !m.catchUpMode {
			m.redrawDiff()
			m.jumpToCurrentHunk()
		}
	}
	return m, nil
}

// handlePollTick re-reads the active PR's marks/notes from the brain. If
// anything changed since the last tick we rebuild items and redraw the diff
// so external writers (nvim in a separate tmux pane) show up. Reschedules
// itself as long as a PR is selected.
func (m *model) handlePollTick(msg pollTickMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.pollGen || m.selectedPR == nil {
		return m, nil
	}
	pr := *m.selectedPR
	changed := false

	if m.view == viewDiff && m.selectedFile != "" {
		newMarks := m.brain.HunkMarks(pr.Repo, pr.Number, m.selectedFile)
		if !reflect.DeepEqual(newMarks, m.currentMarks) {
			m.currentMarks = newMarks
			changed = true
		}
		newNotes := m.brain.NotesForFile(pr.Repo, pr.Number, m.selectedFile)
		if !reflect.DeepEqual(newNotes, m.currentNotes) {
			m.currentNotes = newNotes
			changed = true
		}
		if changed {
			m.redrawDiff()
		}
	}

	// Always rebuild item lists — cheap and catches per-file status flips that
	// don't affect the current diff buffer but change file-list glyphs.
	m.rebuildFileItems()
	m.rebuildPRItems()

	return m, pollTickCmd(m.pollGen)
}

// handleEditorDone runs after an external editor exits. For the tea.ExecProcess
// path this fires once the user quits nvim; for the tmux path it fires
// immediately after spawning the pane/window. In both cases we refresh the
// current PR's marks/notes so any changes made in nvim show up in the TUI.
func (m *model) handleEditorDone(msg editorDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.statusMsg = "editor: " + msg.err.Error()
		return m, nil
	}
	if m.selectedPR != nil {
		pr := *m.selectedPR
		if m.view == viewDiff && m.selectedFile != "" {
			m.currentMarks = m.brain.HunkMarks(pr.Repo, pr.Number, m.selectedFile)
			m.currentNotes = m.brain.NotesForFile(pr.Repo, pr.Number, m.selectedFile)
			m.redrawDiff()
		}
		m.rebuildFileItems()
		m.rebuildPRItems()
	}
	return m, nil
}

func (m *model) handlePrefetchDone() (tea.Model, tea.Cmd) {
	if len(m.freshKeys) > 0 {
		var live []PR
		for _, p := range m.allPRs {
			if m.freshKeys[prKey(p.Repo, p.Number)] {
				live = append(live, p)
			}
		}
		m.allPRs = live
		m.rebuildPRItems()
		go m.brain.SetPRCache(m.allPRs)
	}
	m.prs.Title = fmt.Sprintf("PRs (%d)", len(m.allPRs))
	return m, nil
}

// updateListKeys handles keys for the todo/PR/files list views.
func (m *model) updateListKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Tab switching in files view.
	if m.view == viewFiles && !listIsFiltering(*m) {
		switch msg.String() {
		case "1":
			m.fileTab = tabFiles
			return m, nil
		case "2":
			m.fileTab = tabNotes
			m.rebuildInfoVP()
			return m, nil
		}
	}

	// 'a' from the todo view drops into the full PR list.
	if m.view == viewTodo && !listIsFiltering(*m) && msg.String() == "a" {
		m.view = viewPRs
		return m, nil
	}

	// 's' toggles scrutiny on the selected PR.
	if m.view == viewPRs && !listIsFiltering(*m) && msg.String() == "s" {
		if it, ok := m.prs.SelectedItem().(prItem); ok {
			on := !it.scrutinized
			m.brain.SetScrutiny(it.pr.Repo, it.pr.Number, on)
			m.rebuildPRItems()
			if on {
				m.statusMsg = fmt.Sprintf("scrutiny ON for %s#%d — full diffs, no catch-up shortcuts", it.pr.Repo, it.pr.Number)
			} else {
				m.statusMsg = fmt.Sprintf("scrutiny OFF for %s#%d", it.pr.Repo, it.pr.Number)
			}
		}
		return m, nil
	}

	// vim-style h/l drill out/in as aliases for esc/enter.
	// Skip h/l while a filter is active so they're still typeable.
	switch msg.String() {
	case "ctrl+c", "q":
		if !listIsFiltering(*m) {
			return m, tea.Quit
		}
	case "esc", "h", "left":
		if msg.String() == "h" && listIsFiltering(*m) {
			break
		}
		switch m.view {
		case viewPRs:
			m.view = viewTodo
			return m, nil
		case viewFiles:
			m.fileTab = tabFiles
			if m.listViewOrigin == viewTodo {
				m.view = viewTodo
			} else {
				m.view = viewPRs
			}
			return m, nil
		}
	case "enter", "l", "right":
		if msg.String() == "l" && listIsFiltering(*m) {
			break
		}
		switch m.view {
		case viewTodo:
			if it, ok := m.todo.SelectedItem().(todoItem); ok {
				return m.openPR(it.pr)
			}
		case viewPRs:
			if it, ok := m.prs.SelectedItem().(prItem); ok {
				return m.openPR(it.pr)
			}
		case viewFiles:
			if m.fileTab != tabFiles {
				break
			}
			if it, ok := m.files.SelectedItem().(fileItem); ok {
				cmd := m.openFile(it.fc)
				return m, cmd
			}
		}
	}

	// Unmatched key — delegate to the active list/viewport widget.
	return m.delegateToWidget(msg)
}

// openPR transitions to the files view for pr, loading the file list if it
// isn't already cached. Shared between the todo and full PR list views.
func (m *model) openPR(pr PR) (tea.Model, tea.Cmd) {
	m.listViewOrigin = m.view // remember where to return on esc/h
	m.selectedPR = &pr
	m.catchUpSession = m.brain.ActiveCatchUp(pr.Repo, pr.Number)
	m.view = viewFiles
	m.rebuildDescVP()
	m.pollGen++ // invalidate any in-flight tick from a previous PR
	key := prKey(pr.Repo, pr.Number)
	if _, cached := m.prFiles[key]; cached {
		m.rebuildFileItems()
		m.files.Title = fmt.Sprintf("Files in %s#%d", pr.Repo, pr.Number)
		return m, pollTickCmd(m.pollGen)
	}
	m.loadingFiles = true
	m.files.Title = fmt.Sprintf("Files in %s#%d (loading...)", pr.Repo, pr.Number)
	m.files.SetItems(nil)
	return m, tea.Batch(loadFilesCmd(pr), pollTickCmd(m.pollGen))
}

// delegateToWidget passes an unhandled message to the active list or viewport.
func (m *model) delegateToWidget(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.view {
	case viewTodo:
		prev := m.todo.Index()
		m.todo, cmd = m.todo.Update(msg)
		skipSectionHeaders(&m.todo, prev)
	case viewPRs:
		prev := m.prs.Index()
		m.prs, cmd = m.prs.Update(msg)
		skipSectionHeaders(&m.prs, prev)
	case viewFiles:
		if m.fileTab != tabFiles {
			m.infoVP, cmd = m.infoVP.Update(msg)
		} else {
			prev := m.files.Index()
			m.files, cmd = m.files.Update(msg)
			skipSectionHeaders(&m.files, prev)
		}
	case viewDiff:
		m.diff, cmd = m.diff.Update(msg)
	}
	return m, cmd
}

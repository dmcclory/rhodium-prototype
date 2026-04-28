package diff

import (
	"fmt"

	corediff "rhodium/internal/diff"
	"rhodium/internal/tui/keys"

	tea "github.com/charmbracelet/bubbletea"
)

// updateKeys dispatches a keypress in non-noting mode. Falls back to the
// viewport so j/k and arrow keys scroll naturally when no binding matched.
func (m *Model) updateKeys(b Brain, msg tea.KeyMsg, globals []keys.Binding) tea.Cmd {
	if cmd, matched := keys.Dispatch(msg.String(), false, m.bindings(b), globals); matched {
		return cmd
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return cmd
}

// Bindings returns the non-noting bindings for help rendering. Noting and
// mention-picker modes show synthetic display-only bindings instead — see
// notingBindings / mentionPickerBindings.
//
// The diff view has three modes — base diff browsing, in-progress note,
// and the @-mention picker over the note. Each shows different shortcuts
// in help. Dispatch only ever consults the base table because Update
// routes to updateNotingKeys / updateMentionKeys before reaching the
// dispatch path in noting modes.
func (m *Model) Bindings() []keys.Binding {
	if m.noting {
		if m.mention.open {
			return mentionPickerBindings()
		}
		return notingBindings()
	}
	return m.bindings(nil)
}

// bindings returns the diff view's key table. brain may be nil — the
// help render path passes nil since dispatch isn't running, only the
// shape/desc strings matter; binding actions only fire from dispatch
// where brain is supplied.
func (m *Model) bindings(b Brain) []keys.Binding {
	out := make([]keys.Binding, 0, 16)
	out = append(out, m.navBindings()...)
	out = append(out, m.markBindings(b)...)
	out = append(out, m.noteBindings()...)
	out = append(out, m.viewBindings(b)...)
	out = append(out, m.AgentBindings...)
	return out
}

// navBindings: cursor + hunk movement, back, advance.
func (m *Model) navBindings() []keys.Binding {
	return []keys.Binding{
		{
			Name: "back", Keys: []string{"esc", "h", "left"},
			Desc: "back to files", Group: "Navigate",
			Action: func() tea.Cmd {
				return func() tea.Msg { return LeavingMsg{} }
			},
		},
		{
			Name: "next-hunk", Keys: []string{"n", "down", "tab"},
			Desc: "next hunk", Group: "Navigate",
			Action: func() tea.Cmd {
				m.stepHunk(1)
				m.redraw()
				m.jumpToHunk()
				return nil
			},
		},
		{
			Name: "prev-hunk", Keys: []string{"p", "up", "shift+tab"},
			Desc: "prev hunk", Group: "Navigate",
			Action: func() tea.Cmd {
				m.stepHunk(-1)
				m.redraw()
				m.jumpToHunk()
				return nil
			},
		},
		{
			Name: "cursor-down", Keys: []string{"j"},
			Desc: "cursor down", Group: "Navigate",
			Action: func() tea.Cmd { m.moveCursor(1); return nil },
		},
		{
			Name: "cursor-up", Keys: []string{"k"},
			Desc: "cursor up", Group: "Navigate",
			Action: func() tea.Cmd { m.moveCursor(-1); return nil },
		},
		{
			Name: "advance", Keys: []string{"enter", "right"},
			Desc: "back to files (when all marked)", Group: "Navigate",
			Action: func() tea.Cmd {
				if m.allMarked() {
					return func() tea.Msg { return LeavingMsg{} }
				}
				return nil
			},
		},
	}
}

// markBindings: toggle / mark-all / unmark-all on hunks.
func (m *Model) markBindings(b Brain) []keys.Binding {
	return []keys.Binding{
		{
			Name: "toggle-mark", Keys: []string{" ", "x"},
			Desc: "toggle hunk + advance", Group: "Mark",
			Action: func() tea.Cmd { return m.toggleMarkAtCursor(b) },
		},
		{
			Name: "mark-all", Keys: []string{"m"},
			Desc: "mark every hunk", Group: "Mark",
			Action: func() tea.Cmd { return m.markAll(b) },
		},
		{
			Name: "unmark-all", Keys: []string{"u"},
			Desc: "clear marks on file", Group: "Mark",
			Action: func() tea.Cmd { return m.unmarkAll(b) },
		},
	}
}

// noteBindings: publish to GitHub, add at cursor.
func (m *Model) noteBindings() []keys.Binding {
	return []keys.Binding{
		{
			Name: "publish-note", Keys: []string{"P"},
			Desc: "publish note at cursor to GitHub", Group: "Notes",
			Action: func() tea.Cmd { return m.publishNoteAtCursor() },
		},
		{
			Name: "note", Keys: []string{"c"},
			Desc: "add note at cursor", Group: "Notes",
			Action: func() tea.Cmd { return m.startNoteAtCursor() },
		},
	}
}

// viewBindings: cycle segment, catch-up toggle, open-editor.
func (m *Model) viewBindings(b Brain) []keys.Binding {
	return []keys.Binding{
		{
			Name: "cycle-view", Keys: []string{"v"},
			Desc: "cycle segment view", Group: "View",
			Action: func() tea.Cmd { return m.cycleSegmentView() },
		},
		{
			Name: "catch-up-toggle", Keys: []string{"d"},
			Desc: "toggle catch-up / full diff", Group: "View",
			Action: func() tea.Cmd { return m.toggleCatchUp(b) },
		},
		{
			Name: "open-editor", Keys: []string{"o"},
			Desc: "open file in editor", Group: "View",
			Action: func() tea.Cmd { return m.emitOpenEditor() },
		},
	}
}

// --- action helpers ---

func (m *Model) toggleMarkAtCursor(b Brain) tea.Cmd {
	if m.hunkIdx < 0 || m.hunkIdx >= len(m.hunks) {
		return nil
	}
	h := m.hunks[m.hunkIdx]
	if !h.IsMarkable() {
		// Cursor is on a synthetic segment header — just advance.
		m.stepHunk(1)
		m.redraw()
		m.jumpToHunk()
		return nil
	}
	if m.marks == nil {
		m.marks = map[string]bool{}
	}
	if m.marks[h.Hash] {
		delete(m.marks, h.Hash)
	} else {
		m.marks[h.Hash] = true
	}
	cmd := m.saveMarks(b)
	m.stepHunk(1)
	m.redraw()
	m.jumpToHunk()
	return cmd
}

func (m *Model) markAll(b Brain) tea.Cmd {
	if m.marks == nil {
		m.marks = map[string]bool{}
	}
	for _, h := range m.hunks {
		if !h.IsMarkable() {
			continue
		}
		m.marks[h.Hash] = true
	}
	cmd := m.saveMarks(b)
	m.redraw()
	m.jumpToHunk()
	return cmd
}

func (m *Model) unmarkAll(b Brain) tea.Cmd {
	m.marks = map[string]bool{}
	saveCmd := m.saveMarks(b)
	m.redraw()
	m.hunkIdx = 0
	m.jumpToHunk()
	path := m.file
	return tea.Batch(saveCmd, statusCmd("cleared marks on "+path))
}

func (m *Model) startNoteAtCursor() tea.Cmd {
	// In segmented mode, notes key off new-file (F2) line numbers
	// — segments rendered under any other view (b1→b2, f1→f2, etc.)
	// don't have a clean F2 mapping, so block them. Most primary views
	// do end at F2; this is only a limitation for a handful of classes.
	if view, ok := m.currentSegmentView(); ok && view.To != corediff.F2 {
		return statusCmd(fmt.Sprintf("notes are only supported on F2 views (this segment: %s→%s)", view.From, view.To))
	}
	lineNo := m.cursorFileLine()
	if lineNo == 0 {
		return statusCmd("cursor not on a file line")
	}
	m.noting = true
	m.noteLineNo = lineNo
	m.noteLineHash = m.cursorLineHash(lineNo)
	m.noteInput.Reset()
	return m.noteInput.Focus()
}

func (m *Model) cycleSegmentView() tea.Cmd {
	if !m.segmented || len(m.segments) == 0 {
		return nil
	}
	if corediff.MaxSegmentViews(m.segments) <= 1 {
		return statusCmd("no alternate views for these segments")
	}
	m.segmentViewIdx++
	m.hunks = corediff.SegmentHunks(m.segments, m.segmentViewIdx)
	m.hunkIdx = firstUnmarked(m.hunks, m.marks)
	m.cursorLine = 0
	maxV := corediff.MaxSegmentViews(m.segments)
	m.redraw()
	m.jumpToHunk()
	return statusCmd(fmt.Sprintf("view %d/%d", (m.segmentViewIdx%maxV)+1, maxV))
}

func (m *Model) toggleCatchUp(b Brain) tea.Cmd {
	if m.catchUpOldHead == "" || m.pr == nil {
		return nil
	}
	if m.catchUpMode {
		m.catchUpMode = false
		m.segmented = false
		m.hunks = corediff.ParseHunks(m.fullPatch)
		m.marks = b.HunkMarks(m.pr.Repo, m.pr.Number, m.file)
		m.hunkIdx = firstUnmarked(m.hunks, m.marks)
		m.redraw()
		m.jumpToHunk()
		return statusCmd("full diff  (d: catch-up diff)")
	}
	m.catchUpMode = true
	var status string
	if len(m.segments) > 0 {
		m.hunks = corediff.SegmentHunks(m.segments, m.segmentViewIdx)
		m.segmented = true
		status = fmt.Sprintf("catch-up [%s]: %d segments since %s  (d: full diff)", m.catchUpClass, len(m.segments), shortSHA(m.catchUpOldHead))
	} else {
		m.hunks = corediff.ParseHunks(m.catchUpPatch)
		m.segmented = false
		status = fmt.Sprintf("catch-up [%s]: changes since %s  (d: full diff)", m.catchUpClass, shortSHA(m.catchUpOldHead))
	}
	m.marks = b.HunkMarks(m.pr.Repo, m.pr.Number, m.file)
	m.hunkIdx = firstUnmarked(m.hunks, m.marks)
	m.redraw()
	m.jumpToHunk()
	return statusCmd(status)
}

func (m *Model) emitOpenEditor() tea.Cmd {
	if m.pr == nil || m.file == "" {
		return nil
	}
	line := 1
	if m.hunkIdx >= 0 && m.hunkIdx < len(m.hunks) {
		if _, n := hunkLines(m.hunks[m.hunkIdx].Header); n > 0 {
			line = n
		}
	}
	pr := *m.pr
	file := m.file
	return func() tea.Msg { return OpenEditorMsg{PR: pr, File: file, Line: line} }
}

// notingBindings are display-only entries shown in the help overlay while
// the user is composing a note. The actual key handling lives in
// updateNotingKeys; Action is nil because dispatch never sees these.
func notingBindings() []keys.Binding {
	return []keys.Binding{
		{Name: "save-note", Keys: []string{"ctrl+d"}, Desc: "save note", Group: "Notes"},
		{Name: "cancel-note", Keys: []string{"esc"}, Desc: "cancel without saving", Group: "Notes"},
		{Name: "mention", Keys: []string{"@"}, Desc: "open @-mention picker (at word boundary)", Group: "Notes"},
	}
}

// mentionPickerBindings are display-only entries shown in the help
// overlay while the @-mention picker is open over the note textarea.
// Routing lives in updateMentionKeys.
func mentionPickerBindings() []keys.Binding {
	return []keys.Binding{
		{Name: "mention-nav", Keys: []string{"up", "down"}, Desc: "move selection", Group: "Mention"},
		{Name: "mention-filter", Keys: []string{"a-z, 0-9, -, _"}, Desc: "type to filter contributors", Group: "Mention"},
		{Name: "mention-accept", Keys: []string{"enter"}, Desc: "insert @login and close", Group: "Mention"},
		{Name: "mention-close", Keys: []string{"esc"}, Desc: "close picker, leave text untouched", Group: "Mention"},
		{Name: "mention-backspace", Keys: []string{"backspace"}, Desc: "delete a query char (or @ to close)", Group: "Mention"},
	}
}

// publishNoteAtCursor identifies the first unpublished note at the
// cursor line and emits a PublishNoteMsg for the app to POST to GitHub.
//
// GitHub anchors inline comments to a (commit_id, path, line) tuple, and
// only accepts commits that are part of the PR. We use the PR's current
// HeadSHA; if the PR is rebased later, old comments outline themselves
// (GitHub's normal behaviour).
func (m *Model) publishNoteAtCursor() tea.Cmd {
	if m.pr == nil || m.file == "" {
		return nil
	}
	lineNo := m.cursorFileLine()
	if lineNo == 0 {
		return statusCmd("cursor not on a file line")
	}
	for i := range m.notes {
		n := m.notes[i]
		if n.LineNo == lineNo && n.GitHubCommentID == 0 {
			pr := *m.pr
			file := m.file
			return tea.Batch(
				statusCmd(fmt.Sprintf("publishing note on %s:%d…", file, lineNo)),
				func() tea.Msg {
					return PublishNoteMsg{
						NoteID: n.ID,
						PR:     pr,
						Path:   file,
						Line:   lineNo,
						Body:   n.Body,
						Commit: pr.HeadSHA,
					}
				},
			)
		}
	}
	return statusCmd(fmt.Sprintf("no unpublished note on line %d", lineNo))
}

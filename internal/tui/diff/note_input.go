package diff

import (
	"strings"
	"unicode"

	"rhodium/internal/tui/styles"

	tea "github.com/charmbracelet/bubbletea"
)

// updateNotingKeys handles keypresses while the note input is focused.
// Mention picker is modal over the textarea: when open, all keys route
// through updateMentionKeys.
func (m *Model) updateNotingKeys(b Brain, msg tea.KeyMsg) tea.Cmd {
	if m.mention.open {
		return m.updateMentionKeys(msg)
	}
	switch msg.String() {
	case "esc":
		m.noting = false
		m.replyToID = 0
		m.replyToAuthor = ""
		m.noteUrgency = ""
		m.noteAssignee = ""
		m.noteInput.Blur()
		m.noteInput.Placeholder = m.notePlaceholder()
		m.restoreSize()
		return nil
	case "!":
		m.noteUrgency = m.noteUrgency.Next()
		m.noteInput.Placeholder = m.notePlaceholder()
		return nil
	case "ctrl+d":
		body := strings.TrimSpace(m.noteInput.Value())
		replyTo := m.replyToID
		urgency := m.noteUrgency
		assignee := m.noteAssignee
		m.noting = false
		m.replyToID = 0
		m.replyToAuthor = ""
		m.noteUrgency = ""
		m.noteAssignee = ""
		m.noteInput.Blur()
		m.noteInput.Placeholder = m.notePlaceholder()
		m.restoreSize()
		if body == "" || m.pr == nil {
			return nil
		}
		if replyTo != 0 {
			pr := *m.pr
			return tea.Batch(
				statusCmd("sending reply…"),
				func() tea.Msg {
					return ReplyInlineMsg{PR: pr, ReplyToID: replyTo, Body: body}
				},
			)
		}
		var err error
		if urgency != "" || assignee != "" {
			err = b.SaveNoteWithUrgency(m.pr.Repo, m.pr.Number, m.file, m.noteLineNo, m.noteLineHash, body, urgency, assignee)
		} else {
			err = b.SaveNote(m.pr.Repo, m.pr.Number, m.file, m.noteLineNo, m.noteLineHash, body)
		}
		if err != nil {
			return statusCmd("save note: " + err.Error())
		}
		m.notes = b.NotesForFile(m.pr.Repo, m.pr.Number, m.file)
		m.redraw()
		return nil
	case "@":
		// Check boundary *before* the textarea inserts the @ — afterwards
		// the char before the cursor is the @ itself.
		trigger := m.atMentionBoundary()
		var cmd tea.Cmd
		m.noteInput, cmd = m.noteInput.Update(msg)
		if !trigger {
			return cmd
		}
		if openCmd := m.openMentionPicker(); openCmd != nil {
			return tea.Batch(cmd, openCmd)
		}
		return cmd
	}
	var cmd tea.Cmd
	m.noteInput, cmd = m.noteInput.Update(msg)
	return cmd
}

// atMentionBoundary reports whether the cursor is at a spot where typing
// `@` should open the mention picker — i.e. at the start of a line or
// right after whitespace. This keeps email-like text ("foo@bar.com") from
// triggering the picker.
func (m *Model) atMentionBoundary() bool {
	li := m.noteInput.LineInfo()
	col := li.StartColumn + li.ColumnOffset
	if col <= 0 {
		return true
	}
	row := m.noteInput.Line()
	lines := strings.Split(m.noteInput.Value(), "\n")
	if row < 0 || row >= len(lines) {
		return true
	}
	runes := []rune(lines[row])
	if col > len(runes) {
		return true
	}
	return unicode.IsSpace(runes[col-1])
}

// renderNotingView paints the diff body with the note textarea splicing
// in below the cursor line. The viewport is re-aimed if the cursor is
// near the bottom so the textarea always has room.
func (m *Model) renderNotingView() string {
	_, padV := styles.App.GetFrameSize()
	totalH := m.height - padV - 1 // minus footer
	taHeight := m.noteInput.Height() + 2
	diffH := totalH - taHeight

	contentLines := m.diffLines

	screenPos := m.cursorLine - m.vp.YOffset
	yOff := m.vp.YOffset

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

	aboveCount := screenPos + 1
	belowCount := diffH - aboveCount

	var b strings.Builder
	for i := 0; i < aboveCount; i++ {
		idx := yOff + i
		if idx < len(contentLines) {
			b.WriteString(contentLines[idx])
		}
		b.WriteByte('\n')
	}
	b.WriteString(m.noteInput.View())
	b.WriteByte('\n')
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

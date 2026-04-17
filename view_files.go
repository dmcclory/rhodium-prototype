package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
)

const descPaneHeight = 8

// sizeFilesView splits available height between the file list and description pane.
func (m *model) sizeFilesView(w, totalH int) {
	// 1 line for tab bar, 1 line for the separator.
	descH := descPaneHeight
	filesH := totalH - descH - 2
	if filesH < 4 {
		filesH = 4
		descH = totalH - filesH - 2
	}
	m.files.SetSize(w, filesH)
	m.descVP.Width = w
	m.descVP.Height = descH
}

func (m model) viewFiles() string {
	body := m.tabBar()
	switch m.fileTab {
	case tabFiles:
		body += m.files.View()
	case tabNotes:
		body += m.infoVP.View()
		return body // notes tab takes the full space, no description pane
	}
	// Description pane below the file list.
	body += "\n" + lipgloss.NewStyle().Faint(true).Render(strings.Repeat("─", m.descVP.Width)) + "\n"
	body += m.descVP.View()
	return body
}

func (m *model) rebuildFileItems() {
	if m.selectedPR == nil {
		return
	}
	var savedPath string
	if sel, ok := m.files.SelectedItem().(fileItem); ok {
		savedPath = sel.fc.Path
	}
	files := m.prFiles[prKey(m.selectedPR.Repo, m.selectedPR.Number)]
	reviewedStates := m.brain.AllFileReviewedStates(m.selectedPR.Repo, m.selectedPR.Number)
	var unseen, partial, seen []fileItem
	for _, fc := range files {
		status := m.brain.Status(m.selectedPR.Repo, m.selectedPR.Number, fc)
		nc := m.brain.NoteCountForFile(m.selectedPR.Repo, m.selectedPR.Number, fc.Path)
		s := reviewedStates[fc.Path]
		catchUp := s.HeadSHA != "" && (s.HeadSHA != m.selectedPR.HeadSHA || s.BaseSHA != m.selectedPR.BaseSHA)
		fi := fileItem{fc: fc, status: status, noteCount: nc, needsCatchUp: catchUp}
		switch status {
		case StatusUnseen:
			unseen = append(unseen, fi)
		case StatusPartial:
			partial = append(partial, fi)
		case StatusSeen:
			seen = append(seen, fi)
		}
	}
	var items []list.Item
	needSep := false
	if len(partial) > 0 {
		items = append(items, sectionItem{label: "── in progress ──"})
		for _, fi := range partial {
			items = append(items, fi)
		}
		needSep = true
	}
	if len(unseen) > 0 {
		if needSep {
			items = append(items, sectionItem{label: "── unseen ──"})
		}
		for _, fi := range unseen {
			items = append(items, fi)
		}
		needSep = true
	}
	if len(seen) > 0 {
		if needSep {
			items = append(items, sectionItem{label: "── seen ──"})
		}
		for _, fi := range seen {
			items = append(items, fi)
		}
	}
	m.files.SetItems(items)
	if savedPath != "" {
		for i, it := range items {
			if fi, ok := it.(fileItem); ok && fi.fc.Path == savedPath {
				m.files.Select(i)
				break
			}
		}
	}
}

var (
	tabActiveStyle   = lipgloss.NewStyle().Bold(true).Underline(true)
	tabInactiveStyle = lipgloss.NewStyle().Faint(true)
)

func (m *model) tabBar() string {
	tabs := []struct {
		label string
		t     fileTab
	}{
		{"[1] Files", tabFiles},
		{"[2] Notes", tabNotes},
	}
	var parts []string
	for _, tab := range tabs {
		if tab.t == m.fileTab {
			parts = append(parts, tabActiveStyle.Render(tab.label))
		} else {
			parts = append(parts, tabInactiveStyle.Render(tab.label))
		}
	}
	return strings.Join(parts, "  ") + "\n"
}

// rebuildDescVP populates the always-visible description pane.
func (m *model) rebuildDescVP() {
	if m.selectedPR == nil {
		return
	}
	body := m.selectedPR.Body
	if body == "" {
		body = "(no description)"
	}
	content := fmt.Sprintf("%s#%d  %s  @%s\n\n%s",
		m.selectedPR.Repo, m.selectedPR.Number, m.selectedPR.Title, m.selectedPR.Author, body)
	m.descVP.SetContent(content)
	m.descVP.GotoTop()
}

func (m *model) rebuildInfoVP() {
	if m.selectedPR == nil {
		return
	}
	var content string
	switch m.fileTab {
	case tabNotes:
		notes := m.brain.NotesForPR(m.selectedPR.Repo, m.selectedPR.Number)
		if len(notes) == 0 {
			content = "(no notes)"
		} else {
			key := prKey(m.selectedPR.Repo, m.selectedPR.Number)
			fileLinesCache := map[string][]string{}
			getFileLines := func(path string) []string {
				if cached, ok := fileLinesCache[path]; ok {
					return cached
				}
				lines := m.patchNewFileLines(key, path)
				fileLinesCache[path] = lines
				return lines
			}

			var b strings.Builder
			curPath := ""
			for _, n := range notes {
				if n.Path != curPath {
					if curPath != "" {
						b.WriteByte('\n')
					}
					curPath = n.Path
					b.WriteString(lipgloss.NewStyle().Bold(true).Render(curPath) + "\n")
				}
				// Context lines around the note.
				fLines := getFileLines(n.Path)
				idx := n.LineNo - 1
				ctxStart := idx - 2
				if ctxStart < 0 {
					ctxStart = 0
				}
				ctxEnd := idx + 3
				if ctxEnd > len(fLines) {
					ctxEnd = len(fLines)
				}
				for i := ctxStart; i < ctxEnd; i++ {
					lineStr := fmt.Sprintf("  %4d  %s", i+1, fLines[i])
					if i == idx {
						lineStr = lipgloss.NewStyle().Bold(true).Render(lineStr)
					} else {
						lineStr = lipgloss.NewStyle().Faint(true).Render(lineStr)
					}
					b.WriteString(lineStr + "\n")
				}
				b.WriteString(noteStyle.Render("  "+strings.Repeat(" ", 4)+"  RH: "+n.Body) + "\n")
			}
			content = b.String()
		}
	}
	m.infoVP.SetContent(content)
	m.infoVP.GotoTop()
}

// patchNewFileLines reconstructs the new-file lines visible in a patch's hunks.
// Returns a sparse slice indexed by 1-based line number. Lines not covered by
// any hunk are empty strings (best effort — we may not have the full file).
func (m *model) patchNewFileLines(key, path string) []string {
	files := m.prFiles[key]
	var patch string
	for _, f := range files {
		if f.Path == path {
			patch = f.Patch
			break
		}
	}
	if patch == "" {
		return nil
	}
	hunks := parseHunks(patch)
	// Find max line to size the slice.
	maxLine := 0
	for _, h := range hunks {
		r := parseHunkRange(h.Header)
		end := r.newStart + r.newCount
		if end > maxLine {
			maxLine = end
		}
	}
	lines := make([]string, maxLine+1)
	for _, h := range hunks {
		r := parseHunkRange(h.Header)
		cur := r.newStart
		for _, line := range h.BodyLines {
			if len(line) == 0 {
				if cur < len(lines) {
					lines[cur] = ""
				}
				cur++
				continue
			}
			switch line[0] {
			case '-':
				// deleted from old file, not in new
			case '+':
				if cur < len(lines) {
					lines[cur] = line[1:]
				}
				cur++
			default:
				if cur < len(lines) {
					text := line
					if len(text) > 0 && text[0] == ' ' {
						text = text[1:]
					}
					lines[cur] = text
				}
				cur++
			}
		}
	}
	return lines
}

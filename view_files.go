package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const descPaneHeight = 8

// --- filesView ---

type fileTab int

const (
	tabFiles fileTab = iota
	tabNotes
)

type filesView struct {
	list         list.Model
	infoVP       viewport.Model
	descVP       viewport.Model
	tab          fileTab
	loadingFiles bool
}

func newFilesView() filesView {
	l := list.New(nil, compactDelegate(), 0, 0)
	l.Title = "Files"
	return filesView{
		list:   l,
		infoVP: viewport.New(0, 0),
		descVP: viewport.New(0, 0),
	}
}

// Resize splits available height between the file list and description
// pane. Width is shared.
func (v *filesView) Resize(w, totalH int) {
	descH := descPaneHeight
	filesH := totalH - descH - 2 // tab bar + separator
	if filesH < 4 {
		filesH = 4
		descH = totalH - filesH - 2
	}
	v.list.SetSize(w, filesH)
	v.descVP.Width = w
	v.descVP.Height = descH
	v.infoVP.Width = w
	v.infoVP.Height = totalH
}

func (v *filesView) View(a *app) string {
	body := v.tabBar()
	switch v.tab {
	case tabFiles:
		body += v.list.View()
	case tabNotes:
		body += v.infoVP.View()
		return body
	}
	body += "\n" + lipgloss.NewStyle().Faint(true).Render(strings.Repeat("─", v.descVP.Width)) + "\n"
	body += v.descVP.View()
	return body
}

func (v *filesView) Footer(a *app) string {
	return "1: files  2: notes  l/enter: open  h/esc: back  q: quit"
}

func (v *filesView) Update(a *app, msg tea.Msg) tea.Cmd {
	key, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return v.delegate(msg)
	}

	filtering := v.list.FilterState() == list.Filtering

	if !filtering {
		switch key.String() {
		case "1":
			v.tab = tabFiles
			return nil
		case "2":
			v.tab = tabNotes
			v.rebuildInfoVP(a)
			return nil
		}
	}

	switch key.String() {
	case "ctrl+c", "q":
		if !filtering {
			return tea.Quit
		}
	case "esc", "h", "left":
		if key.String() == "h" && filtering {
			break
		}
		v.tab = tabFiles
		if a.listViewOrigin == viewTodo {
			a.activeView = viewTodo
		} else {
			a.activeView = viewPRs
		}
		return nil
	case "enter", "l", "right":
		if key.String() == "l" && filtering {
			break
		}
		if v.tab != tabFiles {
			break
		}
		if it, ok := v.list.SelectedItem().(fileItem); ok {
			return a.openFile(it.fc)
		}
	}
	if !filtering {
		if cmd, matched := tryAction(a, key.String()); matched {
			return cmd
		}
	}
	return v.delegate(msg)
}

func (v *filesView) delegate(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	if v.tab != tabFiles {
		v.infoVP, cmd = v.infoVP.Update(msg)
		return cmd
	}
	prev := v.list.Index()
	v.list, cmd = v.list.Update(msg)
	skipSectionHeaders(&v.list, prev)
	return cmd
}

func (v *filesView) rebuild(a *app) {
	if a.selectedPR == nil {
		return
	}
	var savedPath string
	if sel, ok := v.list.SelectedItem().(fileItem); ok {
		savedPath = sel.fc.Path
	}
	files := a.prFiles[prKey(a.selectedPR.Repo, a.selectedPR.Number)]
	reviewedStates := a.brain.AllFileReviewedStates(a.selectedPR.Repo, a.selectedPR.Number)
	var unseen, partial, seen []fileItem
	for _, fc := range files {
		status := a.brain.Status(a.selectedPR.Repo, a.selectedPR.Number, fc)
		nc := a.brain.NoteCountForFile(a.selectedPR.Repo, a.selectedPR.Number, fc.Path)
		s := reviewedStates[fc.Path]
		catchUp := s.HeadSHA != "" && (s.HeadSHA != a.selectedPR.HeadSHA || s.BaseSHA != a.selectedPR.BaseSHA)
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
	v.list.SetItems(items)
	if savedPath != "" {
		for i, it := range items {
			if fi, ok := it.(fileItem); ok && fi.fc.Path == savedPath {
				v.list.Select(i)
				break
			}
		}
	}
}

var (
	tabActiveStyle   = lipgloss.NewStyle().Bold(true).Underline(true)
	tabInactiveStyle = lipgloss.NewStyle().Faint(true)
)

func (v *filesView) tabBar() string {
	tabs := []struct {
		label string
		t     fileTab
	}{
		{"[1] Files", tabFiles},
		{"[2] Notes", tabNotes},
	}
	var parts []string
	for _, tab := range tabs {
		if tab.t == v.tab {
			parts = append(parts, tabActiveStyle.Render(tab.label))
		} else {
			parts = append(parts, tabInactiveStyle.Render(tab.label))
		}
	}
	return strings.Join(parts, "  ") + "\n"
}

// rebuildDescVP populates the always-visible description pane.
func (v *filesView) rebuildDescVP(a *app) {
	if a.selectedPR == nil {
		return
	}
	body := a.selectedPR.Body
	if body == "" {
		body = "(no description)"
	}
	content := fmt.Sprintf("%s#%d  %s  @%s\n\n%s",
		a.selectedPR.Repo, a.selectedPR.Number, a.selectedPR.Title, a.selectedPR.Author, body)
	v.descVP.SetContent(content)
	v.descVP.GotoTop()
}

func (v *filesView) rebuildInfoVP(a *app) {
	if a.selectedPR == nil {
		return
	}
	var content string
	switch v.tab {
	case tabNotes:
		notes := a.brain.NotesForPR(a.selectedPR.Repo, a.selectedPR.Number, NotesActive)
		if len(notes) == 0 {
			content = "(no notes)"
		} else {
			key := prKey(a.selectedPR.Repo, a.selectedPR.Number)
			fileLinesCache := map[string][]string{}
			getFileLines := func(path string) []string {
				if cached, ok := fileLinesCache[path]; ok {
					return cached
				}
				lines := patchNewFileLines(a, key, path)
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
	v.infoVP.SetContent(content)
	v.infoVP.GotoTop()
}

func (v *filesView) filtering() bool { return v.list.FilterState() == list.Filtering }

// patchNewFileLines reconstructs the new-file lines visible in a patch's
// hunks. Returns a sparse slice indexed by 1-based line number. Lines
// not covered by any hunk are empty strings.
func patchNewFileLines(a *app, key, path string) []string {
	files := a.prFiles[key]
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
				// deleted from old, not in new
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

// --- fileItem ---

type fileItem struct {
	fc           FileChange
	status       FileStatus
	noteCount    int
	needsCatchUp bool // PR head moved since this file was last reviewed
}

func (i fileItem) Title() string {
	s := fmt.Sprintf("%s %s  +%d -%d", i.status.Glyph(), i.fc.Path, i.fc.Additions, i.fc.Deletions)
	if i.needsCatchUp {
		s += "  ↻"
	}
	if i.noteCount > 0 {
		s += fmt.Sprintf("  (%d notes)", i.noteCount)
	}
	return s
}
func (i fileItem) Description() string { return "" }
func (i fileItem) FilterValue() string { return i.fc.Path }

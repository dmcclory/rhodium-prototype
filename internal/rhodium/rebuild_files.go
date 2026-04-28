package rhodium

import (
	"fmt"
	"strings"

	"rhodium/internal/brain"
	"rhodium/internal/diff"
	tuidiff "rhodium/internal/tui/diff"
	"rhodium/internal/tui/files"

	"github.com/charmbracelet/lipgloss"
)

// filesNoteStyle paints the trailing "RH: <body>" preview under each
// note in the files-view notes tab. Same color as the diff view's note
// rendering, kept local so the rebuild_files helpers don't depend on the
// diff package's internal style table.
var filesNoteStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))

// rebuildFiles walks the cached file list for the selected PR and emits a
// files.Item per change. Status, note count, and catch-up flag come from
// the brain — centralized here so the files package can stay dumb about
// brain state.
func (a *app) rebuildFiles() {
	if a.session.selectedPR == nil {
		return
	}
	pr := a.session.selectedPR
	cached := a.cache.prFiles[brain.PRKey(pr.Repo, pr.Number)]
	reviewedStates := a.brain.AllFileReviewedStates(pr.Repo, pr.Number)
	items := make([]files.Item, 0, len(cached))
	for _, fc := range cached {
		status := a.brain.Status(pr.Repo, pr.Number, fc)
		nc := a.brain.NoteCountForFile(pr.Repo, pr.Number, fc.Path)
		s := reviewedStates[fc.Path]
		catchUp := s.HeadSHA != "" && (s.HeadSHA != pr.HeadSHA || s.BaseSHA != pr.BaseSHA)
		items = append(items, files.Item{
			File:         fc,
			Status:       status,
			NoteCount:    nc,
			NeedsCatchUp: catchUp,
		})
	}
	a.files.Rebuild(items)
}

// rebuildFilesDesc renders the always-visible PR description pane content
// and pushes it into the files view.
func (a *app) rebuildFilesDesc() {
	if a.session.selectedPR == nil {
		return
	}
	pr := a.session.selectedPR
	body := pr.Body
	if body == "" {
		body = "(no description)"
	}
	content := fmt.Sprintf("%s#%d  %s  @%s\n\n%s",
		pr.Repo, pr.Number, pr.Title, pr.Author, body)
	a.files.SetDescription(content)
}

// rebuildFilesNotes renders the notes-tab content and pushes it into the
// files view. Triggered by files.RebuildNotesMsg when the user toggles to
// the notes tab — kept lazy because reconstructing the surrounding code
// context for every note is non-trivial.
func (a *app) rebuildFilesNotes() {
	if a.session.selectedPR == nil {
		return
	}
	pr := a.session.selectedPR
	notes := a.brain.NotesForPR(pr.Repo, pr.Number, brain.NotesActive)
	if len(notes) == 0 {
		a.files.SetNotes("(no notes)")
		return
	}
	key := brain.PRKey(pr.Repo, pr.Number)
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
		b.WriteString(filesNoteStyle.Render("  "+strings.Repeat(" ", 4)+"  RH: "+n.Body) + "\n")
	}
	a.files.SetNotes(b.String())
}

// patchNewFileLines reconstructs the new-file lines visible in a patch's
// hunks. Returns a sparse slice indexed by 1-based line number. Lines not
// covered by any hunk are empty strings.
func patchNewFileLines(a *app, key, path string) []string {
	cached := a.cache.prFiles[key]
	var patch string
	for _, f := range cached {
		if f.Path == path {
			patch = f.Patch
			break
		}
	}
	if patch == "" {
		return nil
	}
	hunks := diff.ParseHunks(patch)
	maxLine := 0
	for _, h := range hunks {
		start, count := tuidiff.ParseHunkRange(h.Header)
		end := start + count
		if end > maxLine {
			maxLine = end
		}
	}
	lines := make([]string, maxLine+1)
	for _, h := range hunks {
		start, _ := tuidiff.ParseHunkRange(h.Header)
		cur := start
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

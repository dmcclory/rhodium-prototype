package diff

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"rhodium/internal/brain"
	corediff "rhodium/internal/diff"
	"rhodium/internal/gh"

	"github.com/charmbracelet/lipgloss"
)

var (
	focusedHunkStyle = lipgloss.NewStyle().Reverse(true).Bold(true)
	markedStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	addedStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	deletedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	lineNumStyle     = lipgloss.NewStyle().Faint(true)
	noteStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	cursorStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
)

var cursorIndicator = cursorStyle.Render("▸ ")

func notesByLine(notes []brain.Note) map[int][]brain.Note {
	m := map[int][]brain.Note{}
	for _, n := range notes {
		m[n.LineNo] = append(m[n.LineNo], n)
	}
	return m
}

// ghInlineByLine groups GH inline comments by their new-file line
// number, the same key local notes use. Comments whose ID matches a
// local note's GitHubCommentID are dropped so we don't double-render
// notes that this reviewer already published themselves.
func ghInlineByLine(ghs []gh.Comment, notes []brain.Note) map[int][]gh.Comment {
	skip := map[int64]bool{}
	for _, n := range notes {
		if n.GitHubCommentID != 0 {
			skip[n.GitHubCommentID] = true
		}
	}
	m := map[int][]gh.Comment{}
	for _, c := range ghs {
		if skip[c.GHID] {
			continue
		}
		m[c.Line] = append(m[c.Line], c)
	}
	return m
}

var ghCommentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))

func renderNoteLines(b *strings.Builder, notes []brain.Note, lineNum *int, lineMap *[]int) {
	for _, n := range notes {
		lines := strings.Split(n.Body, "\n")
		for i, line := range lines {
			prefix := "  ┃ "
			if i == 0 {
				prefix = "  ┃ RH: "
			}
			rendered := prefix + line
			if i == len(lines)-1 && n.GitHubCommentID != 0 {
				rendered += "  [→GH]"
			}
			b.WriteString(noteStyle.Render(rendered) + "\n")
			*lineMap = append(*lineMap, 0)
			*lineNum++
		}
	}
}

// renderGHInlineLines emits each GitHub inline comment formatted similarly
// to local notes but with a "GH @<author>:" lead and a different color so
// reviewers can tell at a glance which comments are theirs vs. other
// people's.
func renderGHInlineLines(b *strings.Builder, ghs []gh.Comment, lineNum *int, lineMap *[]int) {
	for _, c := range ghs {
		body := strings.TrimRight(c.Body, "\n")
		lines := strings.Split(body, "\n")
		for i, line := range lines {
			prefix := "  ┃ "
			if i == 0 {
				prefix = fmt.Sprintf("  ┃ GH @%s: ", c.Author)
			}
			b.WriteString(ghCommentStyle.Render(prefix+line) + "\n")
			*lineMap = append(*lineMap, 0)
			*lineNum++
		}
	}
}

type hunkRange struct {
	newStart int
	newCount int
}

var hunkRangeRE = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

func parseHunkRange(header string) hunkRange {
	m := hunkRangeRE.FindStringSubmatch(header)
	if m == nil {
		return hunkRange{}
	}
	start, _ := strconv.Atoi(m[1])
	count := 1
	if m[2] != "" {
		count, _ = strconv.Atoi(m[2])
	}
	return hunkRange{newStart: start, newCount: count}
}

// renderHunks produces the diff body with a "[✓]"/"[ ]" marker prepended
// to each hunk header. The hunk at focusedIdx is rendered with a
// reverse-video header so you can see what `space` / `up` / `down` will
// act on. Returns the rendered body and a parallel slice with each
// hunk's header line offset for SetYOffset-based navigation.
func renderHunks(hunks []corediff.Hunk, marks map[string]bool, focusedIdx int, notes []brain.Note, ghInline []gh.Comment, cursorLine int) (string, []int, []int) {
	byLine := notesByLine(notes)
	ghByLine := ghInlineByLine(ghInline, notes)
	var b strings.Builder
	var lineMap []int
	hunkLines := make([]int, 0, len(hunks))
	lineNum := 0
	for i, h := range hunks {
		mark := "[ ]"
		if marks[h.Hash] {
			mark = markedStyle.Render("[✓]")
		}
		headerLine := mark + " " + h.Header
		if i == focusedIdx {
			headerLine = focusedHunkStyle.Render(headerLine)
		}
		hunkLines = append(hunkLines, lineNum)
		b.WriteString(headerLine + "\n")
		lineMap = append(lineMap, 0)
		lineNum++

		r := parseHunkRange(h.Header)
		fileLine := r.newStart
		for _, line := range h.BodyLines {
			cur := fileLine
			isFile := true
			if len(line) > 0 && line[0] == '-' {
				isFile = false
			}

			prefix := ""
			if lineNum == cursorLine {
				prefix = cursorIndicator
			}
			b.WriteString(prefix + colorDiffLine(line) + "\n")
			if isFile {
				lineMap = append(lineMap, cur)
			} else {
				lineMap = append(lineMap, 0)
			}
			lineNum++

			if isFile {
				if ln, ok := byLine[cur]; ok {
					renderNoteLines(&b, ln, &lineNum, &lineMap)
				}
				if gl, ok := ghByLine[cur]; ok {
					renderGHInlineLines(&b, gl, &lineNum, &lineMap)
				}
				fileLine++
			}
		}
	}
	return b.String(), hunkLines, lineMap
}

func colorDiffLine(line string) string {
	if len(line) == 0 {
		return line
	}
	switch line[0] {
	case '+':
		return addedStyle.Render(line)
	case '-':
		return deletedStyle.Render(line)
	default:
		return line
	}
}

// renderFullFile produces a full-file view with diff lines colored
// inline. Unchanged lines show with line numbers; additions are green,
// deletions red. Hunk headers with mark indicators are shown at each
// change boundary.
func renderFullFile(fileContent string, hunks []corediff.Hunk, marks map[string]bool, focusedIdx int, notes []brain.Note, ghInline []gh.Comment, cursorLine int) (string, []int, []int) {
	byLine := notesByLine(notes)
	ghByLine := ghInlineByLine(ghInline, notes)
	fileLines := strings.Split(fileContent, "\n")
	if len(fileLines) > 0 && fileLines[len(fileLines)-1] == "" {
		fileLines = fileLines[:len(fileLines)-1]
	}

	type parsedHunk struct {
		corediff.Hunk
		r hunkRange
	}
	parsed := make([]parsedHunk, len(hunks))
	for i, h := range hunks {
		parsed[i] = parsedHunk{Hunk: h, r: parseHunkRange(h.Header)}
	}

	var b strings.Builder
	var lineMap []int
	hunkLineOffsets := make([]int, len(hunks))
	outputLine := 0
	newFileLine := 1
	gutterW := len(fmt.Sprintf("%d", len(fileLines)+100))

	writeLine := func(num int, text string) {
		prefix := ""
		if outputLine == cursorLine {
			prefix = cursorIndicator
		}
		gutter := lineNumStyle.Render(fmt.Sprintf("%*d", gutterW, num))
		b.WriteString(prefix + gutter + "  " + text + "\n")
		lineMap = append(lineMap, num)
		outputLine++
	}
	writeUnnum := func(text string) {
		pad := strings.Repeat(" ", gutterW)
		b.WriteString(pad + "  " + text + "\n")
		lineMap = append(lineMap, 0)
		outputLine++
	}
	emitNotes := func(fileLineNo int) {
		if ln, ok := byLine[fileLineNo]; ok {
			renderNoteLines(&b, ln, &outputLine, &lineMap)
		}
		if gl, ok := ghByLine[fileLineNo]; ok {
			renderGHInlineLines(&b, gl, &outputLine, &lineMap)
		}
	}

	for hi, ph := range parsed {
		for newFileLine < ph.r.newStart && newFileLine-1 < len(fileLines) {
			writeLine(newFileLine, fileLines[newFileLine-1])
			emitNotes(newFileLine)
			newFileLine++
		}

		mark := "[ ]"
		if marks[ph.Hash] {
			mark = markedStyle.Render("[✓]")
		}
		headerLine := mark + " " + ph.Header
		if hi == focusedIdx {
			headerLine = focusedHunkStyle.Render(headerLine)
		}
		hunkLineOffsets[hi] = outputLine
		writeUnnum(headerLine)

		for _, line := range ph.BodyLines {
			if len(line) == 0 {
				writeLine(newFileLine, "")
				emitNotes(newFileLine)
				newFileLine++
				continue
			}
			switch line[0] {
			case '+':
				writeLine(newFileLine, addedStyle.Render(line[1:]))
				emitNotes(newFileLine)
				newFileLine++
			case '-':
				writeUnnum(deletedStyle.Render(line))
			default:
				text := line
				if len(text) > 0 && text[0] == ' ' {
					text = text[1:]
				}
				writeLine(newFileLine, text)
				emitNotes(newFileLine)
				newFileLine++
			}
		}
	}

	for newFileLine-1 < len(fileLines) {
		writeLine(newFileLine, fileLines[newFileLine-1])
		emitNotes(newFileLine)
		newFileLine++
	}

	return b.String(), hunkLineOffsets, lineMap
}

// ParseHunkRange exposes the internal hunk-range parser to the rhodium
// package, which still uses it for building patchNewFileLines for the
// notes-tab content. The existing rhodium copy was the only other caller;
// re-exporting from here lets render_hunks.go go away cleanly.
func ParseHunkRange(header string) (newStart, newCount int) {
	r := parseHunkRange(header)
	return r.newStart, r.newCount
}

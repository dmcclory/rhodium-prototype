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
	resolvedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244")) // muted gray
	storyStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("249")).Italic(true) // muted, italic story line

	ddiffLabelStyle = lipgloss.NewStyle().Bold(true)
	ddiffDroppedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // red
	ddiffAddedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green
	ddiffKeptStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))// muted gray
	ddiffAbsorbedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))// yellow
	ddiffPropagatedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))// cyan

	// Chunk mode styles.
	chunkHeaderStyle    = lipgloss.NewStyle().Bold(true)
	chunkFocusedStyle   = lipgloss.NewStyle().Reverse(true).Bold(true)
	chunkComplexityStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true) // yellow warning
	chunkSigStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("249"))           // muted signature
)

var cursorIndicator = cursorStyle.Render("▸ ")

// resolvedIndicator is the subtle glyph shown in the gutter when a line
// has resolved (stale or manually resolved) notes.
var resolvedIndicator = resolvedStyle.Render("↺")

// renderDDiffLine renders one classified ddiff line with its label prefix.
func renderDDiffLine(dl corediff.DDiffLine) string {
	label := ddiffLabelStyle.Render(fmt.Sprintf("[%s]", dl.Kind))
	var prefix string
	var lineStyle lipgloss.Style
	switch dl.Kind {
	case corediff.DDiffDropped:
		prefix = "-"
		lineStyle = ddiffDroppedStyle
	case corediff.DDiffAdded:
		prefix = "+"
		lineStyle = ddiffAddedStyle
	case corediff.DDiffPropagated:
		prefix = "+"
		lineStyle = ddiffPropagatedStyle
	case corediff.DDiffAbsorbed:
		prefix = "-"
		lineStyle = ddiffAbsorbedStyle
	case corediff.DDiffKept:
		prefix = " "
		lineStyle = ddiffKeptStyle
	}
	return fmt.Sprintf("  ┃ %s %s%s", label, prefix, lineStyle.Render(dl.Text))
}

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
func renderHunks(hunks []corediff.Hunk, marks map[string]int, focusedIdx int, notes []brain.Note, resolvedNotes []brain.Note, ghInline []gh.Comment, cursorLine int, showingResolved bool) (string, []int, []int) {
	byLine := notesByLine(notes)
	resolvedByLine := notesByLine(resolvedNotes)
	ghByLine := ghInlineByLine(ghInline, notes)
	var b strings.Builder
	var lineMap []int
	hunkLines := make([]int, 0, len(hunks))
	lineNum := 0
	for i, h := range hunks {
		mark := "[ ]"
		if marks[h.Hash] > 0 {
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
				// Resolved notes: show inline when expanded, or a subtle
				// indicator when collapsed.
				if rn, ok := resolvedByLine[cur]; ok && len(rn) > 0 {
					if showingResolved {
						renderResolvedLines(&b, rn, &lineNum, &lineMap)
					} else {
						b.WriteString(resolvedIndicator + "\n")
						lineMap = append(lineMap, 0)
						lineNum++
					}
				}
				fileLine++
			}
		}
	}
	return b.String(), hunkLines, lineMap
}

// renderResolvedLines emits resolved notes in a muted style. These are
// notes that were either manually resolved or auto-resolved as stale.
func renderResolvedLines(b *strings.Builder, notes []brain.Note, lineNum *int, lineMap *[]int) {
	for _, n := range notes {
		lines := strings.Split(n.Body, "\n")
		stale := ""
		if n.BaseSHA != "" {
			stale = " [stale]"
		}
		for i, line := range lines {
			prefix := "  ┃ "
			if i == 0 {
				prefix = fmt.Sprintf("  ┃ RH: %s%s", line, stale)
				line = ""
			}
			rendered := prefix + line
			b.WriteString(resolvedStyle.Render(rendered) + "\n")
			*lineMap = append(*lineMap, 0)
			*lineNum++
		}
	}
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
func renderFullFile(fileContent string, hunks []corediff.Hunk, marks map[string]int, focusedIdx int, notes []brain.Note, resolvedNotes []brain.Note, ghInline []gh.Comment, cursorLine int, showingResolved bool, highlighter *corediff.Highlighter) (string, []int, []int) {
	fileLines := splitFileLines(fileContent)
	parsed := parseHunksWithRanges(hunks)

	fb := &fullFileBuilder{
		byLine:     notesByLine(notes),
		ghByLine:   ghInlineByLine(ghInline, notes),
		resolvedByLine: notesByLine(resolvedNotes),
		showingResolved: showingResolved,
		gutterW:    len(fmt.Sprintf("%d", len(fileLines)+100)),
		cursorLine: cursorLine,
		highlighter: highlighter,
	}
	hunkLineOffsets := make([]int, len(hunks))
	newFileLine := 1

	for hi, ph := range parsed {
		// Emit unchanged context before this hunk.
		for newFileLine < ph.r.newStart && newFileLine-1 < len(fileLines) {
			fb.writeLine(newFileLine, fileLines[newFileLine-1])
			fb.emitNotes(newFileLine)
			newFileLine++
		}
		hunkLineOffsets[hi] = fb.outputLine
		fb.writeUnnum(formatHunkHeader(ph.Hunk, marks, hi == focusedIdx))
		newFileLine = fb.emitHunkBody(ph.BodyLines, newFileLine)
	}

	// Trailing context after the last hunk.
	for newFileLine-1 < len(fileLines) {
		fb.writeLine(newFileLine, fileLines[newFileLine-1])
		fb.emitNotes(newFileLine)
		newFileLine++
	}

	return fb.b.String(), hunkLineOffsets, fb.lineMap
}

// splitFileLines splits the raw file content into a 0-indexed slice of
// lines, trimming the trailing empty string that strings.Split produces
// for content ending with a newline.
func splitFileLines(fileContent string) []string {
	lines := strings.Split(fileContent, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// parsedHunk attaches the parsed @@ header range to the corediff.Hunk so
// downstream code doesn't re-parse it.
type parsedHunk struct {
	corediff.Hunk
	r hunkRange
}

func parseHunksWithRanges(hunks []corediff.Hunk) []parsedHunk {
	parsed := make([]parsedHunk, len(hunks))
	for i, h := range hunks {
		parsed[i] = parsedHunk{Hunk: h, r: parseHunkRange(h.Header)}
	}
	return parsed
}

func formatHunkHeader(h corediff.Hunk, marks map[string]int, focused bool) string {
	mark := "[ ]"
	if marks[h.Hash] > 0 {
		mark = markedStyle.Render("[✓]")
	}
	headerLine := mark + " " + h.Header
	if focused {
		headerLine = focusedHunkStyle.Render(headerLine)
	}
	return headerLine
}

// fullFileBuilder owns the streaming output state for renderFullFile —
// the strings.Builder, the running output line index, and the line map.
// Exposing the streaming primitives as methods keeps the orchestrator
// readable and lets the per-hunk loop reuse them without juggling
// pointer args.
type fullFileBuilder struct {
	b          strings.Builder
	lineMap    []int
	outputLine int
	gutterW    int
	cursorLine int
	byLine     map[int][]brain.Note
	ghByLine   map[int][]gh.Comment
	resolvedByLine map[int][]brain.Note
	showingResolved bool
	highlighter *corediff.Highlighter // nil if highlighting not yet available
}

func (f *fullFileBuilder) writeLine(num int, text string) {
	prefix := ""
	if f.outputLine == f.cursorLine {
		prefix = cursorIndicator
	}
	gutter := lineNumStyle.Render(fmt.Sprintf("%*d", f.gutterW, num))
	// Use highlighted line if available.
	if f.highlighter != nil {
		if hl := f.highlighter.Line(num - 1); hl != "" {
			f.b.WriteString(prefix + gutter + "  " + hl + "\n")
			f.lineMap = append(f.lineMap, num)
			f.outputLine++
			return
		}
	}
	f.b.WriteString(prefix + gutter + "  " + text + "\n")
	f.lineMap = append(f.lineMap, num)
	f.outputLine++
}

func (f *fullFileBuilder) writeUnnum(text string) {
	pad := strings.Repeat(" ", f.gutterW)
	f.b.WriteString(pad + "  " + text + "\n")
	f.lineMap = append(f.lineMap, 0)
	f.outputLine++
}

func (f *fullFileBuilder) emitNotes(fileLineNo int) {
	if ln, ok := f.byLine[fileLineNo]; ok {
		renderNoteLines(&f.b, ln, &f.outputLine, &f.lineMap)
	}
	if gl, ok := f.ghByLine[fileLineNo]; ok {
		renderGHInlineLines(&f.b, gl, &f.outputLine, &f.lineMap)
	}
	// Resolved notes: show inline when expanded, or a subtle indicator.
	if rn, ok := f.resolvedByLine[fileLineNo]; ok && len(rn) > 0 {
		if f.showingResolved {
			renderResolvedLines(&f.b, rn, &f.outputLine, &f.lineMap)
		} else {
			f.b.WriteString(resolvedIndicator + "\n")
			f.lineMap = append(f.lineMap, 0)
			f.outputLine++
		}
	}
}

// emitHunkBody walks one hunk's BodyLines, emitting +/-/context with
// matching styles and notes. Returns the updated newFileLine cursor so
// the orchestrator can continue with the post-hunk context.
func (f *fullFileBuilder) emitHunkBody(bodyLines []string, newFileLine int) int {
	for _, line := range bodyLines {
		if len(line) == 0 {
			f.writeLine(newFileLine, "")
			f.emitNotes(newFileLine)
			newFileLine++
			continue
		}
		switch line[0] {
		case '+':
			// Use highlighted line if available, otherwise fall back to green.
			if f.highlighter != nil {
				if hl := f.highlighter.Line(newFileLine - 1); hl != "" {
					prefix := ""
					if f.outputLine == f.cursorLine {
						prefix = cursorIndicator
					}
					gutter := lineNumStyle.Render(fmt.Sprintf("%*d", f.gutterW, newFileLine))
					f.b.WriteString(prefix + gutter + "  " + hl + "\n")
					f.lineMap = append(f.lineMap, newFileLine)
					f.outputLine++
					f.emitNotes(newFileLine)
					newFileLine++
					continue
				}
			}
			f.writeLine(newFileLine, addedStyle.Render(line[1:]))
			f.emitNotes(newFileLine)
			newFileLine++
		case '-':
			f.writeUnnum(deletedStyle.Render(line))
		default:
			text := line
			if len(text) > 0 && text[0] == ' ' {
				text = text[1:]
			}
			f.writeLine(newFileLine, text)
			f.emitNotes(newFileLine)
			newFileLine++
		}
	}
	return newFileLine
}

// renderSegment renders a single segment's diff with proper line anchoring
// to the View.To corner. toLineOffset is added to all "new" line numbers
// so they reflect the segment's position in the full file. Returns the
// rendered body, per-hunk output-line offsets, and the output→file-line map.
// focusedHunkInSeg is the 0-based index of the focused hunk *within this
// segment's hunks only* (0 = first diff hunk, etc.). segIdx is the
// segment's index in the segments slice, used to build prefixed mark keys.
func renderSegment(seg corediff.Segment, view corediff.View, toLineOffset int, segIdx int, marks map[string]int, focusedHunkInSeg int, notes []brain.Note, resolvedNotes []brain.Note, ghInline []gh.Comment, cursorLine int, showingResolved bool) (string, []int, []int) {
	d := corediff.Diamond{B1: seg.B1, F1: seg.F1, B2: seg.B2, F2: seg.F2}
	from := d.Get(view.From)
	to := d.Get(view.To)

	segHunks := corediff.Diff2Hunks(from, to)
	if len(segHunks) == 0 {
		return "", nil, nil
	}

	byLine := notesByLine(notes)
	resolvedByLine := notesByLine(resolvedNotes)
	ghByLine := ghInlineByLine(ghInline, notes)

	var b strings.Builder
	var lineMap []int
	hunkLines := make([]int, 0, len(segHunks))
	lineNum := 0

	for hi, h := range segHunks {
		mark := "[ ]"
		key := fmt.Sprintf("%d:%s", segIdx, h.Hash)
		if marks[key] > 0 {
			mark = markedStyle.Render("[✓]")
		}
		isFocused := hi == focusedHunkInSeg
		headerLine := mark + " " + h.Header
		if isFocused {
			headerLine = focusedHunkStyle.Render(headerLine)
		}
		hunkLines = append(hunkLines, lineNum)
		b.WriteString(headerLine + "\n")
		lineMap = append(lineMap, 0)
		lineNum++

		r := parseHunkRange(h.Header)
		fileLine := r.newStart + toLineOffset
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
				if rn, ok := resolvedByLine[cur]; ok && len(rn) > 0 {
					if showingResolved {
						renderResolvedLines(&b, rn, &lineNum, &lineMap)
					} else {
						b.WriteString(resolvedIndicator + "\n")
						lineMap = append(lineMap, 0)
						lineNum++
					}
				}
				fileLine++
			}
		}
	}
	return b.String(), hunkLines, lineMap
}

// segmentHeader renders the synthetic header line for a classified segment.
func segmentHeader(segIdx, total int, seg corediff.Segment, view corediff.View) string {
	return fmt.Sprintf("== segment %d/%d · %s · %s→%s (%s) ==",
		segIdx+1, total, seg.Class, view.From, view.To, view.Kind)
}

// splitLinesCount returns the number of logical lines in s, matching the
// convention of splitLinesForSeg (drops trailing phantom empty element).
func splitLinesCount(s string) int {
	if s == "" {
		return 0
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return len(lines)
}

// renderSegmented renders each segment independently with per-segment
// diff hunks and line anchoring to the View.To corner. This replaces the
// flat renderHunks path when the diff is in segmented (catch-up) mode.
// Returns the rendered body, per-hunk output-line offsets (including
// segment headers), and the output→file-line map.
func renderSegmented(segments []corediff.Segment, viewIdx int, storyMode bool, marks map[string]int, focusedHunkIdx int, notes []brain.Note, resolvedNotes []brain.Note, ghInline []gh.Comment, cursorLine int, showingResolved bool) (string, []int, []int) {
	var b strings.Builder
	var lineMap []int
	hunkLines := make([]int, 0, len(segments)*2)

	toOffset := 0  // cumulative To-line offset across segments
	globalIdx := 0 // global hunk index (headers + diff hunks combined)
	outLine := 0   // running output line counter

	for segIdx, seg := range segments {
		views := seg.Class.Views()
		if len(views) == 0 {
			continue // hidden class — skip
		}
		view := views[viewIdx%len(views)]

		// Determine how many diff hunks this segment will produce.
		d := corediff.Diamond{B1: seg.B1, F1: seg.F1, B2: seg.B2, F2: seg.F2}
		from := d.Get(view.From)
		toContent := d.Get(view.To)
		segHunks := corediff.Diff2Hunks(from, toContent)

		// --- Segment header ---
		headerFocused := globalIdx == focusedHunkIdx
		headerText := segmentHeader(segIdx, len(segments), seg, view)
		if headerFocused {
			headerText = focusedHunkStyle.Render(headerText)
		}
		b.WriteString(headerText + "\n")
		lineMap = append(lineMap, 0)
		hunkLines = append(hunkLines, outLine)
		outLine++
		globalIdx++

		// Story summary line.
		if storyMode {
			if summary := corediff.StorySummary(seg); summary != "" {
				b.WriteString(storyStyle.Render(summary) + "\n")
				lineMap = append(lineMap, 0)
				outLine++
			}
			// Feature ddiff block.
			if ddiff := corediff.FeatureDDiff(seg); len(ddiff) > 0 {
				for _, dl := range ddiff {
					b.WriteString(renderDDiffLine(dl) + "\n")
					lineMap = append(lineMap, 0)
					outLine++
				}
			}
		}

		// If no diff hunks, skip the body but still account for the To
		// lines (they're context for subsequent segments).
		if len(segHunks) == 0 {
			toOffset += splitLinesCount(toContent)
			continue
		}

		// Find which hunk within this segment is globally focused.
		focusedInSeg := -1
		for hi := 0; hi < len(segHunks); hi++ {
			if globalIdx+hi == focusedHunkIdx {
				focusedInSeg = hi
				break
			}
		}

		body, segHunkLines, segLineMap := renderSegment(seg, view, toOffset, segIdx, marks, focusedInSeg, notes, resolvedNotes, ghInline, cursorLine, showingResolved)
		if body != "" {
			b.WriteString(body)
			// segHunkLines are relative to the segment body's start (0-based).
			// Convert to absolute output-line indices.
			for _, rel := range segHunkLines {
				hunkLines = append(hunkLines, outLine+rel)
			}
			lineMap = append(lineMap, segLineMap...)
			outLine += len(segLineMap)
		}

		// Update offset for next segment.
		toOffset += splitLinesCount(toContent)
		globalIdx += len(segHunks)
	}

	return b.String(), hunkLines, lineMap
}

// ParseHunkRange exposes the internal hunk-range parser to the rhodium
// package, which still uses it for building patchNewFileLines for the
// notes-tab content. The existing rhodium copy was the only other caller;
// re-exporting from here lets render_hunks.go go away cleanly.
func ParseHunkRange(header string) (newStart, newCount int) {
	r := parseHunkRange(header)
	return r.newStart, r.newCount
}

// renderChunks produces a collapsed or expanded chunk view. Each chunk
// shows as a single line when collapsed, or as a header + its diff lines
// when expanded. Returns the rendered body, per-chunk output-line offsets,
// and the output→file-line map.
func renderChunks(chunks []corediff.Chunk, hunks []corediff.Hunk, marks map[string]int, focusedChunkIdx int, expanded map[int]bool, notes []brain.Note, resolvedNotes []brain.Note, ghInline []gh.Comment, cursorLine int, showingResolved bool, highlighter *corediff.Highlighter) (string, []int, []int) {
	byLine := notesByLine(notes)
	resolvedByLine := notesByLine(resolvedNotes)
	ghByLine := ghInlineByLine(ghInline, notes)

	var b strings.Builder
	var lineMap []int
	chunkLines := make([]int, 0, len(chunks))
	lineNum := 0

	for ci, c := range chunks {
		isFocused := ci == focusedChunkIdx
		isExpanded := expanded[ci]

		// Check if all hunks in this chunk are marked.
		allMarked := true
		for _, hi := range c.HunkIdxs {
			h := hunks[hi]
			if !h.IsMarkable() {
				continue
			}
			if marks[h.Hash] == 0 {
				allMarked = false
				break
			}
		}

		mark := "[ ]"
		if allMarked {
			mark = markedStyle.Render("[✓]")
		}

		// Build the header line.
		header := formatChunkHeader(mark, c, allMarked, isFocused)
		chunkLines = append(chunkLines, lineNum)
		b.WriteString(header + "\n")
		lineMap = append(lineMap, 0)
		lineNum++

		// If expanded, render the hunks within this chunk.
		if isExpanded {
			for _, hi := range c.HunkIdxs {
				h := hunks[hi]
				// Render hunk header (without mark, since chunk has the mark).
				hunkHeader := h.Header
				b.WriteString(lineNumStyle.Render("  "+hunkHeader) + "\n")
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

					if len(line) > 0 && line[0] == '+' && highlighter != nil {
						if hl := highlighter.Line(fileLine - 1); hl != "" {
							b.WriteString(prefix + "  " + hl + "\n")
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
								if rn, ok := resolvedByLine[cur]; ok && len(rn) > 0 {
									if showingResolved {
										renderResolvedLines(&b, rn, &lineNum, &lineMap)
									} else {
										b.WriteString(resolvedIndicator + "\n")
										lineMap = append(lineMap, 0)
										lineNum++
									}
								}
								fileLine++
							}
							continue
						}
					}

					b.WriteString(prefix + "  " + colorDiffLine(line) + "\n")
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
						if rn, ok := resolvedByLine[cur]; ok && len(rn) > 0 {
							if showingResolved {
								renderResolvedLines(&b, rn, &lineNum, &lineMap)
							} else {
								b.WriteString(resolvedIndicator + "\n")
								lineMap = append(lineMap, 0)
								lineNum++
							}
						}
						fileLine++
					}
				}
			}
		}
	}

	return b.String(), chunkLines, lineMap
}

// formatChunkHeader builds the display line for one chunk.
func formatChunkHeader(mark string, c corediff.Chunk, allMarked bool, focused bool) string {
	sig := chunkSigStyle.Render(c.Signature)
	rangeStr := fmt.Sprintf("(%d-%d)", c.StartLine, c.EndLine)

	var complexityStr string
	if c.Complexity > 5 {
		complexityStr = " " + chunkComplexityStyle.Render(fmt.Sprintf("⚠ %d", c.Complexity))
	}

	content := fmt.Sprintf("  %s %s %s%s", mark, sig, rangeStr, complexityStr)
	if focused {
		return chunkFocusedStyle.Render(content)
	}
	return chunkHeaderStyle.Render(content)
}

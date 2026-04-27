package rhodium

import (
	"fmt"
	"rhodium/internal/brain"
	"rhodium/internal/diff"
	"rhodium/internal/gh"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- diffView ---

type diffView struct {
	vp        viewport.Model
	noteInput textarea.Model

	hunks      []diff.Hunk
	marks      map[string]bool
	notes      []brain.Note
	ghInline   []gh.Comment // GitHub inline comments scoped to the current file
	hunkLines  []int
	lineMap    []int // output line → new-file line number (0 = non-file line)
	hunkIdx    int
	cursorLine int
	diffLines  []string // raw content lines for manual rendering in noting mode
	blob       string   // full file content, empty until blob loads

	// Catch-up diff state.
	catchUpMode    bool       // true when showing only the delta since last review
	catchUpOldHead string     // the head SHA we last reviewed at (f1)
	catchUpOldBase string     // the base SHA we last reviewed at (b1)
	catchUpClass   diff.Class // diff4 classification of the catch-up
	catchUpPatch   string     // the delta patch for the current file

	// Slow-path segmentation state. Populated when catch-up lands on a
	// complex class and diff.ComputeSlow returns per-segment decomposition.
	// segmented==true means v.hunks coordinates are segment-local, so the
	// full-file render path in redraw() must be skipped.
	segments       []diff.Segment
	segmentViewIdx int // phase 2: cycles which View each segment renders under
	segmented      bool

	// Note input state.
	noting       bool
	noteLineNo   int
	noteLineHash string

	// @-mention picker overlay (only meaningful while noting).
	mention mentionPicker
}

func newDiffView() diffView {
	ti := textarea.New()
	ti.Placeholder = "Write a note... (ctrl+d to save, esc to cancel)"
	ti.SetHeight(3)
	ti.ShowLineNumbers = false
	return diffView{
		vp:        viewport.New(0, 0),
		noteInput: ti,
		mention:   newMentionPicker(),
	}
}

func (v *diffView) Resize(w, h int) {
	v.vp.Width = w
	v.vp.Height = h
}

func (v *diffView) View(a *app) string {
	if v.noting {
		body := v.renderNotingView(a)
		if v.mention.open {
			box := v.mention.Render()
			boxW := lipgloss.Width(box)
			boxH := lipgloss.Height(box)
			x := (a.layout.width - boxW) / 2
			if x < 0 {
				x = 0
			}
			y := (a.layout.height - boxH) / 2
			if y < 0 {
				y = 0
			}
			return overlay(body, box, x, y)
		}
		return body
	}
	return v.vp.View()
}

func (v *diffView) Footer(a *app) string {
	if v.noting {
		if v.mention.open {
			return "@-mention  ↑/↓: nav  type to filter  enter: insert  esc: close"
		}
		return fmt.Sprintf("line %d  ctrl+d: save  type @ to mention  esc: cancel", v.noteLineNo)
	}
	marked := 0
	total := 0
	cur := 0
	for i, h := range v.hunks {
		if !h.IsMarkable() {
			continue
		}
		total++
		if v.marks[h.Hash] {
			marked++
		}
		if i <= v.hunkIdx {
			cur = total
		}
	}
	if total == 0 {
		cur = 0
	}
	modeHint := ""
	if v.catchUpOldHead != "" {
		if v.catchUpMode {
			if v.segmented {
				maxV := diff.MaxSegmentViews(v.segments)
				cycleHint := ""
				if maxV > 1 {
					cycleHint = fmt.Sprintf("  v: cycle view (%d/%d)", (v.segmentViewIdx%maxV)+1, maxV)
				}
				modeHint = fmt.Sprintf("  [catch-up %s: %d segments since %s]  d: full diff%s", v.catchUpClass, len(v.segments), shortSHA(v.catchUpOldHead), cycleHint)
			} else {
				modeHint = fmt.Sprintf("  [catch-up %s since %s]  d: full diff", v.catchUpClass, shortSHA(v.catchUpOldHead))
			}
		} else {
			modeHint = "  [full diff]  d: catch-up"
		}
	}
	return fmt.Sprintf("hunk %d/%d  marked %d/%d%s  ↑/↓: nav  j/k: cursor  space: toggle+next  m: mark all  c: note  P: publish note  o: open  u: unmark  h: back", cur, total, marked, total, modeHint)
}

// Update routes keys + messages while the diff view is active.
func (v *diffView) Update(a *app, msg tea.Msg) tea.Cmd {
	if key, ok := msg.(tea.KeyMsg); ok {
		if v.noting {
			return v.updateNotingKeys(a, key)
		}
		return v.updateKeys(a, key)
	}
	var cmd tea.Cmd
	v.vp, cmd = v.vp.Update(msg)
	return cmd
}

// open loads a file into the diff view: parse hunks, seed marks from
// the brain, show patch view immediately, then kick off blob fetch for
// full file. If the PR head has moved since this file was last reviewed,
// we enter catch-up mode: fetch only the delta and show that instead of
// the full PR diff.
func (v *diffView) open(a *app, fc gh.FileChange) tea.Cmd {
	a.session.selectedFile = fc.Path
	a.layout.focus(viewDiff)
	v.blob = ""
	v.catchUpMode = false
	v.catchUpOldHead = ""
	v.catchUpOldBase = ""
	v.catchUpClass = diff.ClassB1B2F1F2
	v.catchUpPatch = ""
	v.segments = nil
	v.segmentViewIdx = 0
	v.segmented = false

	revState := a.brain.FileReviewedState(a.session.selectedPR.Repo, a.session.selectedPR.Number, fc.Path)
	scrutinized := a.brain.IsScrutinized(a.session.selectedPR.Repo, a.session.selectedPR.Number)
	needsCatchUp := !scrutinized && revState.HeadSHA != "" && (revState.HeadSHA != a.session.selectedPR.HeadSHA || revState.BaseSHA != a.session.selectedPR.BaseSHA)

	v.hunks = diff.ParseHunks(fc.Patch)
	v.marks = a.brain.HunkMarks(a.session.selectedPR.Repo, a.session.selectedPR.Number, fc.Path)
	v.notes = a.brain.NotesForFile(a.session.selectedPR.Repo, a.session.selectedPR.Number, fc.Path)
	v.ghInline = ghInlineForFile(a, fc.Path)
	v.hunkIdx = firstUnmarked(v.hunks, v.marks)
	v.redraw()
	v.jumpToHunk()

	var cmds []tea.Cmd

	if needsCatchUp {
		v.catchUpOldHead = revState.HeadSHA
		v.catchUpOldBase = revState.BaseSHA
		repo := a.session.selectedPR.Repo
		oldHead := revState.HeadSHA
		oldBase := revState.BaseSHA
		newHead := a.session.selectedPR.HeadSHA
		newBase := a.session.selectedPR.BaseSHA
		path := fc.Path
		rebased := oldBase != newBase && oldBase != ""

		if rebased {
			a.status.msg = fmt.Sprintf("classifying diamond (rebase %s→%s)", shortSHA(oldBase), shortSHA(newBase))
			cmds = append(cmds, func() tea.Msg {
				b1, _ := gh.FetchFileAtRef(repo, path, oldBase)
				f1, _ := gh.FetchFileAtRef(repo, path, oldHead)
				b2, _ := gh.FetchFileAtRef(repo, path, newBase)
				f2, _ := gh.FetchFileAtRef(repo, path, newHead)
				d := diff.Diamond{B1: b1, F1: f1, B2: b2, F2: f2}
				class := diff.Classify(d, nil)
				result := diff.ComputeSlow(d)
				// For shown-as-diff2 classes we still fetch the whole-file
				// unified patch — it's one segment, and the reviewer gets
				// nicer context/hunk boundaries from git than from our
				// patience-based diff2Hunks. Complex classes go through
				// diff.SegmentHunks instead, so no patch is needed.
				var patch string
				if class.ShownAsDiff2() {
					files, _ := gh.FetchCompare(repo, oldHead, newHead)
					for _, f := range files {
						if f.Path == path {
							patch = f.Patch
							break
						}
					}
				}
				return diamondClassifiedMsg{path: path, class: class, diamond: d, result: result, patch: patch}
			})
		} else {
			a.status.msg = fmt.Sprintf("loading catch-up diff %s..%s", shortSHA(oldHead), shortSHA(newHead))
			cmds = append(cmds, func() tea.Msg {
				files, err := gh.FetchCompare(repo, oldHead, newHead)
				return catchUpLoadedMsg{path: path, files: files, err: err}
			})
		}
	}

	if fc.Blob != "" {
		repo := a.session.selectedPR.Repo
		sha := fc.Blob
		cmds = append(cmds, func() tea.Msg {
			content, err := gh.FetchBlob(repo, sha)
			return blobLoadedMsg{content: content, err: err}
		})
	}

	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// --- async message callbacks ---

func (v *diffView) onCatchUpLoaded(a *app, msg catchUpLoadedMsg) tea.Cmd {
	if msg.err != nil {
		a.status.msg = "catch-up: " + msg.err.Error()
		return nil
	}
	if a.layout.activeView != viewDiff || a.session.selectedFile != msg.path {
		return nil
	}
	var deltaFC *gh.FileChange
	for _, f := range msg.files {
		if f.Path == msg.path {
			deltaFC = &f
			break
		}
	}
	if deltaFC == nil || deltaFC.Patch == "" {
		v.catchUpMode = false
		v.catchUpClass = diff.ClassB1B2__F1F2
		a.status.msg = fmt.Sprintf("✓ %s: %s (auto-caught-up)", a.session.selectedFile, diff.ClassB1B2__F1F2)
		a.brain.SetFileReviewed(a.session.selectedPR.Repo, a.session.selectedPR.Number, a.session.selectedFile, a.session.selectedPR.HeadSHA, a.session.selectedPR.BaseSHA)
		a.markSessionFileDone(a.session.selectedFile)
		return nil
	}
	v.catchUpMode = true
	v.catchUpClass = diff.ClassB1B2
	v.catchUpPatch = deltaFC.Patch
	v.hunks = diff.ParseHunks(deltaFC.Patch)
	v.marks = a.brain.HunkMarks(a.session.selectedPR.Repo, a.session.selectedPR.Number, a.session.selectedFile)
	v.hunkIdx = firstUnmarked(v.hunks, v.marks)
	a.status.msg = fmt.Sprintf("catch-up [%s]: f1→f2 since %s  (d: full diff)", diff.ClassB1B2, shortSHA(v.catchUpOldHead))
	v.redraw()
	v.jumpToHunk()
	return nil
}

func (v *diffView) onDiamondClassified(a *app, msg diamondClassifiedMsg) tea.Cmd {
	if msg.err != nil {
		a.status.msg = "classify: " + msg.err.Error()
		return nil
	}
	if a.layout.activeView != viewDiff || a.session.selectedFile != msg.path {
		return nil
	}
	v.catchUpClass = msg.class

	if msg.class.Hidden() {
		v.catchUpMode = false
		v.segmented = false
		label := msg.class.String()
		if msg.class.IsForget() {
			label = "FORGET — base absorbed feature"
		}
		a.status.msg = fmt.Sprintf("✓ %s: %s (auto-caught-up)", a.session.selectedFile, label)
		a.brain.SetFileReviewed(a.session.selectedPR.Repo, a.session.selectedPR.Number, a.session.selectedFile, a.session.selectedPR.HeadSHA, a.session.selectedPR.BaseSHA)
		a.markSessionFileDone(a.session.selectedFile)
		return nil
	}

	v.catchUpMode = true
	if msg.class.ShownAsDiff2() && msg.patch != "" {
		// Single-segment case: reuse git's whole-file unified patch.
		v.catchUpPatch = msg.patch
		v.hunks = diff.ParseHunks(msg.patch)
		v.segments = nil
		v.segmented = false
	} else if msg.result != nil && len(msg.result.Segments) > 0 {
		// Complex class: per-segment decomposition.
		v.segments = msg.result.Segments
		v.hunks = diff.SegmentHunks(v.segments, v.segmentViewIdx)
		v.catchUpPatch = ""
		v.segmented = true
	}
	v.marks = a.brain.HunkMarks(a.session.selectedPR.Repo, a.session.selectedPR.Number, a.session.selectedFile)
	v.hunkIdx = firstUnmarked(v.hunks, v.marks)

	if v.segmented {
		a.status.msg = fmt.Sprintf("catch-up [%s]: %d segments  (d: full diff)", msg.class, len(v.segments))
	} else {
		views := msg.class.Views()
		viewLabel := ""
		if len(views) > 0 {
			viewLabel = fmt.Sprintf("%s→%s", views[0].From, views[0].To)
		}
		a.status.msg = fmt.Sprintf("catch-up [%s]: %s  (d: full diff)", msg.class, viewLabel)
	}
	v.redraw()
	v.jumpToHunk()
	return nil
}

// ghInlineForFile filters the cached PR comments down to inline ones on
// the given path. Empty result is fine — comments may not have loaded yet,
// or the file simply has none.
func ghInlineForFile(a *app, path string) []gh.Comment {
	if a.session.selectedPR == nil {
		return nil
	}
	all := a.cache.prComments[brain.PRKey(a.session.selectedPR.Repo, a.session.selectedPR.Number)]
	var out []gh.Comment
	for _, c := range all {
		if c.Type == "inline" && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

// refreshGHInline reloads the inline comment slice for the currently
// open file and redraws. Called by app.onCommentsLoaded so comments that
// arrive while the diff view is already open still surface inline.
func (v *diffView) refreshGHInline(a *app) {
	v.ghInline = ghInlineForFile(a, a.session.selectedFile)
	v.redraw()
}

func (v *diffView) onBlobLoaded(a *app, msg blobLoadedMsg) tea.Cmd {
	if msg.err != nil {
		a.status.msg = "blob: " + msg.err.Error()
		return nil
	}
	if a.layout.activeView == viewDiff {
		v.blob = msg.content
		if !v.catchUpMode {
			v.redraw()
			v.jumpToHunk()
		}
	}
	return nil
}

// --- rendering ---

func (v *diffView) redraw() {
	if len(v.hunks) == 0 {
		v.vp.SetContent("(no hunks — nothing to review)")
		v.hunkLines = nil
		return
	}
	var body string
	var lines []int
	var lmap []int
	if v.blob != "" && !v.segmented {
		body, lines, lmap = renderFullFile(v.blob, v.hunks, v.marks, v.hunkIdx, v.notes, v.ghInline, v.cursorLine)
	} else {
		body, lines, lmap = renderHunks(v.hunks, v.marks, v.hunkIdx, v.notes, v.ghInline, v.cursorLine)
	}
	v.vp.SetContent(body)
	v.diffLines = strings.Split(body, "\n")
	v.hunkLines = lines
	v.lineMap = lmap
}

func (v *diffView) jumpToHunk() {
	if v.hunkIdx < 0 || v.hunkIdx >= len(v.hunkLines) {
		return
	}
	target := v.hunkLines[v.hunkIdx]
	v.cursorLine = target
	v.vp.SetYOffset(target)
}

func (v *diffView) allMarked() bool {
	any := false
	for _, h := range v.hunks {
		if !h.IsMarkable() {
			continue
		}
		any = true
		if !v.marks[h.Hash] {
			return false
		}
	}
	return any
}

// stepHunk moves v.hunkIdx by ±1 (or more, future-proof) across real hunks,
// skipping synthetic segment headers so n/p/tab navigation only lands on
// markable units.
func (v *diffView) stepHunk(delta int) {
	if len(v.hunks) == 0 || delta == 0 {
		return
	}
	step := 1
	if delta < 0 {
		step = -1
	}
	next := v.hunkIdx + step
	for next >= 0 && next < len(v.hunks) && !v.hunks[next].IsMarkable() {
		next += step
	}
	if next < 0 || next >= len(v.hunks) {
		return
	}
	v.hunkIdx = next
}

// currentSegmentView returns the View the hunk at hunkIdx is being rendered
// under, or ok=false when not in segmented mode or the cursor is off the
// hunk list. Used to decide whether a note can be keyed to an F2 line.
func (v *diffView) currentSegmentView() (diff.View, bool) {
	if !v.segmented || v.hunkIdx < 0 || v.hunkIdx >= len(v.hunks) {
		return diff.View{}, false
	}
	segIdx := -1
	for i := 0; i <= v.hunkIdx; i++ {
		if !v.hunks[i].IsMarkable() {
			segIdx++
		}
	}
	if segIdx < 0 || segIdx >= len(v.segments) {
		return diff.View{}, false
	}
	views := v.segments[segIdx].Class.Views()
	if len(views) == 0 {
		return diff.View{}, false
	}
	return views[v.segmentViewIdx%len(views)], true
}

func (v *diffView) renderNotingView(a *app) string {
	_, padV := appStyle.GetFrameSize()
	totalH := a.layout.height - padV - 1 // minus footer
	taHeight := v.noteInput.Height() + 2
	diffH := totalH - taHeight

	contentLines := v.diffLines

	screenPos := v.cursorLine - v.vp.YOffset
	yOff := v.vp.YOffset

	maxScreenPos := diffH - taHeight - 1
	if maxScreenPos < 0 {
		maxScreenPos = 0
	}
	if screenPos > maxScreenPos {
		yOff = v.cursorLine - maxScreenPos
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
	b.WriteString(v.noteInput.View())
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

func (v *diffView) moveCursor(delta int) {
	next := v.cursorLine + delta
	if next < 0 {
		next = 0
	}
	if max := len(v.lineMap) - 1; max >= 0 && next > max {
		next = max
	}
	v.cursorLine = next
	v.redraw()
	if v.cursorLine < v.vp.YOffset {
		v.vp.SetYOffset(v.cursorLine)
	} else if v.cursorLine >= v.vp.YOffset+v.vp.Height {
		v.vp.SetYOffset(v.cursorLine - v.vp.Height + 1)
	}
}

func (v *diffView) cursorFileLine() int {
	if v.cursorLine < 0 || v.cursorLine >= len(v.lineMap) {
		return 0
	}
	return v.lineMap[v.cursorLine]
}

func (v *diffView) cursorLineHash(lineNo int) string {
	if v.blob == "" {
		return ""
	}
	lines := strings.Split(v.blob, "\n")
	idx := lineNo - 1
	if idx < 0 || idx >= len(lines) {
		return ""
	}
	return hashLine(lines[idx])
}

// --- persistence helpers ---

func (v *diffView) saveMarks(a *app) {
	if a.session.selectedPR == nil || a.session.selectedFile == "" {
		return
	}
	if err := a.brain.SetHunkMarks(a.session.selectedPR.Repo, a.session.selectedPR.Number, a.session.selectedFile, v.marks); err != nil {
		a.status.msg = "save error: " + err.Error()
		return
	}
	if a.session.selectedPR.HeadSHA != "" {
		a.brain.SetFileReviewed(a.session.selectedPR.Repo, a.session.selectedPR.Number, a.session.selectedFile, a.session.selectedPR.HeadSHA, a.session.selectedPR.BaseSHA)
	}
	if v.allMarked() {
		a.markSessionFileDone(a.session.selectedFile)
	}
}

// --- editor launch ---

func (v *diffView) openInEditor(a *app) (tea.Cmd, error) {
	if a.session.selectedPR == nil || a.session.selectedFile == "" {
		return nil, fmt.Errorf("nothing selected")
	}
	worktree, err := resolveWorktree(a.cfg, a.session.selectedPR.Repo, a.session.selectedPR.Number)
	if err != nil {
		return nil, err
	}
	line := 1
	if v.hunkIdx >= 0 && v.hunkIdx < len(v.hunks) {
		if _, n := hunkLines(v.hunks[v.hunkIdx].Header); n > 0 {
			line = n
		}
	}
	prKeyStr := fmt.Sprintf("%s#%d", a.session.selectedPR.Repo, a.session.selectedPR.Number)
	a.status.msg = fmt.Sprintf("opening %s:%d in %s", a.session.selectedFile, line, worktree)
	return launchEditor(a.cfg, worktree, a.session.selectedFile, prKeyStr, line), nil
}

// --- key handling ---

func (v *diffView) updateNotingKeys(a *app, msg tea.KeyMsg) tea.Cmd {
	// Mention picker is modal over the textarea: routes all keys while open.
	if v.mention.open {
		return v.updateMentionKeys(a, msg)
	}
	switch msg.String() {
	case "esc":
		v.noting = false
		v.noteInput.Blur()
		v.restoreSize(a)
		return nil
	case "ctrl+d":
		body := strings.TrimSpace(v.noteInput.Value())
		v.noting = false
		v.noteInput.Blur()
		v.restoreSize(a)
		if body != "" {
			if err := a.brain.SaveNote(a.session.selectedPR.Repo, a.session.selectedPR.Number, a.session.selectedFile, v.noteLineNo, v.noteLineHash, body); err != nil {
				a.status.msg = "save note: " + err.Error()
			} else {
				v.notes = a.brain.NotesForFile(a.session.selectedPR.Repo, a.session.selectedPR.Number, a.session.selectedFile)
				v.redraw()
			}
		}
		return nil
	case "@":
		// Check boundary *before* the textarea inserts the @ — afterwards
		// the char before the cursor is the @ itself.
		trigger := v.atMentionBoundary()
		var cmd tea.Cmd
		v.noteInput, cmd = v.noteInput.Update(msg)
		if !trigger {
			return cmd
		}
		if openCmd := v.openMentionPicker(a); openCmd != nil {
			return tea.Batch(cmd, openCmd)
		}
		return cmd
	}
	var cmd tea.Cmd
	v.noteInput, cmd = v.noteInput.Update(msg)
	return cmd
}

// atMentionBoundary reports whether the cursor is at a spot where typing
// `@` should open the mention picker — i.e. at the start of a line or
// right after whitespace. This keeps email-like text ("foo@bar.com") from
// triggering the picker.
func (v *diffView) atMentionBoundary() bool {
	li := v.noteInput.LineInfo()
	col := li.StartColumn + li.ColumnOffset
	if col <= 0 {
		return true
	}
	row := v.noteInput.Line()
	lines := strings.Split(v.noteInput.Value(), "\n")
	if row < 0 || row >= len(lines) {
		return true
	}
	runes := []rune(lines[row])
	if col > len(runes) {
		return true
	}
	return unicode.IsSpace(runes[col-1])
}

// openMentionPicker shows the @-picker over the textarea. Contributors are
// fetched lazily per repo; subsequent opens in the same session use the
// cache and are instant. The picker opens with an empty query showing the
// full contributor list.
func (v *diffView) openMentionPicker(a *app) tea.Cmd {
	if a.session.selectedPR == nil {
		return nil
	}
	repo := a.session.selectedPR.Repo
	v.mention.open = true
	v.mention.query = ""
	if cached, ok := a.cache.contributors[repo]; ok {
		v.mention.loading = false
		v.mention.list.SetItems(filterContributors(cached, ""))
		v.mention.list.ResetSelected()
		v.sizeMentionPicker(a)
		return nil
	}
	v.mention.loading = true
	v.mention.list.SetItems(nil)
	v.sizeMentionPicker(a)
	return loadContributorsCmd(repo)
}

// onContributorsReady lands from app.onContributorsLoaded when a fetch
// returns while the picker is open for the same repo. If the picker has
// been dismissed in the meantime this is a no-op — the cache is still
// populated for next time.
func (v *diffView) onContributorsReady(a *app, repo string) {
	if !v.mention.open || a.session.selectedPR == nil || a.session.selectedPR.Repo != repo {
		return
	}
	v.mention.loading = false
	v.mention.list.SetItems(filterContributors(a.cache.contributors[repo], v.mention.query))
	v.sizeMentionPicker(a)
}

func (v *diffView) sizeMentionPicker(a *app) {
	w := a.layout.width / 2
	if w < 30 {
		w = 30
	}
	h := 12
	if a.layout.height < h+4 {
		h = a.layout.height - 4
		if h < 5 {
			h = 5
		}
	}
	v.mention.list.SetSize(w, h)
}

// updateMentionKeys routes keys while the mention picker is open. The
// picker sits over the noting textarea and runs in a dual-input model:
// printable keys are forwarded to the textarea (so the user sees their
// `@query` building up) and also drive a live filter over the contributor
// list. Enter replaces the typed `@query` fragment with `@<login> `; esc
// leaves the text untouched. Cursor-motion and other non-login keys close
// the picker and fall through to the textarea.
func (v *diffView) updateMentionKeys(a *app, msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		v.mention.open = false
		return nil
	case "enter":
		return v.acceptMention()
	case "up", "down", "tab", "shift+tab":
		var cmd tea.Cmd
		v.mention.list, cmd = v.mention.list.Update(msg)
		return cmd
	case "backspace", "ctrl+h":
		return v.mentionBackspace(a, msg)
	}

	if len(msg.Runes) == 1 && isMentionChar(msg.Runes[0]) {
		return v.mentionTypeRune(a, msg)
	}

	// Any other key (space, punctuation, cursor motion, ctrl+u, etc.)
	// closes the picker and flows through to the textarea so the user's
	// intent — typing, navigating, deleting a word — still happens.
	v.mention.open = false
	var cmd tea.Cmd
	v.noteInput, cmd = v.noteInput.Update(msg)
	return cmd
}

// mentionTypeRune appends a char to the query and refreshes the filtered list.
func (v *diffView) mentionTypeRune(a *app, msg tea.KeyMsg) tea.Cmd {
	var cmd tea.Cmd
	v.noteInput, cmd = v.noteInput.Update(msg)
	v.mention.query += string(msg.Runes[0])
	v.refilterMentions(a)
	return cmd
}

// mentionBackspace pops a char off the query. If the query was already
// empty the backspace deletes the leading `@` and we close the picker.
func (v *diffView) mentionBackspace(a *app, msg tea.KeyMsg) tea.Cmd {
	if v.mention.query == "" {
		v.mention.open = false
		var cmd tea.Cmd
		v.noteInput, cmd = v.noteInput.Update(msg)
		return cmd
	}
	runes := []rune(v.mention.query)
	v.mention.query = string(runes[:len(runes)-1])
	var cmd tea.Cmd
	v.noteInput, cmd = v.noteInput.Update(msg)
	v.refilterMentions(a)
	return cmd
}

func (v *diffView) refilterMentions(a *app) {
	if a.session.selectedPR == nil {
		return
	}
	cached, ok := a.cache.contributors[a.session.selectedPR.Repo]
	if !ok {
		return
	}
	v.mention.list.SetItems(filterContributors(cached, v.mention.query))
	v.mention.list.ResetSelected()
}

// acceptMention replaces the typed `@<query>` fragment in the textarea
// with `@<login> ` by rewinding len(query)+1 backspaces through the
// textarea and then inserting the chosen login.
func (v *diffView) acceptMention() tea.Cmd {
	it, ok := v.mention.list.SelectedItem().(mentionItem)
	if !ok {
		v.mention.open = false
		return nil
	}
	bs := tea.KeyMsg{Type: tea.KeyBackspace}
	for i := 0; i < len([]rune(v.mention.query))+1; i++ {
		v.noteInput, _ = v.noteInput.Update(bs)
	}
	v.noteInput.InsertString("@" + it.login + " ")
	v.mention.open = false
	return nil
}

// isMentionChar is true for characters allowed in a GitHub login — typing
// one extends the filter query; anything else closes the picker.
func isMentionChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '-' || r == '_':
		return true
	}
	return false
}

func (v *diffView) updateKeys(a *app, msg tea.KeyMsg) tea.Cmd {
	if cmd, matched := dispatch(a, msg.String(), false, v.bindings(a), globalBindings()); matched {
		return cmd
	}
	var cmd tea.Cmd
	v.vp, cmd = v.vp.Update(msg)
	return cmd
}

// bindings declares every key the diff view handles. Kept data-oriented so
// help rendering and dispatch share one source of truth; agent actions are
// appended so user-configured harnesses appear here too.
//
// The diff view has three modes — base diff browsing, in-progress note,
// and the @-mention picker over the note. Each shows different shortcuts
// in help. Dispatch only ever consults the base table because Update
// routes to updateNotingKeys / updateMentionKeys before reaching the
// dispatch path in noting modes.
func (v *diffView) bindings(a *app) []Binding {
	if v.noting {
		if v.mention.open {
			return mentionPickerBindings()
		}
		return notingBindings()
	}
	return append([]Binding{
		{
			Name: "back", Keys: []string{"esc", "h", "left"},
			Desc: "back to files", Group: "Navigate",
			Action: func(a *app) tea.Cmd {
				a.layout.focus(viewFiles)
				a.files.rebuild(a)
				a.prs.rebuild(a)
				return nil
			},
		},
		{
			Name: "next-hunk", Keys: []string{"n", "down", "tab"},
			Desc: "next hunk", Group: "Navigate",
			Action: func(a *app) tea.Cmd {
				v.stepHunk(1)
				v.redraw()
				v.jumpToHunk()
				return nil
			},
		},
		{
			Name: "prev-hunk", Keys: []string{"p", "up", "shift+tab"},
			Desc: "prev hunk", Group: "Navigate",
			Action: func(a *app) tea.Cmd {
				v.stepHunk(-1)
				v.redraw()
				v.jumpToHunk()
				return nil
			},
		},
		{
			Name: "cursor-down", Keys: []string{"j"},
			Desc: "cursor down", Group: "Navigate",
			Action: func(a *app) tea.Cmd { v.moveCursor(1); return nil },
		},
		{
			Name: "cursor-up", Keys: []string{"k"},
			Desc: "cursor up", Group: "Navigate",
			Action: func(a *app) tea.Cmd { v.moveCursor(-1); return nil },
		},
		{
			Name: "advance", Keys: []string{"enter", "right"},
			Desc: "back to files (when all marked)", Group: "Navigate",
			Action: func(a *app) tea.Cmd {
				if v.allMarked() {
					a.layout.focus(viewFiles)
					a.files.rebuild(a)
					a.prs.rebuild(a)
				}
				return nil
			},
		},
		{
			Name: "toggle-mark", Keys: []string{" ", "x"},
			Desc: "toggle hunk + advance", Group: "Mark",
			Action: func(a *app) tea.Cmd {
				if v.hunkIdx < 0 || v.hunkIdx >= len(v.hunks) {
					return nil
				}
				h := v.hunks[v.hunkIdx]
				if !h.IsMarkable() {
					// Cursor is on a synthetic segment header — just advance.
					v.stepHunk(1)
					v.redraw()
					v.jumpToHunk()
					return nil
				}
				if v.marks == nil {
					v.marks = map[string]bool{}
				}
				if v.marks[h.Hash] {
					delete(v.marks, h.Hash)
				} else {
					v.marks[h.Hash] = true
				}
				v.saveMarks(a)
				v.stepHunk(1)
				v.redraw()
				v.jumpToHunk()
				return nil
			},
		},
		{
			Name: "mark-all", Keys: []string{"m"},
			Desc: "mark every hunk", Group: "Mark",
			Action: func(a *app) tea.Cmd {
				if v.marks == nil {
					v.marks = map[string]bool{}
				}
				for _, h := range v.hunks {
					if !h.IsMarkable() {
						continue
					}
					v.marks[h.Hash] = true
				}
				v.saveMarks(a)
				v.redraw()
				v.jumpToHunk()
				return nil
			},
		},
		{
			Name: "unmark-all", Keys: []string{"u"},
			Desc: "clear marks on file", Group: "Mark",
			Action: func(a *app) tea.Cmd {
				v.marks = map[string]bool{}
				v.saveMarks(a)
				v.redraw()
				v.hunkIdx = 0
				v.jumpToHunk()
				a.status.msg = "cleared marks on " + a.session.selectedFile
				return nil
			},
		},
		{
			Name: "publish-note", Keys: []string{"P"},
			Desc: "publish note at cursor to GitHub", Group: "Notes",
			Action: func(a *app) tea.Cmd {
				return v.publishNoteAtCursor(a)
			},
		},
		{
			Name: "note", Keys: []string{"c"},
			Desc: "add note at cursor", Group: "Notes",
			Action: func(a *app) tea.Cmd {
				// In segmented mode, notes key off new-file (F2) line numbers
				// — segments rendered under any other view (b1→b2, f1→f2,
				// etc.) don't have a clean F2 mapping, so block them. Most
				// primary views do end at F2; this is only a limitation for
				// a handful of classes.
				if view, ok := v.currentSegmentView(); ok && view.To != diff.F2 {
					a.status.msg = fmt.Sprintf("notes are only supported on F2 views (this segment: %s→%s)", view.From, view.To)
					return nil
				}
				lineNo := v.cursorFileLine()
				if lineNo == 0 {
					a.status.msg = "cursor not on a file line"
					return nil
				}
				v.noting = true
				v.noteLineNo = lineNo
				v.noteLineHash = v.cursorLineHash(lineNo)
				v.noteInput.Reset()
				return v.noteInput.Focus()
			},
		},
		{
			Name: "cycle-view", Keys: []string{"v"},
			Desc: "cycle segment view", Group: "View",
			Action: func(a *app) tea.Cmd {
				if !v.segmented || len(v.segments) == 0 {
					return nil
				}
				if diff.MaxSegmentViews(v.segments) <= 1 {
					a.status.msg = "no alternate views for these segments"
					return nil
				}
				v.segmentViewIdx++
				v.hunks = diff.SegmentHunks(v.segments, v.segmentViewIdx)
				v.hunkIdx = firstUnmarked(v.hunks, v.marks)
				v.cursorLine = 0
				maxV := diff.MaxSegmentViews(v.segments)
				a.status.msg = fmt.Sprintf("view %d/%d", (v.segmentViewIdx%maxV)+1, maxV)
				v.redraw()
				v.jumpToHunk()
				return nil
			},
		},
		{
			Name: "catch-up-toggle", Keys: []string{"d"},
			Desc: "toggle catch-up / full diff", Group: "View",
			Action: func(a *app) tea.Cmd {
				if v.catchUpOldHead == "" {
					return nil
				}
				fc, ok := a.currentFile()
				if !ok {
					return nil
				}
				if v.catchUpMode {
					v.catchUpMode = false
					v.segmented = false
					v.hunks = diff.ParseHunks(fc.Patch)
					v.marks = a.brain.HunkMarks(a.session.selectedPR.Repo, a.session.selectedPR.Number, fc.Path)
					v.hunkIdx = firstUnmarked(v.hunks, v.marks)
					a.status.msg = "full diff  (d: catch-up diff)"
				} else {
					v.catchUpMode = true
					if len(v.segments) > 0 {
						v.hunks = diff.SegmentHunks(v.segments, v.segmentViewIdx)
						v.segmented = true
						a.status.msg = fmt.Sprintf("catch-up [%s]: %d segments since %s  (d: full diff)", v.catchUpClass, len(v.segments), shortSHA(v.catchUpOldHead))
					} else {
						v.hunks = diff.ParseHunks(v.catchUpPatch)
						v.segmented = false
						a.status.msg = fmt.Sprintf("catch-up [%s]: changes since %s  (d: full diff)", v.catchUpClass, shortSHA(v.catchUpOldHead))
					}
					v.marks = a.brain.HunkMarks(a.session.selectedPR.Repo, a.session.selectedPR.Number, fc.Path)
					v.hunkIdx = firstUnmarked(v.hunks, v.marks)
				}
				v.redraw()
				v.jumpToHunk()
				return nil
			},
		},
		{
			Name: "open-editor", Keys: []string{"o"},
			Desc: "open file in editor", Group: "View",
			Action: func(a *app) tea.Cmd {
				cmd, err := v.openInEditor(a)
				if err != nil {
					a.status.msg = "open: " + err.Error()
					return nil
				}
				return cmd
			},
		},
	}, agentBindings(a.cfg)...)
}

// notingBindings are display-only entries shown in the help overlay while
// the user is composing a note. The actual key handling lives in
// updateNotingKeys; Action is nil because dispatch never sees these.
func notingBindings() []Binding {
	return []Binding{
		{Name: "save-note", Keys: []string{"ctrl+d"}, Desc: "save note", Group: "Notes"},
		{Name: "cancel-note", Keys: []string{"esc"}, Desc: "cancel without saving", Group: "Notes"},
		{Name: "mention", Keys: []string{"@"}, Desc: "open @-mention picker (at word boundary)", Group: "Notes"},
	}
}

// mentionPickerBindings are display-only entries shown in the help overlay
// while the @-mention picker is open over the note textarea. Routing lives
// in updateMentionKeys.
func mentionPickerBindings() []Binding {
	return []Binding{
		{Name: "mention-nav", Keys: []string{"up", "down"}, Desc: "move selection", Group: "Mention"},
		{Name: "mention-filter", Keys: []string{"a-z, 0-9, -, _"}, Desc: "type to filter contributors", Group: "Mention"},
		{Name: "mention-accept", Keys: []string{"enter"}, Desc: "insert @login and close", Group: "Mention"},
		{Name: "mention-close", Keys: []string{"esc"}, Desc: "close picker, leave text untouched", Group: "Mention"},
		{Name: "mention-backspace", Keys: []string{"backspace"}, Desc: "delete a query char (or @ to close)", Group: "Mention"},
	}
}

func (v *diffView) restoreSize(a *app) {
	h, padV := appStyle.GetFrameSize()
	v.vp.Width = a.layout.width - h
	v.vp.Height = a.layout.height - padV - 1
}

// publishNoteAtCursor posts the first unpublished note on the cursor's
// current file line as a GitHub inline review comment. Per-line selection
// (vs "everything on the file") is intentional: publication is a decision
// the reviewer makes per-comment, not in bulk.
//
// GitHub anchors inline comments to a (commit_id, path, line) tuple, and
// only accepts commits that are part of the PR. We use the PR's current
// HeadSHA; if the PR is rebased later, old comments outline themselves
// (GitHub's normal behaviour).
func (v *diffView) publishNoteAtCursor(a *app) tea.Cmd {
	if a.session.selectedPR == nil || a.session.selectedFile == "" {
		return nil
	}
	lineNo := v.cursorFileLine()
	if lineNo == 0 {
		a.status.msg = "cursor not on a file line"
		return nil
	}
	var target *brain.Note
	for i := range v.notes {
		n := &v.notes[i]
		if n.LineNo == lineNo && n.GitHubCommentID == 0 {
			target = n
			break
		}
	}
	if target == nil {
		a.status.msg = fmt.Sprintf("no unpublished note on line %d", lineNo)
		return nil
	}
	pr := *a.session.selectedPR
	path := a.session.selectedFile
	noteID := target.ID
	body := target.Body
	commit := pr.HeadSHA
	a.status.msg = fmt.Sprintf("publishing note on %s:%d…", path, lineNo)
	return func() tea.Msg {
		ghID, err := gh.PostInlineComment(pr.Repo, pr.Number, gh.InlineComment{
			Body:     body,
			Path:     path,
			CommitID: commit,
			Line:     lineNo,
		})
		return notePublishedMsg{noteID: noteID, ghID: ghID, err: err}
	}
}

func firstUnmarked(hunks []diff.Hunk, marks map[string]bool) int {
	for i, h := range hunks {
		if !h.IsMarkable() {
			continue
		}
		if !marks[h.Hash] {
			return i
		}
	}
	// Nothing unmarked — fall back to the first markable hunk so the cursor
	// doesn't land on a synthetic segment header.
	for i, h := range hunks {
		if h.IsMarkable() {
			return i
		}
	}
	return 0
}

package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// --- diffView ---

type diffView struct {
	vp        viewport.Model
	noteInput textarea.Model

	hunks      []Hunk
	marks      map[string]bool
	notes      []Note
	hunkLines  []int
	lineMap    []int // output line → new-file line number (0 = non-file line)
	hunkIdx    int
	cursorLine int
	diffLines  []string // raw content lines for manual rendering in noting mode
	blob       string   // full file content, empty until blob loads

	// Catch-up diff state.
	catchUpMode    bool   // true when showing only the delta since last review
	catchUpOldHead string // the head SHA we last reviewed at (f1)
	catchUpOldBase string // the base SHA we last reviewed at (b1)
	catchUpClass   Class  // diff4 classification of the catch-up
	catchUpPatch   string // the delta patch for the current file

	// Note input state.
	noting       bool
	noteLineNo   int
	noteLineHash string
}

func newDiffView() diffView {
	ti := textarea.New()
	ti.Placeholder = "Write a note... (ctrl+d to save, esc to cancel)"
	ti.SetHeight(3)
	ti.ShowLineNumbers = false
	return diffView{
		vp:        viewport.New(0, 0),
		noteInput: ti,
	}
}

func (v *diffView) Resize(w, h int) {
	v.vp.Width = w
	v.vp.Height = h
}

func (v *diffView) View(a *app) string {
	if v.noting {
		return v.renderNotingView(a)
	}
	return v.vp.View()
}

func (v *diffView) Footer(a *app) string {
	if v.noting {
		return fmt.Sprintf("line %d  ctrl+d: save  esc: cancel", v.noteLineNo)
	}
	marked := 0
	for _, h := range v.hunks {
		if v.marks[h.Hash] {
			marked++
		}
	}
	total := len(v.hunks)
	cur := v.hunkIdx + 1
	if total == 0 {
		cur = 0
	}
	modeHint := ""
	if v.catchUpOldHead != "" {
		if v.catchUpMode {
			modeHint = fmt.Sprintf("  [catch-up %s since %s]  d: full diff", v.catchUpClass, shortSHA(v.catchUpOldHead))
		} else {
			modeHint = "  [full diff]  d: catch-up"
		}
	}
	return fmt.Sprintf("hunk %d/%d  marked %d/%d%s  ↑/↓: nav  j/k: cursor  space: toggle+next  m: mark all  c: note  o: open in editor  u: unmark  h: back", cur, total, marked, total, modeHint)
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
func (v *diffView) open(a *app, fc FileChange) tea.Cmd {
	a.selectedFile = fc.Path
	a.activeView = viewDiff
	v.blob = ""
	v.catchUpMode = false
	v.catchUpOldHead = ""
	v.catchUpOldBase = ""
	v.catchUpClass = ClassB1B2F1F2
	v.catchUpPatch = ""

	revState := a.brain.FileReviewedState(a.selectedPR.Repo, a.selectedPR.Number, fc.Path)
	scrutinized := a.brain.IsScrutinized(a.selectedPR.Repo, a.selectedPR.Number)
	needsCatchUp := !scrutinized && revState.HeadSHA != "" && (revState.HeadSHA != a.selectedPR.HeadSHA || revState.BaseSHA != a.selectedPR.BaseSHA)

	v.hunks = parseHunks(fc.Patch)
	v.marks = a.brain.HunkMarks(a.selectedPR.Repo, a.selectedPR.Number, fc.Path)
	v.notes = a.brain.NotesForFile(a.selectedPR.Repo, a.selectedPR.Number, fc.Path)
	v.hunkIdx = firstUnmarked(v.hunks, v.marks)
	v.redraw()
	v.jumpToHunk()

	var cmds []tea.Cmd

	if needsCatchUp {
		v.catchUpOldHead = revState.HeadSHA
		v.catchUpOldBase = revState.BaseSHA
		repo := a.selectedPR.Repo
		oldHead := revState.HeadSHA
		oldBase := revState.BaseSHA
		newHead := a.selectedPR.HeadSHA
		newBase := a.selectedPR.BaseSHA
		path := fc.Path
		rebased := oldBase != newBase && oldBase != ""

		if rebased {
			a.statusMsg = fmt.Sprintf("classifying diamond (rebase %s→%s)", shortSHA(oldBase), shortSHA(newBase))
			cmds = append(cmds, func() tea.Msg {
				b1, _ := fetchFileAtRef(repo, path, oldBase)
				f1, _ := fetchFileAtRef(repo, path, oldHead)
				b2, _ := fetchFileAtRef(repo, path, newBase)
				f2, _ := fetchFileAtRef(repo, path, newHead)
				d := Diamond{B1: b1, F1: f1, B2: b2, F2: f2}
				class := Classify(d, nil)
				var patch string
				if !class.Hidden() {
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
			a.statusMsg = fmt.Sprintf("loading catch-up diff %s..%s", shortSHA(oldHead), shortSHA(newHead))
			cmds = append(cmds, func() tea.Msg {
				files, err := fetchCompare(repo, oldHead, newHead)
				return catchUpLoadedMsg{path: path, files: files, err: err}
			})
		}
	}

	if fc.Blob != "" {
		repo := a.selectedPR.Repo
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

// --- async message callbacks ---

func (v *diffView) onCatchUpLoaded(a *app, msg catchUpLoadedMsg) tea.Cmd {
	if msg.err != nil {
		a.statusMsg = "catch-up: " + msg.err.Error()
		return nil
	}
	if a.activeView != viewDiff || a.selectedFile != msg.path {
		return nil
	}
	var deltaFC *FileChange
	for _, f := range msg.files {
		if f.Path == msg.path {
			deltaFC = &f
			break
		}
	}
	if deltaFC == nil || deltaFC.Patch == "" {
		v.catchUpMode = false
		v.catchUpClass = ClassB1B2__F1F2
		a.statusMsg = fmt.Sprintf("✓ %s: %s (auto-caught-up)", a.selectedFile, ClassB1B2__F1F2)
		a.brain.SetFileReviewed(a.selectedPR.Repo, a.selectedPR.Number, a.selectedFile, a.selectedPR.HeadSHA, a.selectedPR.BaseSHA)
		a.advanceCatchUpSession()
		return nil
	}
	v.catchUpMode = true
	v.catchUpClass = ClassB1B2
	v.catchUpPatch = deltaFC.Patch
	v.hunks = parseHunks(deltaFC.Patch)
	v.marks = a.brain.HunkMarks(a.selectedPR.Repo, a.selectedPR.Number, a.selectedFile)
	v.hunkIdx = firstUnmarked(v.hunks, v.marks)
	a.statusMsg = fmt.Sprintf("catch-up [%s]: f1→f2 since %s  (d: full diff)", ClassB1B2, shortSHA(v.catchUpOldHead))
	v.redraw()
	v.jumpToHunk()
	return nil
}

func (v *diffView) onDiamondClassified(a *app, msg diamondClassifiedMsg) tea.Cmd {
	if msg.err != nil {
		a.statusMsg = "classify: " + msg.err.Error()
		return nil
	}
	if a.activeView != viewDiff || a.selectedFile != msg.path {
		return nil
	}
	v.catchUpClass = msg.class

	if msg.class.Hidden() {
		v.catchUpMode = false
		label := msg.class.String()
		if msg.class.IsForget() {
			label = "FORGET — base absorbed feature"
		}
		a.statusMsg = fmt.Sprintf("✓ %s: %s (auto-caught-up)", a.selectedFile, label)
		a.brain.SetFileReviewed(a.selectedPR.Repo, a.selectedPR.Number, a.selectedFile, a.selectedPR.HeadSHA, a.selectedPR.BaseSHA)
		a.advanceCatchUpSession()
		return nil
	}

	v.catchUpMode = true
	if msg.patch != "" {
		v.catchUpPatch = msg.patch
		v.hunks = parseHunks(msg.patch)
	}
	v.marks = a.brain.HunkMarks(a.selectedPR.Repo, a.selectedPR.Number, a.selectedFile)
	v.hunkIdx = firstUnmarked(v.hunks, v.marks)
	views := msg.class.Views()
	viewLabel := ""
	if len(views) > 0 {
		viewLabel = fmt.Sprintf("%s→%s", views[0].From, views[0].To)
	}
	a.statusMsg = fmt.Sprintf("catch-up [%s]: %s  (d: full diff)", msg.class, viewLabel)
	v.redraw()
	v.jumpToHunk()
	return nil
}

func (v *diffView) onBlobLoaded(a *app, msg blobLoadedMsg) tea.Cmd {
	if msg.err != nil {
		a.statusMsg = "blob: " + msg.err.Error()
		return nil
	}
	if a.activeView == viewDiff {
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
	if v.blob != "" {
		body, lines, lmap = renderFullFile(v.blob, v.hunks, v.marks, v.hunkIdx, v.notes, v.cursorLine)
	} else {
		body, lines, lmap = renderHunks(v.hunks, v.marks, v.hunkIdx, v.notes, v.cursorLine)
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
	for _, h := range v.hunks {
		if !v.marks[h.Hash] {
			return false
		}
	}
	return len(v.hunks) > 0
}

func (v *diffView) renderNotingView(a *app) string {
	_, padV := appStyle.GetFrameSize()
	totalH := a.height - padV - 1 // minus footer
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
	if a.selectedPR == nil || a.selectedFile == "" {
		return
	}
	if err := a.brain.SetHunkMarks(a.selectedPR.Repo, a.selectedPR.Number, a.selectedFile, v.marks); err != nil {
		a.statusMsg = "save error: " + err.Error()
		return
	}
	if a.selectedPR.HeadSHA != "" {
		a.brain.SetFileReviewed(a.selectedPR.Repo, a.selectedPR.Number, a.selectedFile, a.selectedPR.HeadSHA, a.selectedPR.BaseSHA)
	}
}

// --- editor launch ---

func (v *diffView) openInEditor(a *app) (tea.Cmd, error) {
	if a.selectedPR == nil || a.selectedFile == "" {
		return nil, fmt.Errorf("nothing selected")
	}
	worktree, err := resolveWorktree(a.cfg, a.selectedPR.Repo, a.selectedPR.Number)
	if err != nil {
		return nil, err
	}
	line := 1
	if v.hunkIdx >= 0 && v.hunkIdx < len(v.hunks) {
		if _, n := hunkLines(v.hunks[v.hunkIdx].Header); n > 0 {
			line = n
		}
	}
	prKeyStr := fmt.Sprintf("%s#%d", a.selectedPR.Repo, a.selectedPR.Number)
	a.statusMsg = fmt.Sprintf("opening %s:%d in %s", a.selectedFile, line, worktree)
	return launchEditor(a.cfg, worktree, a.selectedFile, prKeyStr, line), nil
}

// --- key handling ---

func (v *diffView) updateNotingKeys(a *app, msg tea.KeyMsg) tea.Cmd {
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
			if err := a.brain.SaveNote(a.selectedPR.Repo, a.selectedPR.Number, a.selectedFile, v.noteLineNo, v.noteLineHash, body); err != nil {
				a.statusMsg = "save note: " + err.Error()
			} else {
				v.notes = a.brain.NotesForFile(a.selectedPR.Repo, a.selectedPR.Number, a.selectedFile)
				v.redraw()
			}
		}
		return nil
	}
	var cmd tea.Cmd
	v.noteInput, cmd = v.noteInput.Update(msg)
	return cmd
}

func (v *diffView) updateKeys(a *app, msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+c", "q":
		return tea.Quit
	case "esc", "h", "left":
		a.activeView = viewFiles
		a.files.rebuild(a)
		a.prs.rebuild(a)
		return nil
	case "n", "down", "tab":
		if len(v.hunks) > 0 && v.hunkIdx < len(v.hunks)-1 {
			v.hunkIdx++
			v.redraw()
			v.jumpToHunk()
		}
		return nil
	case "p", "up", "shift+tab":
		if v.hunkIdx > 0 {
			v.hunkIdx--
			v.redraw()
			v.jumpToHunk()
		}
		return nil
	case " ", "x":
		if v.hunkIdx >= 0 && v.hunkIdx < len(v.hunks) {
			h := v.hunks[v.hunkIdx]
			if v.marks == nil {
				v.marks = map[string]bool{}
			}
			if v.marks[h.Hash] {
				delete(v.marks, h.Hash)
			} else {
				v.marks[h.Hash] = true
			}
			v.saveMarks(a)
			if v.hunkIdx < len(v.hunks)-1 {
				v.hunkIdx++
			}
			v.redraw()
			v.jumpToHunk()
		}
		return nil
	case "m":
		if v.marks == nil {
			v.marks = map[string]bool{}
		}
		for _, h := range v.hunks {
			v.marks[h.Hash] = true
		}
		v.saveMarks(a)
		v.redraw()
		v.jumpToHunk()
		return nil
	case "enter", "right":
		if v.allMarked() {
			a.activeView = viewFiles
			a.files.rebuild(a)
			a.prs.rebuild(a)
		}
		return nil
	case "j":
		v.moveCursor(1)
		return nil
	case "k":
		v.moveCursor(-1)
		return nil
	case "u":
		v.marks = map[string]bool{}
		v.saveMarks(a)
		v.redraw()
		v.hunkIdx = 0
		v.jumpToHunk()
		a.statusMsg = "cleared marks on " + a.selectedFile
		return nil
	case "d":
		if v.catchUpOldHead == "" {
			return nil
		}
		fc, ok := a.currentFile()
		if !ok {
			return nil
		}
		if v.catchUpMode {
			v.catchUpMode = false
			v.hunks = parseHunks(fc.Patch)
			v.marks = a.brain.HunkMarks(a.selectedPR.Repo, a.selectedPR.Number, fc.Path)
			v.hunkIdx = firstUnmarked(v.hunks, v.marks)
			a.statusMsg = "full diff  (d: catch-up diff)"
		} else {
			v.catchUpMode = true
			v.hunks = parseHunks(v.catchUpPatch)
			v.marks = a.brain.HunkMarks(a.selectedPR.Repo, a.selectedPR.Number, fc.Path)
			v.hunkIdx = firstUnmarked(v.hunks, v.marks)
			a.statusMsg = fmt.Sprintf("catch-up [%s]: changes since %s  (d: full diff)", v.catchUpClass, shortSHA(v.catchUpOldHead))
		}
		v.redraw()
		v.jumpToHunk()
		return nil
	case "c":
		lineNo := v.cursorFileLine()
		if lineNo == 0 {
			a.statusMsg = "cursor not on a file line"
			return nil
		}
		v.noting = true
		v.noteLineNo = lineNo
		v.noteLineHash = v.cursorLineHash(lineNo)
		v.noteInput.Reset()
		return v.noteInput.Focus()
	case "o":
		cmd, err := v.openInEditor(a)
		if err != nil {
			a.statusMsg = "open: " + err.Error()
			return nil
		}
		return cmd
	}
	var cmd tea.Cmd
	v.vp, cmd = v.vp.Update(msg)
	return cmd
}

func (v *diffView) restoreSize(a *app) {
	h, padV := appStyle.GetFrameSize()
	v.vp.Width = a.width - h
	v.vp.Height = a.height - padV - 1
}

func firstUnmarked(hunks []Hunk, marks map[string]bool) int {
	for i, h := range hunks {
		if !marks[h.Hash] {
			return i
		}
	}
	return 0
}

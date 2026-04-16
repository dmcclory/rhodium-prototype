package main

import (
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// program is a package-level handle to the running tea program, set in main
// before Run(). Goroutines spawned from tea.Cmd's use it to push messages
// back onto the update loop.
var program *tea.Program

type view int

const (
	viewPRs view = iota
	viewFiles
	viewDiff
)

type fileTab int

const (
	tabFiles fileTab = iota
	tabDescription
	tabNotes
)

// --- list items ---

type prItem struct {
	pr        PR
	summary   string
	noteCount int
}

func (i prItem) Title() string {
	head := fmt.Sprintf("%s#%d  %s  @%s", i.pr.Repo, i.pr.Number, i.pr.Title, i.pr.Author)
	var parts []string
	if i.summary != "" {
		parts = append(parts, i.summary)
	}
	if i.noteCount > 0 {
		parts = append(parts, fmt.Sprintf("%d notes", i.noteCount))
	}
	if len(parts) > 0 {
		head += "  (" + strings.Join(parts, ", ") + ")"
	}
	return head
}
func (i prItem) Description() string { return "" }
func (i prItem) FilterValue() string { return i.Title() }

type fileItem struct {
	fc         FileChange
	status     FileStatus
	noteCount  int
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

// sectionItem is a non-interactive header used to group list entries into
// "in progress" / "unseen" buckets. Enter/l handlers ignore it via type
// assertion.
type sectionItem struct{ label string }

var sectionHeaderStyle = lipgloss.NewStyle().Faint(true).Bold(true)

func (s sectionItem) Title() string       { return sectionHeaderStyle.Render(s.label) }
func (s sectionItem) Description() string { return "" }
func (s sectionItem) FilterValue() string { return "" }

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// --- messages ---

type prsLoadedMsg struct {
	prs []PR
	err error
}
type filesLoadedMsg struct {
	pr    PR
	files []FileChange
	err   error
}
type prefetchDoneMsg struct{}
type blobLoadedMsg struct {
	content string
	err     error
}
type catchUpLoadedMsg struct {
	path  string
	files []FileChange // delta files from compare API
	err   error
}

// --- model ---

type model struct {
	cfg    *Config
	brain  *Brain
	view   view
	width  int
	height int

	prs   list.Model
	files list.Model
	diff  viewport.Model

	fileTab      fileTab
	infoVP       viewport.Model // for description / notes tabs

	allPRs       []PR
	freshKeys    map[string]bool         // keys confirmed still open by a repo listing
	prFiles      map[string][]FileChange // prKey → files
	selectedPR   *PR
	selectedFile string

	// Diff view state.
	currentHunks []Hunk
	currentMarks map[string]bool
	currentNotes []Note
	hunkLines    []int
	lineMap      []int // output line → new-file line number (0 = non-file line)
	hunkIdx      int
	cursorLine   int      // output line the cursor is on
	diffLines    []string // raw content lines for manual rendering in noting mode
	blobContent  string   // full file content, empty until blob loads

	// Catch-up diff state.
	catchUpMode     bool   // true when showing only the delta since last review
	catchUpOldHead  string // the head SHA we last reviewed at
	catchUpPatch    string // the delta patch for the current file

	// Note input state.
	noting       bool
	noteLineNo   int
	noteLineHash string
	noteInput    textarea.Model

	loadingFiles bool
	statusMsg    string
}

func compactDelegate() list.DefaultDelegate {
	d := list.NewDefaultDelegate()
	d.ShowDescription = false
	d.SetSpacing(0)
	return d
}

func newModel(cfg *Config, brain *Brain) model {
	prList := list.New(nil, compactDelegate(), 0, 0)

	fileList := list.New(nil, compactDelegate(), 0, 0)
	fileList.Title = "Files"

	vp := viewport.New(0, 0)
	infoVP := viewport.New(0, 0)

	ti := textarea.New()
	ti.Placeholder = "Write a note... (ctrl+d to save, esc to cancel)"
	ti.SetHeight(3)
	ti.ShowLineNumbers = false

	m := model{
		cfg:     cfg,
		brain:   brain,
		view:    viewPRs,
		prs:     prList,
		files:   fileList,
		diff:    vp,
		noteInput: ti,
		infoVP:    infoVP,
		prFiles:   map[string][]FileChange{},
		freshKeys: map[string]bool{},
	}

	cached := brain.CachedPRs()
	if len(cached) > 0 {
		m.allPRs = cached
		m.rebuildPRItems()
		m.prs.Title = fmt.Sprintf("PRs (%d, refreshing…)", len(cached))
	} else {
		m.prs.Title = "PRs (loading...)"
	}

	return m
}

func (m model) Init() tea.Cmd {
	cmds := make([]tea.Cmd, len(m.cfg.Repos))
	for i, repo := range m.cfg.Repos {
		cmds[i] = loadRepoPRsCmd(repo)
	}
	return tea.Batch(cmds...)
}

// loadRepoPRsCmd fetches PRs for a single repo and returns a prsLoadedMsg.
// Runs one per repo via tea.Batch so each repo renders independently — the
// in-progress bucket stays stable at the top; untouched PRs fill in below as
// they arrive.
func loadRepoPRsCmd(repo string) tea.Cmd {
	return func() tea.Msg {
		prs, err := listPRs(repo)
		if err != nil {
			return prsLoadedMsg{err: fmt.Errorf("%s: %w", repo, err)}
		}
		return prsLoadedMsg{prs: prs}
	}
}

func loadFilesCmd(pr PR) tea.Cmd {
	return func() tea.Msg {
		return fetchOne(pr)
	}
}

func fetchOne(pr PR) filesLoadedMsg {
	files, err := listPRFiles(pr.Repo, pr.Number)
	if err != nil {
		return filesLoadedMsg{pr: pr, err: err}
	}
	return filesLoadedMsg{pr: pr, files: files}
}

func prefetchAllCmd(prs []PR) tea.Cmd {
	const workers = 4
	return func() tea.Msg {
		jobs := make(chan PR)
		done := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for pr := range jobs {
					program.Send(fetchOne(pr))
				}
			}()
		}
		go func() {
			for _, pr := range prs {
				jobs <- pr
			}
			close(jobs)
			wg.Wait()
			close(done)
		}()
		<-done
		return prefetchDoneMsg{}
	}
}

// mergePRs appends PRs whose (repo, number) aren't already in m.allPRs and
// returns just the newly-added ones, so callers can kick off file prefetch
// without redundantly re-fetching PRs already loaded.
func (m *model) mergePRs(prs []PR) []PR {
	seen := make(map[string]bool, len(m.allPRs))
	for _, p := range m.allPRs {
		seen[prKey(p.Repo, p.Number)] = true
	}
	var added []PR
	for _, p := range prs {
		k := prKey(p.Repo, p.Number)
		if seen[k] {
			continue
		}
		seen[k] = true
		m.allPRs = append(m.allPRs, p)
		added = append(added, p)
	}
	return added
}

func (m *model) rebuildPRItems() {
	// Remember which PR the cursor is on so rebuild doesn't jump it.
	var savedKey string
	if sel, ok := m.prs.SelectedItem().(prItem); ok {
		savedKey = prKey(sel.pr.Repo, sel.pr.Number)
	}

	var inProgress, untouched []prItem
	for _, pr := range m.allPRs {
		it := prItem{pr: pr, noteCount: m.brain.NoteCountForPR(pr.Repo, pr.Number)}
		// A PR is "in progress" if the brain has any marks for it, even
		// before we've fetched its file list. This keeps already-touched
		// PRs from popping between buckets during startup prefetch.
		looked := m.brain.HasAnyMarks(pr.Repo, pr.Number)
		if files, ok := m.prFiles[prKey(pr.Repo, pr.Number)]; ok {
			unseen := m.brain.UnseenCount(pr.Repo, pr.Number, files)
			if unseen == 0 {
				it.summary = "✓ caught up"
			} else {
				it.summary = fmt.Sprintf("%d new", unseen)
			}
			// Check if any files need catch-up (PR head moved since last review).
			reviewedHeads := m.brain.AllFileReviewedHeads(pr.Repo, pr.Number)
			catchUpCount := 0
			for _, f := range files {
				if old := reviewedHeads[f.Path]; old != "" && old != pr.HeadSHA {
					catchUpCount++
				}
			}
			if catchUpCount > 0 {
				it.summary += fmt.Sprintf(", %d ↻", catchUpCount)
			}
		}
		if looked {
			inProgress = append(inProgress, it)
		} else {
			untouched = append(untouched, it)
		}
	}

	var items []list.Item
	if len(inProgress) > 0 {
		items = append(items, sectionItem{label: "── in progress ──"})
		for _, it := range inProgress {
			items = append(items, it)
		}
	}
	if len(untouched) > 0 {
		if len(inProgress) > 0 {
			items = append(items, sectionItem{label: "── new ──"})
		}
		for _, it := range untouched {
			items = append(items, it)
		}
	}
	m.prs.SetItems(items)
	if savedKey != "" {
		for i, it := range items {
			if pi, ok := it.(prItem); ok && prKey(pi.pr.Repo, pi.pr.Number) == savedKey {
				m.prs.Select(i)
				break
			}
		}
	}
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
	reviewedHeads := m.brain.AllFileReviewedHeads(m.selectedPR.Repo, m.selectedPR.Number)
	var unseen, partial, seen []fileItem
	for _, fc := range files {
		status := m.brain.Status(m.selectedPR.Repo, m.selectedPR.Number, fc)
		nc := m.brain.NoteCountForFile(m.selectedPR.Repo, m.selectedPR.Number, fc.Path)
		oldHead := reviewedHeads[fc.Path]
		catchUp := oldHead != "" && oldHead != m.selectedPR.HeadSHA
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
		{"[2] Description", tabDescription},
		{"[3] Notes", tabNotes},
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

func (m *model) rebuildInfoVP() {
	if m.selectedPR == nil {
		return
	}
	var content string
	switch m.fileTab {
	case tabDescription:
		body := m.selectedPR.Body
		if body == "" {
			body = "(no description)"
		}
		content = fmt.Sprintf("%s#%d  %s  @%s\n\n%s",
			m.selectedPR.Repo, m.selectedPR.Number, m.selectedPR.Title, m.selectedPR.Author, body)
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

func (m *model) currentFile() (FileChange, bool) {
	if m.selectedPR == nil {
		return FileChange{}, false
	}
	for _, f := range m.prFiles[prKey(m.selectedPR.Repo, m.selectedPR.Number)] {
		if f.Path == m.selectedFile {
			return f, true
		}
	}
	return FileChange{}, false
}

// openFile loads a file into the diff view: parse hunks, seed marks from the
// brain, show patch view immediately, then kick off blob fetch for full file.
//
// If the PR head has moved since this file was last reviewed, we enter catch-up
// mode: fetch only the delta and show that instead of the full PR diff.
func (m *model) openFile(fc FileChange) tea.Cmd {
	m.selectedFile = fc.Path
	m.view = viewDiff
	m.blobContent = ""
	m.catchUpMode = false
	m.catchUpOldHead = ""
	m.catchUpPatch = ""

	// Check if this file was previously reviewed at an older head.
	oldHead := m.brain.FileReviewedHead(m.selectedPR.Repo, m.selectedPR.Number, fc.Path)
	needsCatchUp := oldHead != "" && oldHead != m.selectedPR.HeadSHA

	m.currentHunks = parseHunks(fc.Patch)
	m.currentMarks = m.brain.HunkMarks(m.selectedPR.Repo, m.selectedPR.Number, fc.Path)
	m.currentNotes = m.brain.NotesForFile(m.selectedPR.Repo, m.selectedPR.Number, fc.Path)
	m.hunkIdx = firstUnmarked(m.currentHunks, m.currentMarks)
	m.redrawDiff()
	m.jumpToCurrentHunk()

	var cmds []tea.Cmd

	if needsCatchUp {
		m.catchUpOldHead = oldHead
		m.statusMsg = fmt.Sprintf("loading catch-up diff %s..%s", shortSHA(oldHead), shortSHA(m.selectedPR.HeadSHA))
		repo := m.selectedPR.Repo
		newHead := m.selectedPR.HeadSHA
		path := fc.Path
		cmds = append(cmds, func() tea.Msg {
			files, err := fetchCompare(repo, oldHead, newHead)
			return catchUpLoadedMsg{path: path, files: files, err: err}
		})
	}

	if fc.Blob != "" {
		repo := m.selectedPR.Repo
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

func firstUnmarked(hunks []Hunk, marks map[string]bool) int {
	for i, h := range hunks {
		if !marks[h.Hash] {
			return i
		}
	}
	return 0
}

func (m *model) redrawDiff() {
	if len(m.currentHunks) == 0 {
		m.diff.SetContent("(no hunks — nothing to review)")
		m.hunkLines = nil
		return
	}
	var body string
	var lines []int
	var lmap []int
	if m.blobContent != "" {
		body, lines, lmap = renderFullFile(m.blobContent, m.currentHunks, m.currentMarks, m.hunkIdx, m.currentNotes, m.cursorLine)
	} else {
		body, lines, lmap = renderHunks(m.currentHunks, m.currentMarks, m.hunkIdx, m.currentNotes, m.cursorLine)
	}
	m.diff.SetContent(body)
	m.diffLines = strings.Split(body, "\n")
	m.hunkLines = lines
	m.lineMap = lmap
}

func (m *model) jumpToCurrentHunk() {
	if m.hunkIdx < 0 || m.hunkIdx >= len(m.hunkLines) {
		return
	}
	target := m.hunkLines[m.hunkIdx]
	m.cursorLine = target
	m.diff.SetYOffset(target)
}

func (m *model) allHunksMarked() bool {
	for _, h := range m.currentHunks {
		if !m.currentMarks[h.Hash] {
			return false
		}
	}
	return len(m.currentHunks) > 0
}

func (m *model) renderNotingView() string {
	_, v := appStyle.GetFrameSize()
	totalH := m.height - v - 1 // minus footer
	taHeight := m.noteInput.Height() + 2
	diffH := totalH - taHeight

	contentLines := m.diffLines

	// Where does the cursor sit relative to the current scroll?
	screenPos := m.cursorLine - m.diff.YOffset
	yOff := m.diff.YOffset

	// If cursor is too close to the bottom, scroll up to make room for the
	// textarea right under the cursor line.
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

	// Lines above textarea: from yOff to cursor line (inclusive).
	aboveCount := screenPos + 1
	// Lines below textarea fill the rest.
	belowCount := diffH - aboveCount

	var b strings.Builder
	// Above section.
	for i := 0; i < aboveCount; i++ {
		idx := yOff + i
		if idx < len(contentLines) {
			b.WriteString(contentLines[idx])
		}
		b.WriteByte('\n')
	}
	// Textarea.
	b.WriteString(m.noteInput.View())
	b.WriteByte('\n')
	// Below section.
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

func (m *model) restoreDiffSize() {
	h, v := appStyle.GetFrameSize()
	m.diff.Width = m.width - h
	m.diff.Height = m.height - v - 1
}

func (m *model) moveCursor(delta int) {
	next := m.cursorLine + delta
	if next < 0 {
		next = 0
	}
	if max := len(m.lineMap) - 1; max >= 0 && next > max {
		next = max
	}
	m.cursorLine = next
	m.redrawDiff()
	// Scroll viewport to keep cursor visible.
	if m.cursorLine < m.diff.YOffset {
		m.diff.SetYOffset(m.cursorLine)
	} else if m.cursorLine >= m.diff.YOffset+m.diff.Height {
		m.diff.SetYOffset(m.cursorLine - m.diff.Height + 1)
	}
}

func (m *model) cursorFileLine() int {
	if m.cursorLine < 0 || m.cursorLine >= len(m.lineMap) {
		return 0
	}
	return m.lineMap[m.cursorLine]
}

func (m *model) cursorLineHash(lineNo int) string {
	if m.blobContent == "" {
		return ""
	}
	lines := strings.Split(m.blobContent, "\n")
	idx := lineNo - 1
	if idx < 0 || idx >= len(lines) {
		return ""
	}
	return hashLine(lines[idx])
}

func hashLine(s string) string {
	h := hashHunkBody([]string{"+" + s})
	return h
}

func (m *model) saveMarks() {
	if m.selectedPR == nil || m.selectedFile == "" {
		return
	}
	if err := m.brain.SetHunkMarks(m.selectedPR.Repo, m.selectedPR.Number, m.selectedFile, m.currentMarks); err != nil {
		m.statusMsg = "save error: " + err.Error()
		return
	}
	// Record the PR head SHA we're reviewing against so catch-up diffs
	// know what version we last saw.
	if m.selectedPR.HeadSHA != "" {
		m.brain.SetFileReviewed(m.selectedPR.Repo, m.selectedPR.Number, m.selectedFile, m.selectedPR.HeadSHA)
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		h, v := appStyle.GetFrameSize()
		listW, listH := msg.Width-h, msg.Height-v-1
		m.prs.SetSize(listW, listH)
		m.files.SetSize(listW, listH)
		m.diff.Width = listW
		m.diff.Height = listH
		m.infoVP.Width = listW
		m.infoVP.Height = listH
		return m, nil

	case prsLoadedMsg:
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

	case filesLoadedMsg:
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
		return m, nil

	case catchUpLoadedMsg:
		if msg.err != nil {
			m.statusMsg = "catch-up: " + msg.err.Error()
			return m, nil
		}
		// Only apply if we're still looking at the same file.
		if m.view != viewDiff || m.selectedFile != msg.path {
			return m, nil
		}
		// Find whether this file changed in the compare delta.
		var deltaFC *FileChange
		for _, f := range msg.files {
			if f.Path == msg.path {
				deltaFC = &f
				break
			}
		}
		if deltaFC == nil || deltaFC.Patch == "" {
			// File didn't change since last review — auto-caught-up.
			m.catchUpMode = false
			m.statusMsg = fmt.Sprintf("✓ %s unchanged since %s — caught up", m.selectedFile, shortSHA(m.catchUpOldHead))
			// Update the reviewed head to current so next visit is clean.
			m.brain.SetFileReviewed(m.selectedPR.Repo, m.selectedPR.Number, m.selectedFile, m.selectedPR.HeadSHA)
			return m, nil
		}
		// Switch to catch-up mode: show only the delta hunks.
		m.catchUpMode = true
		m.catchUpPatch = deltaFC.Patch
		m.currentHunks = parseHunks(deltaFC.Patch)
		m.currentMarks = m.brain.HunkMarks(m.selectedPR.Repo, m.selectedPR.Number, m.selectedFile)
		m.hunkIdx = firstUnmarked(m.currentHunks, m.currentMarks)
		m.statusMsg = fmt.Sprintf("catch-up: showing changes since %s  (d: full diff)", shortSHA(m.catchUpOldHead))
		m.redrawDiff()
		m.jumpToCurrentHunk()
		return m, nil

	case blobLoadedMsg:
		if msg.err != nil {
			m.statusMsg = "blob: " + msg.err.Error()
			return m, nil
		}
		if m.view == viewDiff {
			m.blobContent = msg.content
			// Don't redraw if we're in catch-up mode — keep showing the delta.
			if !m.catchUpMode {
				m.redrawDiff()
				m.jumpToCurrentHunk()
			}
		}
		return m, nil

	case prefetchDoneMsg:
		// Prune cached PRs that are no longer open (merged/closed since
		// last session). freshKeys was populated by prsLoadedMsg handlers.
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

	case tea.KeyMsg:
		if m.view == viewDiff && m.noting {
			switch msg.String() {
			case "esc":
				m.noting = false
				m.noteInput.Blur()
				m.restoreDiffSize()
				return m, nil
			case "ctrl+d":
				body := strings.TrimSpace(m.noteInput.Value())
				m.noting = false
				m.noteInput.Blur()
				m.restoreDiffSize()
				if body != "" {
					if err := m.brain.SaveNote(m.selectedPR.Repo, m.selectedPR.Number, m.selectedFile, m.noteLineNo, m.noteLineHash, body); err != nil {
						m.statusMsg = "save note: " + err.Error()
					} else {
						m.currentNotes = m.brain.NotesForFile(m.selectedPR.Repo, m.selectedPR.Number, m.selectedFile)
						m.redrawDiff()
					}
				}
				return m, nil
			}
			var cmd tea.Cmd
			m.noteInput, cmd = m.noteInput.Update(msg)
			return m, cmd
		}
		if m.view == viewDiff {
			switch msg.String() {
			case "ctrl+c", "q":
				return m, tea.Quit
			case "esc":
				m.view = viewFiles
				m.rebuildFileItems()
				m.rebuildPRItems()
				return m, nil
			case "n", "down", "tab":
				if len(m.currentHunks) > 0 && m.hunkIdx < len(m.currentHunks)-1 {
					m.hunkIdx++
					m.redrawDiff()
					m.jumpToCurrentHunk()
				}
				return m, nil
			case "p", "up", "shift+tab":
				if m.hunkIdx > 0 {
					m.hunkIdx--
					m.redrawDiff()
					m.jumpToCurrentHunk()
				}
				return m, nil
			case " ", "x":
				if m.hunkIdx >= 0 && m.hunkIdx < len(m.currentHunks) {
					h := m.currentHunks[m.hunkIdx]
					if m.currentMarks == nil {
						m.currentMarks = map[string]bool{}
					}
					if m.currentMarks[h.Hash] {
						delete(m.currentMarks, h.Hash)
					} else {
						m.currentMarks[h.Hash] = true
					}
					m.saveMarks()
					if m.hunkIdx < len(m.currentHunks)-1 {
						m.hunkIdx++
					}
					m.redrawDiff()
					m.jumpToCurrentHunk()
				}
				return m, nil
			case "h", "left":
				m.view = viewFiles
				m.rebuildFileItems()
				m.rebuildPRItems()
				return m, nil
			case "m":
				// Mark every current hunk as seen.
				if m.currentMarks == nil {
					m.currentMarks = map[string]bool{}
				}
				for _, h := range m.currentHunks {
					m.currentMarks[h.Hash] = true
				}
				m.saveMarks()
				m.redrawDiff()
				m.jumpToCurrentHunk()
				return m, nil
			case "enter", "right":
				if m.allHunksMarked() {
					m.view = viewFiles
					m.rebuildFileItems()
					m.rebuildPRItems()
				}
				return m, nil
			case "j":
				m.moveCursor(1)
				return m, nil
			case "k":
				m.moveCursor(-1)
				return m, nil
			case "u":
				m.currentMarks = map[string]bool{}
				m.saveMarks()
				m.redrawDiff()
				m.hunkIdx = 0
				m.jumpToCurrentHunk()
				m.statusMsg = "cleared marks on " + m.selectedFile
				return m, nil
			case "d":
				// Toggle between catch-up diff and full PR diff.
				if m.catchUpOldHead == "" {
					// No catch-up available — nothing to toggle.
					return m, nil
				}
				fc, ok := m.currentFile()
				if !ok {
					return m, nil
				}
				if m.catchUpMode {
					// Switch to full diff.
					m.catchUpMode = false
					m.currentHunks = parseHunks(fc.Patch)
					m.currentMarks = m.brain.HunkMarks(m.selectedPR.Repo, m.selectedPR.Number, fc.Path)
					m.hunkIdx = firstUnmarked(m.currentHunks, m.currentMarks)
					m.statusMsg = "full diff  (d: catch-up diff)"
				} else {
					// Switch back to catch-up diff.
					m.catchUpMode = true
					m.currentHunks = parseHunks(m.catchUpPatch)
					m.currentMarks = m.brain.HunkMarks(m.selectedPR.Repo, m.selectedPR.Number, fc.Path)
					m.hunkIdx = firstUnmarked(m.currentHunks, m.currentMarks)
					m.statusMsg = fmt.Sprintf("catch-up: changes since %s  (d: full diff)", shortSHA(m.catchUpOldHead))
				}
				m.redrawDiff()
				m.jumpToCurrentHunk()
				return m, nil
			case "c":
				lineNo := m.cursorFileLine()
				if lineNo == 0 {
					m.statusMsg = "cursor not on a file line"
					return m, nil
				}
				m.noting = true
				m.noteLineNo = lineNo
				m.noteLineHash = m.cursorLineHash(lineNo)
				m.noteInput.Reset()
				return m, m.noteInput.Focus()
			}
			// Fall through to let the viewport handle scrolling keys.
			var cmd tea.Cmd
			m.diff, cmd = m.diff.Update(msg)
			return m, cmd
		}

		// Tab switching in files view.
		if m.view == viewFiles && !listIsFiltering(m) {
			switch msg.String() {
			case "1":
				m.fileTab = tabFiles
				return m, nil
			case "2":
				m.fileTab = tabDescription
				m.rebuildInfoVP()
				return m, nil
			case "3":
				m.fileTab = tabNotes
				m.rebuildInfoVP()
				return m, nil
			}
		}

		// Non-diff views. vim-style h/l drill out/in as aliases for esc/enter.
		// Skip h/l while a filter is active so they're still typeable.
		switch msg.String() {
		case "ctrl+c", "q":
			if !listIsFiltering(m) {
				return m, tea.Quit
			}
		case "esc", "h", "left":
			if msg.String() == "h" && listIsFiltering(m) {
				break
			}
			if m.view == viewFiles {
				m.fileTab = tabFiles
				m.view = viewPRs
				return m, nil
			}
		case "enter", "l", "right":
			if msg.String() == "l" && listIsFiltering(m) {
				break
			}
			switch m.view {
			case viewPRs:
				if it, ok := m.prs.SelectedItem().(prItem); ok {
					pr := it.pr
					m.selectedPR = &pr
					m.view = viewFiles
					key := prKey(pr.Repo, pr.Number)
					if _, cached := m.prFiles[key]; cached {
						m.rebuildFileItems()
						m.files.Title = fmt.Sprintf("Files in %s#%d", pr.Repo, pr.Number)
						return m, nil
					}
					m.loadingFiles = true
					m.files.Title = fmt.Sprintf("Files in %s#%d (loading...)", pr.Repo, pr.Number)
					m.files.SetItems(nil)
					return m, loadFilesCmd(pr)
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
	}

	var cmd tea.Cmd
	switch m.view {
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

// skipSectionHeaders nudges the cursor past non-interactive sectionItem
// headers. Direction is inferred from whether the index went up or down.
func skipSectionHeaders(l *list.Model, prevIdx int) {
	items := l.Items()
	cur := l.Index()
	if cur >= len(items) {
		return
	}
	if _, ok := items[cur].(sectionItem); !ok {
		return
	}
	dir := 1
	if cur < prevIdx {
		dir = -1
	}
	next := cur + dir
	if next >= 0 && next < len(items) {
		l.Select(next)
	}
}

func listIsFiltering(m model) bool {
	switch m.view {
	case viewPRs:
		return m.prs.FilterState() == list.Filtering
	case viewFiles:
		return m.files.FilterState() == list.Filtering
	}
	return false
}

var appStyle = lipgloss.NewStyle().Padding(0, 1)

func (m model) View() string {
	var body string
	switch m.view {
	case viewPRs:
		body = m.prs.View()
	case viewFiles:
		body = m.tabBar()
		switch m.fileTab {
		case tabFiles:
			body += m.files.View()
		case tabDescription, tabNotes:
			body += m.infoVP.View()
		}
	case viewDiff:
		if m.noting {
			body = m.renderNotingView()
		} else {
			body = m.diff.View()
		}
	}
	footer := m.statusMsg
	if footer == "" {
		switch m.view {
		case viewDiff:
			if m.noting {
				footer = fmt.Sprintf("line %d  ctrl+d: save  esc: cancel", m.noteLineNo)
			} else {
				marked := 0
				for _, h := range m.currentHunks {
					if m.currentMarks[h.Hash] {
						marked++
					}
				}
				total := len(m.currentHunks)
				cur := m.hunkIdx + 1
				if total == 0 {
					cur = 0
				}
				modeHint := ""
				if m.catchUpOldHead != "" {
					if m.catchUpMode {
						modeHint = fmt.Sprintf("  [catch-up since %s]  d: full diff", shortSHA(m.catchUpOldHead))
					} else {
						modeHint = "  [full diff]  d: catch-up"
					}
				}
				footer = fmt.Sprintf("hunk %d/%d  marked %d/%d%s  ↑/↓: nav  j/k: cursor  space: toggle+next  m: mark all  c: note  u: unmark  h: back", cur, total, marked, total, modeHint)
			}
		case viewFiles:
			footer = "1: files  2: description  3: notes  l/enter: open  h/esc: back  q: quit"
		default:
			footer = "l/enter: open  h/esc: back  q: quit"
		}
	}
	return appStyle.Render(body) + "\n" + lipgloss.NewStyle().Faint(true).Render(footer)
}

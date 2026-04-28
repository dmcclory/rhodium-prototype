// Package diff renders the per-file diff view: the patch (or full-file)
// rendering of one file's changes, hunk-level mark/unmark, inline notes
// and GitHub comments, and the catch-up flow when the PR has moved since
// the file was last reviewed.
//
// The view emits typed messages — LeavingMsg, FileMarkedDoneMsg, StatusMsg,
// OpenEditorMsg, PublishNoteMsg, SaveNoteMsg — that the app's Update loop
// handles. Brain reads/writes go through the Brain interface so this
// package stays unaware of the concrete *brain.Brain type.
package diff

import (
	"fmt"
	"reflect"
	"strings"

	"rhodium/internal/brain"
	corediff "rhodium/internal/diff"
	"rhodium/internal/gh"
	"rhodium/internal/tui/keys"
	"rhodium/internal/tui/overlay"
	"rhodium/internal/tui/router"
	"rhodium/internal/tui/styles"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- Brain ---

// Brain is the narrow consumer-side interface this view needs from the
// brain package. *brain.Brain satisfies it implicitly.
type Brain interface {
	HunkMarks(repo string, num int, path string) map[string]bool
	NotesForFile(repo string, num int, path string) []brain.Note
	FileReviewedState(repo string, num int, path string) brain.FileReviewState
	IsScrutinized(repo string, num int) bool
	SetHunkMarks(repo string, num int, path string, marks map[string]bool) error
	SetFileReviewed(repo string, num int, path, head, base string) error
	SaveNote(repo string, num int, path string, lineNo int, hash, body string) error
}

// --- typed action messages emitted by the view ---

// LeavingMsg requests the app rebuild file & PR lists, then navigate back
// to the files view. Emitted by the back binding and by advance-when-marked.
type LeavingMsg struct{}

// FileMarkedDoneMsg requests the app mark Path complete in the active
// review session (and update session.review). Emitted after the diff view
// detects the file is fully reviewed (all marked / auto-caught-up).
type FileMarkedDoneMsg struct{ Path string }

// StatusMsg sets the footer status line. Emitted instead of poking app
// state directly so the diff package stays unaware of app internals.
type StatusMsg struct{ Text string }

// OpenEditorMsg requests the app launch the configured editor on
// (PR, File, Line). The app resolves the worktree, formats the status
// message, and runs launchEditor — the diff view's `o` binding has none
// of those handles itself.
type OpenEditorMsg struct {
	PR   gh.PR
	File string
	Line int
}

// PublishNoteMsg requests the app POST a single note as a GitHub inline
// comment. Echoes notePublishedMsg-equivalent fields back when done.
type PublishNoteMsg struct {
	NoteID int64
	PR     gh.PR
	Path   string
	Line   int
	Body   string
	Commit string
}

// LoadContributorsMsg requests the app fetch contributors for Repo and
// echo them back via SetContributors. Emitted on first @ when the cache
// is cold.
type LoadContributorsMsg struct{ Repo string }

// --- internal async messages (emitted by Cmds inside this package) ---

// CatchUpLoadedMsg lands when FetchCompare returns for a non-rebased
// catch-up. Routed by the app to the diff view's Update.
type CatchUpLoadedMsg struct {
	Path  string
	Files []gh.FileChange
	Err   error
}

// DiamondClassifiedMsg lands when a rebase diamond classification finishes.
type DiamondClassifiedMsg struct {
	Path    string
	Class   corediff.Class
	Diamond corediff.Diamond
	Result  *corediff.Result
	Patch   string
	Err     error
}

// BlobLoadedMsg lands when a full-file fetch returns. Switches the diff
// view from patch-only to full-file rendering.
type BlobLoadedMsg struct {
	Content string
	Err     error
}

// --- Model ---

// Model is the diff view's state. The app sets BackRoute when entering
// (always RouteFiles today) and AgentBindings so per-config agent
// actions appear alongside view bindings; everything else moves through
// Open / RefreshFromBrain / setters.
type Model struct {
	vp        viewport.Model
	noteInput textarea.Model

	pr   *gh.PR
	file string

	hunks      []corediff.Hunk
	marks      map[string]bool
	notes      []brain.Note
	ghInline   []gh.Comment
	hunkLines  []int
	lineMap    []int
	hunkIdx    int
	cursorLine int
	diffLines  []string
	blob       string
	fullPatch  string // original PR-level patch from Open — used to restore full diff after toggling out of catch-up

	// Catch-up diff state.
	catchUpMode    bool
	catchUpOldHead string
	catchUpOldBase string
	catchUpClass   corediff.Class
	catchUpPatch   string

	// Slow-path segmentation state.
	segments       []corediff.Segment
	segmentViewIdx int
	segmented      bool

	// Note input state.
	noting       bool
	noteLineNo   int
	noteLineHash string

	// @-mention picker overlay (only meaningful while noting).
	mention      mentionPicker
	contributors map[string][]gh.Contributor // by repo

	// External: width/height for picker centering.
	width, height int

	BackRoute     router.Route
	AgentBindings []keys.Binding
}

func New() Model {
	ti := textarea.New()
	ti.Placeholder = "Write a note... (ctrl+d to save, esc to cancel)"
	ti.SetHeight(3)
	ti.ShowLineNumbers = false
	return Model{
		vp:           viewport.New(0, 0),
		noteInput:    ti,
		mention:      newMentionPicker(),
		contributors: map[string][]gh.Contributor{},
		BackRoute:    router.RouteFiles,
	}
}

func (m *Model) Resize(w, h int) {
	m.vp.Width = w
	m.vp.Height = h
	m.width = w
	m.height = h
}

// SetLayoutSize lets the app pass full terminal dimensions used to center
// the @-mention picker overlay (which sits over the diff body, not just
// inside the viewport's frame).
func (m *Model) SetLayoutSize(w, h int) {
	m.width = w
	m.height = h
}

func (m *Model) View() string {
	if m.noting {
		body := m.renderNotingView()
		if m.mention.open {
			box := m.mention.Render()
			boxW := lipgloss.Width(box)
			boxH := lipgloss.Height(box)
			x := (m.width - boxW) / 2
			if x < 0 {
				x = 0
			}
			y := (m.height - boxH) / 2
			if y < 0 {
				y = 0
			}
			return overlay.Render(body, box, x, y)
		}
		return body
	}
	return m.vp.View()
}

func (m *Model) Footer() string {
	if m.noting {
		if m.mention.open {
			return "@-mention  ↑/↓: nav  type to filter  enter: insert  esc: close"
		}
		return fmt.Sprintf("line %d  ctrl+d: save  type @ to mention  esc: cancel", m.noteLineNo)
	}
	marked := 0
	total := 0
	cur := 0
	for i, h := range m.hunks {
		if !h.IsMarkable() {
			continue
		}
		total++
		if m.marks[h.Hash] {
			marked++
		}
		if i <= m.hunkIdx {
			cur = total
		}
	}
	if total == 0 {
		cur = 0
	}
	modeHint := ""
	if m.catchUpOldHead != "" {
		if m.catchUpMode {
			if m.segmented {
				maxV := corediff.MaxSegmentViews(m.segments)
				cycleHint := ""
				if maxV > 1 {
					cycleHint = fmt.Sprintf("  v: cycle view (%d/%d)", (m.segmentViewIdx%maxV)+1, maxV)
				}
				modeHint = fmt.Sprintf("  [catch-up %s: %d segments since %s]  d: full diff%s", m.catchUpClass, len(m.segments), shortSHA(m.catchUpOldHead), cycleHint)
			} else {
				modeHint = fmt.Sprintf("  [catch-up %s since %s]  d: full diff", m.catchUpClass, shortSHA(m.catchUpOldHead))
			}
		} else {
			modeHint = "  [full diff]  d: catch-up"
		}
	}
	return fmt.Sprintf("hunk %d/%d  marked %d/%d%s  ↑/↓: nav  j/k: cursor  space: toggle+next  m: mark all  c: note  P: publish note  o: open  u: unmark  h: back", cur, total, marked, total, modeHint)
}

// Update routes a message: keys to mode-specific handlers, async results
// to handlers, non-key fallback to the viewport. brain is supplied per
// call so the package stays decoupled from the concrete brain type.
func (m *Model) Update(msg tea.Msg, b Brain, globals []keys.Binding) tea.Cmd {
	switch msg := msg.(type) {
	case CatchUpLoadedMsg:
		return m.onCatchUpLoaded(b, msg)
	case DiamondClassifiedMsg:
		return m.onDiamondClassified(b, msg)
	case BlobLoadedMsg:
		return m.onBlobLoaded(msg)
	case tea.KeyMsg:
		if m.noting {
			return m.updateNotingKeys(b, msg)
		}
		return m.updateKeys(b, msg, globals)
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return cmd
}

// Open loads a file into the diff view: parse hunks, seed marks from the
// brain, show patch view immediately, then kick off blob fetch for full
// file. If the PR head has moved since this file was last reviewed we
// enter catch-up mode: fetch only the delta and show that instead of the
// full PR diff.
func (m *Model) Open(b Brain, pr *gh.PR, fc gh.FileChange, ghInline []gh.Comment) tea.Cmd {
	m.pr = pr
	m.file = fc.Path
	m.blob = ""
	m.catchUpMode = false
	m.catchUpOldHead = ""
	m.catchUpOldBase = ""
	m.catchUpClass = corediff.ClassB1B2F1F2
	m.catchUpPatch = ""
	m.segments = nil
	m.segmentViewIdx = 0
	m.segmented = false

	revState := b.FileReviewedState(pr.Repo, pr.Number, fc.Path)
	scrutinized := b.IsScrutinized(pr.Repo, pr.Number)
	needsCatchUp := !scrutinized && revState.HeadSHA != "" && (revState.HeadSHA != pr.HeadSHA || revState.BaseSHA != pr.BaseSHA)

	m.fullPatch = fc.Patch
	m.hunks = corediff.ParseHunks(fc.Patch)
	m.marks = b.HunkMarks(pr.Repo, pr.Number, fc.Path)
	m.notes = b.NotesForFile(pr.Repo, pr.Number, fc.Path)
	m.ghInline = ghInline
	m.hunkIdx = firstUnmarked(m.hunks, m.marks)
	m.redraw()
	m.jumpToHunk()

	var cmds []tea.Cmd

	if needsCatchUp {
		m.catchUpOldHead = revState.HeadSHA
		m.catchUpOldBase = revState.BaseSHA
		repo := pr.Repo
		oldHead := revState.HeadSHA
		oldBase := revState.BaseSHA
		newHead := pr.HeadSHA
		newBase := pr.BaseSHA
		path := fc.Path
		rebased := oldBase != newBase && oldBase != ""

		if rebased {
			cmds = append(cmds, statusCmd(fmt.Sprintf("classifying diamond (rebase %s→%s)", shortSHA(oldBase), shortSHA(newBase))))
			cmds = append(cmds, func() tea.Msg {
				b1, _ := gh.FetchFileAtRef(repo, path, oldBase)
				f1, _ := gh.FetchFileAtRef(repo, path, oldHead)
				b2, _ := gh.FetchFileAtRef(repo, path, newBase)
				f2, _ := gh.FetchFileAtRef(repo, path, newHead)
				d := corediff.Diamond{B1: b1, F1: f1, B2: b2, F2: f2}
				class := corediff.Classify(d, nil)
				result := corediff.ComputeSlow(d)
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
				return DiamondClassifiedMsg{Path: path, Class: class, Diamond: d, Result: result, Patch: patch}
			})
		} else {
			cmds = append(cmds, statusCmd(fmt.Sprintf("loading catch-up diff %s..%s", shortSHA(oldHead), shortSHA(newHead))))
			cmds = append(cmds, func() tea.Msg {
				files, err := gh.FetchCompare(repo, oldHead, newHead)
				return CatchUpLoadedMsg{Path: path, Files: files, Err: err}
			})
		}
	}

	if fc.Blob != "" {
		repo := pr.Repo
		sha := fc.Blob
		cmds = append(cmds, func() tea.Msg {
			content, err := gh.FetchBlob(repo, sha)
			return BlobLoadedMsg{Content: content, Err: err}
		})
	}

	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// RefreshFromBrain re-reads marks and notes for the currently-open file.
// Returns true if anything changed (so the caller can decide whether to
// note the change in the status line). Always redraws on change.
func (m *Model) RefreshFromBrain(b Brain) bool {
	if m.pr == nil || m.file == "" {
		return false
	}
	changed := false
	newMarks := b.HunkMarks(m.pr.Repo, m.pr.Number, m.file)
	if !reflect.DeepEqual(newMarks, m.marks) {
		m.marks = newMarks
		changed = true
	}
	newNotes := b.NotesForFile(m.pr.Repo, m.pr.Number, m.file)
	if !reflect.DeepEqual(newNotes, m.notes) {
		m.notes = newNotes
		changed = true
	}
	if changed {
		m.redraw()
	}
	return changed
}

// RefreshNotes re-reads only notes from the brain and redraws. Used after
// async note publishes / agent saves where marks are unchanged.
func (m *Model) RefreshNotes(b Brain) {
	if m.pr == nil || m.file == "" {
		return
	}
	m.notes = b.NotesForFile(m.pr.Repo, m.pr.Number, m.file)
	m.redraw()
}

// RefreshGHInline replaces the inline comment slice and redraws. Called
// when the PR-level comments fetch returns while the diff view is open.
func (m *Model) RefreshGHInline(comments []gh.Comment) {
	if m.file == "" {
		m.ghInline = nil
		return
	}
	m.ghInline = filterInlineForPath(comments, m.file)
	m.redraw()
}

// SetContributors caches contributors for repo so the @-mention picker
// has them next time the user opens it. Also refreshes the picker if it's
// open on this repo.
func (m *Model) SetContributors(repo string, c []gh.Contributor) {
	m.contributors[repo] = c
	if !m.mention.open || m.pr == nil || m.pr.Repo != repo {
		return
	}
	m.mention.loading = false
	m.mention.list.SetItems(filterContributors(c, m.mention.query))
	m.sizeMentionPicker()
}

// PR exposes the currently-open PR, or nil. App uses this to know whether
// the diff view has anything loaded (e.g. before issuing a refresh).
func (m *Model) PR() *gh.PR { return m.pr }

// File returns the currently-open file path, or empty string if none.
func (m *Model) File() string { return m.file }

// --- async message handlers ---

func (m *Model) onCatchUpLoaded(b Brain, msg CatchUpLoadedMsg) tea.Cmd {
	if msg.Err != nil {
		return statusCmd("catch-up: " + msg.Err.Error())
	}
	if m.file != msg.Path {
		return nil
	}
	var deltaFC *gh.FileChange
	for _, f := range msg.Files {
		if f.Path == msg.Path {
			deltaFC = &f
			break
		}
	}
	if deltaFC == nil || deltaFC.Patch == "" {
		m.catchUpMode = false
		m.catchUpClass = corediff.ClassB1B2__F1F2
		b.SetFileReviewed(m.pr.Repo, m.pr.Number, m.file, m.pr.HeadSHA, m.pr.BaseSHA)
		path := m.file
		return tea.Batch(
			statusCmd(fmt.Sprintf("✓ %s: %s (auto-caught-up)", m.file, corediff.ClassB1B2__F1F2)),
			func() tea.Msg { return FileMarkedDoneMsg{Path: path} },
		)
	}
	m.catchUpMode = true
	m.catchUpClass = corediff.ClassB1B2
	m.catchUpPatch = deltaFC.Patch
	m.hunks = corediff.ParseHunks(deltaFC.Patch)
	m.marks = b.HunkMarks(m.pr.Repo, m.pr.Number, m.file)
	m.hunkIdx = firstUnmarked(m.hunks, m.marks)
	m.redraw()
	m.jumpToHunk()
	return statusCmd(fmt.Sprintf("catch-up [%s]: f1→f2 since %s  (d: full diff)", corediff.ClassB1B2, shortSHA(m.catchUpOldHead)))
}

func (m *Model) onDiamondClassified(b Brain, msg DiamondClassifiedMsg) tea.Cmd {
	if msg.Err != nil {
		return statusCmd("classify: " + msg.Err.Error())
	}
	if m.file != msg.Path {
		return nil
	}
	m.catchUpClass = msg.Class

	if msg.Class.Hidden() {
		m.catchUpMode = false
		m.segmented = false
		label := msg.Class.String()
		if msg.Class.IsForget() {
			label = "FORGET — base absorbed feature"
		}
		b.SetFileReviewed(m.pr.Repo, m.pr.Number, m.file, m.pr.HeadSHA, m.pr.BaseSHA)
		path := m.file
		return tea.Batch(
			statusCmd(fmt.Sprintf("✓ %s: %s (auto-caught-up)", m.file, label)),
			func() tea.Msg { return FileMarkedDoneMsg{Path: path} },
		)
	}

	m.catchUpMode = true
	if msg.Class.ShownAsDiff2() && msg.Patch != "" {
		// Single-segment case: reuse git's whole-file unified patch.
		m.catchUpPatch = msg.Patch
		m.hunks = corediff.ParseHunks(msg.Patch)
		m.segments = nil
		m.segmented = false
	} else if msg.Result != nil && len(msg.Result.Segments) > 0 {
		// Complex class: per-segment decomposition.
		m.segments = msg.Result.Segments
		m.hunks = corediff.SegmentHunks(m.segments, m.segmentViewIdx)
		m.catchUpPatch = ""
		m.segmented = true
	}
	m.marks = b.HunkMarks(m.pr.Repo, m.pr.Number, m.file)
	m.hunkIdx = firstUnmarked(m.hunks, m.marks)

	var status string
	if m.segmented {
		status = fmt.Sprintf("catch-up [%s]: %d segments  (d: full diff)", msg.Class, len(m.segments))
	} else {
		views := msg.Class.Views()
		viewLabel := ""
		if len(views) > 0 {
			viewLabel = fmt.Sprintf("%s→%s", views[0].From, views[0].To)
		}
		status = fmt.Sprintf("catch-up [%s]: %s  (d: full diff)", msg.Class, viewLabel)
	}
	m.redraw()
	m.jumpToHunk()
	return statusCmd(status)
}

func (m *Model) onBlobLoaded(msg BlobLoadedMsg) tea.Cmd {
	if msg.Err != nil {
		return statusCmd("blob: " + msg.Err.Error())
	}
	m.blob = msg.Content
	if !m.catchUpMode {
		m.redraw()
		m.jumpToHunk()
	}
	return nil
}

// --- rendering ---

func (m *Model) redraw() {
	if len(m.hunks) == 0 {
		m.vp.SetContent("(no hunks — nothing to review)")
		m.hunkLines = nil
		return
	}
	var body string
	var lines []int
	var lmap []int
	if m.blob != "" && !m.segmented {
		body, lines, lmap = renderFullFile(m.blob, m.hunks, m.marks, m.hunkIdx, m.notes, m.ghInline, m.cursorLine)
	} else {
		body, lines, lmap = renderHunks(m.hunks, m.marks, m.hunkIdx, m.notes, m.ghInline, m.cursorLine)
	}
	m.vp.SetContent(body)
	m.diffLines = strings.Split(body, "\n")
	m.hunkLines = lines
	m.lineMap = lmap
}

func (m *Model) jumpToHunk() {
	if m.hunkIdx < 0 || m.hunkIdx >= len(m.hunkLines) {
		return
	}
	target := m.hunkLines[m.hunkIdx]
	m.cursorLine = target
	m.vp.SetYOffset(target)
}

func (m *Model) allMarked() bool {
	any := false
	for _, h := range m.hunks {
		if !h.IsMarkable() {
			continue
		}
		any = true
		if !m.marks[h.Hash] {
			return false
		}
	}
	return any
}

// stepHunk moves m.hunkIdx by ±1 across real hunks, skipping synthetic
// segment headers so n/p/tab navigation only lands on markable units.
func (m *Model) stepHunk(delta int) {
	if len(m.hunks) == 0 || delta == 0 {
		return
	}
	step := 1
	if delta < 0 {
		step = -1
	}
	next := m.hunkIdx + step
	for next >= 0 && next < len(m.hunks) && !m.hunks[next].IsMarkable() {
		next += step
	}
	if next < 0 || next >= len(m.hunks) {
		return
	}
	m.hunkIdx = next
}

// currentSegmentView returns the View the hunk at hunkIdx is being
// rendered under, or ok=false when not in segmented mode or the cursor is
// off the hunk list. Used to decide whether a note can be keyed to an F2
// line.
func (m *Model) currentSegmentView() (corediff.View, bool) {
	if !m.segmented || m.hunkIdx < 0 || m.hunkIdx >= len(m.hunks) {
		return corediff.View{}, false
	}
	segIdx := -1
	for i := 0; i <= m.hunkIdx; i++ {
		if !m.hunks[i].IsMarkable() {
			segIdx++
		}
	}
	if segIdx < 0 || segIdx >= len(m.segments) {
		return corediff.View{}, false
	}
	views := m.segments[segIdx].Class.Views()
	if len(views) == 0 {
		return corediff.View{}, false
	}
	return views[m.segmentViewIdx%len(views)], true
}

func (m *Model) moveCursor(delta int) {
	next := m.cursorLine + delta
	if next < 0 {
		next = 0
	}
	if max := len(m.lineMap) - 1; max >= 0 && next > max {
		next = max
	}
	m.cursorLine = next
	m.redraw()
	if m.cursorLine < m.vp.YOffset {
		m.vp.SetYOffset(m.cursorLine)
	} else if m.cursorLine >= m.vp.YOffset+m.vp.Height {
		m.vp.SetYOffset(m.cursorLine - m.vp.Height + 1)
	}
}

func (m *Model) cursorFileLine() int {
	if m.cursorLine < 0 || m.cursorLine >= len(m.lineMap) {
		return 0
	}
	return m.lineMap[m.cursorLine]
}

func (m *Model) cursorLineHash(lineNo int) string {
	if m.blob == "" {
		return ""
	}
	lines := strings.Split(m.blob, "\n")
	idx := lineNo - 1
	if idx < 0 || idx >= len(lines) {
		return ""
	}
	return hashLine(lines[idx])
}

// --- persistence ---

// saveMarks writes marks to the brain and stamps reviewed-at-head; if all
// markable hunks are now marked, returns FileMarkedDoneMsg so the app can
// update the active session. Status messages on error are emitted as
// StatusMsg via the returned tea.Cmd.
func (m *Model) saveMarks(b Brain) tea.Cmd {
	if m.pr == nil || m.file == "" {
		return nil
	}
	if err := b.SetHunkMarks(m.pr.Repo, m.pr.Number, m.file, m.marks); err != nil {
		return statusCmd("save error: " + err.Error())
	}
	if m.pr.HeadSHA != "" {
		b.SetFileReviewed(m.pr.Repo, m.pr.Number, m.file, m.pr.HeadSHA, m.pr.BaseSHA)
	}
	if m.allMarked() {
		path := m.file
		return func() tea.Msg { return FileMarkedDoneMsg{Path: path} }
	}
	return nil
}

// --- helpers ---

func filterInlineForPath(all []gh.Comment, path string) []gh.Comment {
	var out []gh.Comment
	for _, c := range all {
		if c.Type == "inline" && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

func firstUnmarked(hunks []corediff.Hunk, marks map[string]bool) int {
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

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// hashLine wraps a single string as a "+"-prefixed hunk body and runs it
// through the hunk hasher. Used for note line anchoring.
func hashLine(s string) string {
	return corediff.HashHunkBody([]string{"+" + s})
}

// hunkLines parses a hunk header to extract the (oldLine, newLine) start.
// Used by openInEditor to compute the open-at-line target.
func hunkLines(header string) (oldLine, newLine int) {
	// Hunk headers look like "@@ -23,4 +25,7 @@ optional context"
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, "@@") {
		return 0, 0
	}
	parts := strings.SplitN(header, "@@", 3)
	if len(parts) < 2 {
		return 0, 0
	}
	hdr := strings.TrimSpace(parts[1])
	for _, tok := range strings.Fields(hdr) {
		if strings.HasPrefix(tok, "-") {
			oldLine = parseLeadingInt(tok[1:])
		} else if strings.HasPrefix(tok, "+") {
			newLine = parseLeadingInt(tok[1:])
		}
	}
	return
}

func parseLeadingInt(s string) int {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n := 0
	for i := 0; i < end; i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

func statusCmd(text string) tea.Cmd {
	return func() tea.Msg { return StatusMsg{Text: text} }
}

// restoreSize resets the viewport to the full content area. Called after
// the noting textarea closes so the diff body fills the screen again.
func (m *Model) restoreSize() {
	h, padV := styles.App.GetFrameSize()
	m.vp.Width = m.width - h
	m.vp.Height = m.height - padV - 1
}

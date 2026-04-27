package rhodium

import (
	"fmt"
	"reflect"
	"rhodium/internal/brain"
	"rhodium/internal/gh"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// program is a package-level handle to the running tea program, set in
// main before Run(). Goroutines spawned from tea.Cmd's use it to push
// messages back onto the update loop.
var program *tea.Program

// pollInterval controls how often the TUI re-reads brain state to pick
// up external mutations (e.g. an nvim instance in another tmux pane
// marking a hunk). SQLite reads are cheap; 500ms feels snappy without
// being wasteful.
const pollInterval = 500 * time.Millisecond

// app is the top-level tea.Model. It owns data shared across views
// (brain, cfg, the PR cache, currently-selected PR/file) plus each
// sub-model's UI state in a named field. The active view is addressed
// through a.layout.activeView; every view method takes *app so transitions
// and shared lookups have one home.
//
// This split is deliberately shaped for the eventual daemon move:
// everything that would live on the daemon side (brain reads/writes,
// the PR cache, auto-advance state) hangs off app; everything that
// would remain client-local (list cursors, viewport offsets, note
// input) hangs off the per-view structs.
type app struct {
	cfg   *Config
	brain *brain.Brain

	// layout is terminal viewport state plus which view has focus.
	layout layout

	todo     todoView
	prs      prsView
	files    filesView
	diff     diffView
	comments commentsView
	help     helpOverlay

	// review modal lives at app level so any list view can open it.
	review reviewModal
	merge  mergeModal

	// cache holds GitHub-fetched data shared across views.
	cache cache

	// session is per-run navigation state — selected PR/file, active
	// review session, where to return when the user backs out.
	session session

	statusMsg string

	// pollGen increments every time a PR is opened; the polling tick
	// carries the generation it was scheduled under, so older loops stop
	// naturally when a newer PR is selected.
	pollGen int
}

func newApp(cfg *Config, b *brain.Brain) *app {
	a := &app{
		cfg:      cfg,
		brain:    b,
		layout:   layout{activeView: viewTodo},
		todo:     newTodoView(),
		prs:      newPRsView(),
		files:    newFilesView(),
		diff:     newDiffView(),
		comments: newCommentsView(),
		review:   newReviewModal(),
		merge:    newMergeModal(),
		cache:    newCache(),
		session:  newSession(),
	}

	cached := b.CachedPRs()
	if len(cached) > 0 {
		a.cache.allPRs = cached
		a.prs.rebuild(a)
		a.prs.list.Title = fmt.Sprintf("PRs (%d, refreshing…)", len(cached))
	} else {
		a.prs.list.Title = "PRs (loading...)"
	}

	return a
}

func (a *app) Init() tea.Cmd {
	cmds := make([]tea.Cmd, len(a.cfg.Repos))
	for i, repo := range a.cfg.Repos {
		cmds[i] = loadRepoPRsCmd(repo)
	}
	return tea.Batch(cmds...)
}

// Update is the one entry point tea calls. Async messages are dispatched
// by type; keypresses are routed to the active sub-model.
func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {

	case tea.WindowSizeMsg:
		a.layout.setSize(m.Width, m.Height)
		a.relayout()
		return a, nil

	case prsLoadedMsg:
		return a, a.onPRsLoaded(m)
	case filesLoadedMsg:
		return a, a.onFilesLoaded(m)
	case autoAdvanceMsg:
		return a, a.onAutoAdvance(m)
	case catchUpLoadedMsg:
		return a, a.diff.onCatchUpLoaded(a, m)
	case diamondClassifiedMsg:
		return a, a.diff.onDiamondClassified(a, m)
	case blobLoadedMsg:
		return a, a.diff.onBlobLoaded(a, m)
	case prefetchDoneMsg:
		return a, a.onPrefetchDone()
	case editorDoneMsg:
		return a, a.onEditorDone(m)
	case actionDoneMsg:
		return a, a.onActionDone(m)
	case inlineNotesReadyMsg:
		return a, a.onInlineNotesReady(m)
	case notePublishedMsg:
		return a, a.onNotePublished(m)
	case reviewSubmittedMsg:
		return a, a.onReviewSubmitted(m)
	case mergeSubmittedMsg:
		return a, a.onMergeSubmitted(m)
	case contributorsLoadedMsg:
		return a, a.onContributorsLoaded(m)
	case commentsLoadedMsg:
		return a, a.onCommentsLoaded(m)
	case pollTickMsg:
		return a, a.onPollTick(m)

	case tea.KeyMsg:
		if a.help.open {
			switch m.String() {
			case "?", "esc", "q", "ctrl+c":
				a.help.open = false
			}
			return a, nil
		}
		if a.review.open {
			return a, a.updateReviewKeys(m)
		}
		if a.merge.open {
			return a, a.updateMergeKeys(m)
		}
		return a, a.routeKey(m)
	}

	// Non-key messages the type switch didn't claim — send to the active
	// sub-model so bubbles internals (mouse events, etc.) still work.
	return a, a.routeToActive(msg)
}

func (a *app) View() string {
	var body string
	switch a.layout.activeView {
	case viewTodo:
		body = a.todo.View(a)
	case viewPRs:
		body = a.prs.View(a)
	case viewFiles:
		body = a.files.View(a)
	case viewDiff:
		body = a.diff.View(a)
	case viewComments:
		body = a.comments.View(a)
	}
	rendered := appStyle.Render(body) + "\n" + lipgloss.NewStyle().Faint(true).Render(a.footer())

	if a.review.open {
		rendered = centerOverlay(rendered, a.renderReviewModal(), a.layout.width, a.layout.height)
	}
	if a.merge.open {
		rendered = centerOverlay(rendered, a.renderMergeModal(), a.layout.width, a.layout.height)
	}
	if a.help.open {
		rendered = centerOverlay(rendered, a.help.Render(a), a.layout.width, a.layout.height)
	}
	return rendered
}

// centerOverlay paints fg centered over bg, clamped to width/height. Used
// for app-level modals (help, review).
func centerOverlay(bg, fg string, width, height int) string {
	boxW := lipgloss.Width(fg)
	boxH := lipgloss.Height(fg)
	x := (width - boxW) / 2
	if x < 0 {
		x = 0
	}
	y := (height - boxH) / 2
	if y < 0 {
		y = 0
	}
	return overlay(bg, fg, x, y)
}

// --- routing ---

func (a *app) routeKey(key tea.KeyMsg) tea.Cmd {
	switch a.layout.activeView {
	case viewTodo:
		return a.todo.Update(a, key)
	case viewPRs:
		return a.prs.Update(a, key)
	case viewFiles:
		return a.files.Update(a, key)
	case viewDiff:
		return a.diff.Update(a, key)
	case viewComments:
		return a.comments.Update(a, key)
	}
	return nil
}

func (a *app) routeToActive(msg tea.Msg) tea.Cmd {
	switch a.layout.activeView {
	case viewTodo:
		return a.todo.Update(a, msg)
	case viewPRs:
		return a.prs.Update(a, msg)
	case viewFiles:
		return a.files.Update(a, msg)
	case viewDiff:
		return a.diff.Update(a, msg)
	case viewComments:
		return a.comments.Update(a, msg)
	}
	return nil
}

func (a *app) relayout() {
	h, padV := appStyle.GetFrameSize()
	listW, listH := a.layout.width-h, a.layout.height-padV-1
	a.todo.Resize(listW, listH)
	a.prs.Resize(listW, listH)
	a.files.Resize(listW, listH)
	a.diff.Resize(listW, listH)
	a.comments.Resize(listW, listH)
}

// --- transitions ---

// openPR transitions todo/prs → files, loading the file list if it isn't
// already cached. Bumps pollGen so any in-flight tick from a previous PR
// stops silently.
func (a *app) openPR(pr gh.PR) tea.Cmd {
	a.session.listOrigin = a.layout.activeView
	a.session.selectedPR = &pr
	a.session.review = a.brain.ActiveSession(pr.Repo, pr.Number)
	a.layout.focus(viewFiles)
	a.files.rebuildDescVP(a)
	a.pollGen++
	key := brain.PRKey(pr.Repo, pr.Number)
	cmds := []tea.Cmd{pollTickCmd(a.pollGen)}
	if _, cached := a.cache.prComments[key]; !cached {
		cmds = append(cmds, loadCommentsCmd(pr))
	}
	if _, cached := a.cache.prFiles[key]; cached {
		a.files.rebuild(a)
		a.files.list.Title = fmt.Sprintf("Files in %s#%d", pr.Repo, pr.Number)
		return tea.Batch(cmds...)
	}
	a.files.loadingFiles = true
	a.files.list.Title = fmt.Sprintf("Files in %s#%d (loading...)", pr.Repo, pr.Number)
	a.files.list.SetItems(nil)
	cmds = append(cmds, loadFilesCmd(pr))
	return tea.Batch(cmds...)
}

// openFile transitions files → diff, delegating to diffView.open for the
// actual state reset + fetch commands.
func (a *app) openFile(fc gh.FileChange) tea.Cmd {
	return a.diff.open(a, fc)
}

// openComments transitions to the PR-level comments view. The comments
// fetch was kicked off when the PR was opened, so usually they're already
// cached; if not, the view shows a "loading" placeholder until
// commentsLoadedMsg lands.
func (a *app) openComments(returnTo view) tea.Cmd {
	if a.session.selectedPR == nil {
		return nil
	}
	a.comments.returnTo = returnTo
	a.layout.focus(viewComments)
	a.comments.rebuild(a)
	return nil
}

// currentFile returns the gh.FileChange for a.session.selectedFile from the PR's
// cached file list, if present.
func (a *app) currentFile() (gh.FileChange, bool) {
	if a.session.selectedPR == nil {
		return gh.FileChange{}, false
	}
	for _, f := range a.cache.prFiles[brain.PRKey(a.session.selectedPR.Repo, a.session.selectedPR.Number)] {
		if f.Path == a.session.selectedFile {
			return f, true
		}
	}
	return gh.FileChange{}, false
}

// prHasOutstandingWork reports whether pr still needs the reviewer's
// attention: unseen, in-progress, catch-up pending, or notes attached.
// Single source of truth for "is this PR on the todo list" — used by
// the todo count and buildTodoItem's nil check.
func (a *app) prHasOutstandingWork(pr gh.PR) bool {
	if a.brain.NoteCountForPR(pr.Repo, pr.Number) > 0 {
		return true
	}
	if a.brain.ActiveSession(pr.Repo, pr.Number) != nil {
		return true
	}
	touched := a.brain.HasAnyMarks(pr.Repo, pr.Number) ||
		len(a.brain.AllFileReviewedStates(pr.Repo, pr.Number)) > 0
	if !touched {
		return true // unseen
	}
	files, filesLoaded := a.cache.prFiles[brain.PRKey(pr.Repo, pr.Number)]
	if !filesLoaded {
		return true // touched but files not yet loaded — assume in-progress
	}
	return a.brain.UnseenCount(pr.Repo, pr.Number, files) > 0
}

// outstandingPRCount is the number of PRs across all repos with work
// left for the reviewer — drives the todo-view title.
func (a *app) outstandingPRCount() int {
	n := 0
	for _, pr := range a.cache.allPRs {
		if a.prHasOutstandingWork(pr) {
			n++
		}
	}
	return n
}

// markSessionFileDone marks the given path done within the active review
// session, if any, and auto-completes the session when it was the last
// outstanding file. Called from the diff view after the reviewer has
// finished a file (via full-diff catch-up resolution, auto-advance, or
// all-hunks-marked).
func (a *app) markSessionFileDone(path string) {
	if a.session.review == nil || a.session.selectedPR == nil {
		return
	}
	a.brain.SetSessionFileDone(a.session.review.ID, path, true)
	a.session.review = a.brain.ActiveSession(a.session.selectedPR.Repo, a.session.selectedPR.Number)
}

// --- footer composition ---

func (a *app) footer() string {
	if a.statusMsg != "" {
		return a.statusMsg
	}
	if a.review.open {
		return "review modal — tab: cycle event   ctrl+s: submit   esc: cancel"
	}
	if a.merge.open {
		return "merge modal — tab: cycle method   ctrl+s: merge   esc: cancel"
	}
	switch a.layout.activeView {
	case viewTodo:
		return a.todo.Footer(a)
	case viewPRs:
		return a.prs.Footer(a)
	case viewFiles:
		return a.files.Footer(a)
	case viewDiff:
		return a.diff.Footer(a)
	case viewComments:
		return a.comments.Footer(a)
	}
	return ""
}

// --- async message handlers ---

func (a *app) onPRsLoaded(msg prsLoadedMsg) tea.Cmd {
	if msg.err != nil {
		a.statusMsg = "error: " + msg.err.Error()
		return nil
	}
	for _, p := range msg.prs {
		a.cache.markFresh(brain.PRKey(p.Repo, p.Number))
	}
	added := mergePRs(a, msg.prs)
	a.prs.rebuild(a)
	a.prs.list.Title = fmt.Sprintf("PRs (%d, loading files…)", len(a.cache.allPRs))
	go a.brain.SetPRCache(a.cache.allPRs)
	return prefetchAllCmd(added)
}

func (a *app) onFilesLoaded(msg filesLoadedMsg) tea.Cmd {
	a.files.loadingFiles = false
	if msg.err != nil {
		a.statusMsg = "error: " + msg.err.Error()
		return nil
	}
	key := brain.PRKey(msg.pr.Repo, msg.pr.Number)
	a.cache.prFiles[key] = msg.files
	a.prs.rebuild(a)
	if a.session.selectedPR != nil && brain.PRKey(a.session.selectedPR.Repo, a.session.selectedPR.Number) == key {
		a.files.rebuild(a)
		a.files.list.Title = fmt.Sprintf("Files in %s#%d", msg.pr.Repo, msg.pr.Number)
	}
	if a.brain.IsScrutinized(msg.pr.Repo, msg.pr.Number) {
		return nil
	}
	return autoAdvanceCmd(a.brain, msg.pr, msg.files)
}

func (a *app) onAutoAdvance(msg autoAdvanceMsg) tea.Cmd {
	if len(msg.advancedFiles) > 0 {
		a.prs.rebuild(a)
		if a.session.selectedPR != nil && brain.PRKey(a.session.selectedPR.Repo, a.session.selectedPR.Number) == msg.prKey {
			a.files.rebuild(a)
			a.session.review = a.brain.ActiveSession(a.session.selectedPR.Repo, a.session.selectedPR.Number)
		}
		a.statusMsg = fmt.Sprintf("✓ auto-caught-up %d files", len(msg.advancedFiles))
	}
	return nil
}

func (a *app) onPrefetchDone() tea.Cmd {
	if len(a.cache.freshKeys) > 0 {
		a.cache.pruneStale()
		a.prs.rebuild(a)
		go a.brain.SetPRCache(a.cache.allPRs)
	}
	a.prs.list.Title = fmt.Sprintf("PRs (%d)", len(a.cache.allPRs))
	return nil
}

// onEditorDone runs after an external editor exits. For the tea.ExecProcess
// path this fires once the user quits nvim; for the tmux path it fires
// immediately after spawning the pane/window. In both cases we refresh
// the current PR's marks/notes so any changes made in nvim show up.
func (a *app) onEditorDone(msg editorDoneMsg) tea.Cmd {
	if msg.err != nil {
		a.statusMsg = "editor: " + msg.err.Error()
		return nil
	}
	if a.session.selectedPR != nil {
		pr := *a.session.selectedPR
		if a.layout.activeView == viewDiff && a.session.selectedFile != "" {
			a.diff.marks = a.brain.HunkMarks(pr.Repo, pr.Number, a.session.selectedFile)
			a.diff.notes = a.brain.NotesForFile(pr.Repo, pr.Number, a.session.selectedFile)
			a.diff.redraw()
		}
		a.files.rebuild(a)
		a.prs.rebuild(a)
	}
	return nil
}

// onActionDone reports completion of an agent action. For tmux actions this
// fires as soon as the pane is spawned (not when the chat ends); for the
// fallback inline path it fires when the agent process exits.
func (a *app) onActionDone(msg actionDoneMsg) tea.Cmd {
	if msg.err != nil {
		a.statusMsg = fmt.Sprintf("%s: %s", msg.action, msg.err.Error())
		return nil
	}
	a.statusMsg = fmt.Sprintf("%s: launched", msg.action)
	return nil
}

// onInlineNotesReady lands parsed agent notes and writes them to the brain
// under source="agent" so they stay distinguishable from human notes.
func (a *app) onInlineNotesReady(msg inlineNotesReadyMsg) tea.Cmd {
	saved := 0
	for _, n := range msg.notes {
		if n.Path == "" || n.Line < 1 || n.Body == "" {
			continue
		}
		if err := a.brain.SaveAgentNote(msg.pr.Repo, msg.pr.Number, n.Path, n.Line, n.Body); err != nil {
			a.statusMsg = fmt.Sprintf("%s: save note: %s", msg.action, err.Error())
			return nil
		}
		saved++
	}
	a.statusMsg = fmt.Sprintf("%s: %d notes added", msg.action, saved)
	// Refresh list glyphs / diff overlay so new notes show up immediately.
	a.files.rebuild(a)
	a.prs.rebuild(a)
	if a.layout.activeView == viewDiff && a.session.selectedFile != "" {
		a.diff.notes = a.brain.NotesForFile(msg.pr.Repo, msg.pr.Number, a.session.selectedFile)
		a.diff.redraw()
	}
	return nil
}

// onNotePublished lands after a single inline-comment POST. On success we
// stamp the github_comment_id onto the local note so the next press of P
// won't re-publish it, and refresh the diff so the "→GH" marker shows up.
func (a *app) onNotePublished(msg notePublishedMsg) tea.Cmd {
	if msg.err != nil {
		a.statusMsg = "publish: " + msg.err.Error()
		return nil
	}
	if err := a.brain.SetNoteGitHubCommentID(msg.noteID, msg.ghID); err != nil {
		a.statusMsg = "publish saved on GitHub but local stamp failed: " + err.Error()
		return nil
	}
	if a.layout.activeView == viewDiff && a.session.selectedPR != nil && a.session.selectedFile != "" {
		a.diff.notes = a.brain.NotesForFile(a.session.selectedPR.Repo, a.session.selectedPR.Number, a.session.selectedFile)
		a.diff.redraw()
	}
	a.statusMsg = fmt.Sprintf("published note → GitHub #%d", msg.ghID)
	return nil
}

// onReviewSubmitted lands after gh.SubmitReview returns. The PR list doesn't
// re-fetch state — GitHub's approval status isn't rendered in the TUI
// today — but the status line confirms what shipped.
func (a *app) onReviewSubmitted(msg reviewSubmittedMsg) tea.Cmd {
	if msg.err != nil {
		a.statusMsg = "review: " + msg.err.Error()
		return nil
	}
	a.statusMsg = fmt.Sprintf("review submitted: %s on %s#%d", msg.event, msg.repo, msg.prNum)
	return nil
}

// onMergeSubmitted lands after gh.MergePR returns. On success we drop the PR
// from allPRs locally so it disappears from the lists without a full
// refetch — the next background refresh would catch it anyway, but that's
// not instant enough for the "M ctrl+s" feedback loop.
func (a *app) onMergeSubmitted(msg mergeSubmittedMsg) tea.Cmd {
	if msg.err != nil {
		a.statusMsg = "merge: " + msg.err.Error()
		return nil
	}
	a.statusMsg = fmt.Sprintf("merged: %s on %s#%d", msg.method, msg.repo, msg.prNum)
	key := brain.PRKey(msg.repo, msg.prNum)
	a.cache.dropPR(key)
	delete(a.session.pinnedAttention, key)
	a.prs.rebuild(a)
	go a.brain.SetPRCache(a.cache.allPRs)
	return nil
}

// onCommentsLoaded caches the GH comment streams for the PR. The comments
// view (if open on this PR) and the diff view (if open on a file in this PR)
// both pull from a.cache.prComments, so a redraw is enough to surface
// them — no per-view re-fetch.
func (a *app) onCommentsLoaded(msg commentsLoadedMsg) tea.Cmd {
	if msg.err != nil {
		a.statusMsg = "comments: " + msg.err.Error()
		return nil
	}
	a.cache.prComments[brain.PRKey(msg.repo, msg.prNum)] = msg.comments
	if a.layout.activeView == viewComments {
		a.comments.rebuild(a)
	}
	if a.layout.activeView == viewDiff && a.session.selectedPR != nil &&
		a.session.selectedPR.Repo == msg.repo && a.session.selectedPR.Number == msg.prNum {
		a.diff.refreshGHInline(a)
	}
	return nil
}

// onContributorsLoaded caches contributors for the repo so future mention
// picks are instant. Errors surface on the status line but don't block
// further attempts — the next ctrl+a will re-fetch.
func (a *app) onContributorsLoaded(msg contributorsLoadedMsg) tea.Cmd {
	if msg.err != nil {
		a.statusMsg = "contributors: " + msg.err.Error()
		return nil
	}
	a.cache.contributors[msg.repo] = msg.contributors
	if a.layout.activeView == viewDiff {
		a.diff.onContributorsReady(a, msg.repo)
	}
	return nil
}

// onPollTick re-reads the active PR's marks/notes from the brain. If
// anything changed since the last tick we rebuild items and redraw the
// diff so external writers (nvim in a separate tmux pane) show up.
// Reschedules itself as long as a PR is selected and the tick belongs
// to the current pollGen.
func (a *app) onPollTick(msg pollTickMsg) tea.Cmd {
	if msg.gen != a.pollGen || a.session.selectedPR == nil {
		return nil
	}
	pr := *a.session.selectedPR
	changed := false

	if a.layout.activeView == viewDiff && a.session.selectedFile != "" {
		newMarks := a.brain.HunkMarks(pr.Repo, pr.Number, a.session.selectedFile)
		if !reflect.DeepEqual(newMarks, a.diff.marks) {
			a.diff.marks = newMarks
			changed = true
		}
		newNotes := a.brain.NotesForFile(pr.Repo, pr.Number, a.session.selectedFile)
		if !reflect.DeepEqual(newNotes, a.diff.notes) {
			a.diff.notes = newNotes
			changed = true
		}
		if changed {
			a.diff.redraw()
		}
	}

	// Always rebuild item lists — cheap, and catches per-file status
	// flips that don't touch the current diff buffer but change file-list
	// glyphs.
	a.files.rebuild(a)
	a.prs.rebuild(a)

	return pollTickCmd(a.pollGen)
}

func pollTickCmd(gen int) tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg { return pollTickMsg{gen: gen} })
}

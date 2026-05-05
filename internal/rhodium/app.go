package rhodium

import (
	"fmt"
	"rhodium/internal/brain"
	"rhodium/internal/gh"
	"rhodium/internal/tui/comments"
	tuidiff "rhodium/internal/tui/diff"
	"rhodium/internal/tui/files"
	"rhodium/internal/tui/help"
	"rhodium/internal/tui/keys"
	"rhodium/internal/tui/merge"
	"rhodium/internal/tui/overlay"
	"rhodium/internal/tui/prs"
	"rhodium/internal/tui/review"
	"rhodium/internal/tui/router"
	"rhodium/internal/tui/styles"
	"rhodium/internal/tui/todo"
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

// remoteRefreshInterval controls how often we re-fetch GitHub side data
// (PR list per repo, comments for cached PRs) so review decisions, CI
// rollups, and incoming comments don't go stale. Separate from
// pollInterval so each can be tuned independently — GitHub calls cost
// more than SQLite reads.
const remoteRefreshInterval = 60 * time.Second

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

	todo     todo.Model
	prs      prs.Model
	files    files.Model
	diff     tuidiff.Model
	comments comments.Model
	help     help.Model

	// review modal lives at app level so any list view can open it.
	review review.Model
	merge  merge.Model

	// cache holds GitHub-fetched data shared across views.
	cache cache

	// session is per-run navigation state — selected PR/file, active
	// review session, where to return when the user backs out.
	session session

	// status carries the footer message and the polling generation.
	status status
}

func newApp(cfg *Config, b *brain.Brain) *app {
	a := &app{
		cfg:      cfg,
		brain:    b,
		layout:   layout{activeView: router.RouteTodo},
		todo:     todo.New(),
		prs:      prs.New(),
		files:    files.New(),
		diff:     tuidiff.New(),
		comments: comments.New(),
		help:     help.New(),
		review:   review.New(),
		merge:    merge.New(),
		cache:    newCache(),
		session:  newSession(),
	}
	a.files.AgentBindings = agentBindings(a)
	a.diff.AgentBindings = agentBindings(a)

	cached := b.CachedPRs()
	if len(cached) > 0 {
		a.cache.allPRs = cached
		a.rebuildPRs()
		a.prs.SetTitle(fmt.Sprintf("PRs (%d, refreshing…)", len(cached)))
	} else {
		a.prs.SetTitle("PRs (loading...)")
	}

	return a
}

func (a *app) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(a.cfg.Repos)+1)
	for _, repo := range a.cfg.Repos {
		cmds = append(cmds, loadRepoPRsCmd(repo))
	}
	cmds = append(cmds, remoteRefreshTickCmd())
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
	case tuidiff.CatchUpLoadedMsg, tuidiff.DiamondClassifiedMsg, tuidiff.BlobLoadedMsg:
		return a, a.diff.Update(msg, a.brain, globalBindings(a))
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
	case inlineReplyPostedMsg:
		return a, a.onInlineReplyPosted(m)
	case prsRefreshedMsg:
		return a, a.onPRsRefreshed(m)
	case remoteRefreshTickMsg:
		return a, a.onRemoteRefreshTick()
	case review.SubmittedMsg:
		return a, a.onReviewSubmitted(m)
	case review.StatusMsg:
		a.status.msg = m.Text
		return a, nil
	case merge.SubmittedMsg:
		return a, a.onMergeSubmitted(m)
	case merge.StatusMsg:
		a.status.msg = m.Text
		return a, nil
	case contributorsLoadedMsg:
		return a, a.onContributorsLoaded(m)
	case commentsLoadedMsg:
		return a, a.onCommentsLoaded(m)
	case pollTickMsg:
		return a, a.onPollTick(m)

	case statusMsg:
		a.status.msg = m.Text
		return a, nil

	case router.NavigatedMsg:
		a.layout.focus(m.To)
		return a, nil

	case todo.OpenPRMsg:
		return a, a.openPR(m.PR)
	case todo.ReviewMsg:
		return a, a.review.OpenFor(m.PR)
	case todo.MergeMsg:
		return a, a.merge.OpenFor(m.PR, gh.MergeMethod(a.cfg.MergeMethodResolved()))
	case todo.CommentsMsg:
		return a, a.openCommentsForPR(m.PR, router.RouteTodo)

	case files.OpenFileMsg:
		return a, a.openFile(m.File)
	case files.OpenCommentsMsg:
		return a, a.openComments(router.RouteFiles)
	case files.RebuildNotesMsg:
		a.rebuildFilesNotes()
		return a, nil
	case files.MarkFullyReviewedMsg:
		return a, a.onMarkFullyReviewed(m)

	case prs.OpenPRMsg:
		return a, a.openPR(m.PR)
	case prs.ReviewMsg:
		return a, a.review.OpenFor(m.PR)
	case prs.MergeMsg:
		return a, a.merge.OpenFor(m.PR, gh.MergeMethod(a.cfg.MergeMethodResolved()))
	case prs.CommentsMsg:
		return a, a.openCommentsForPR(m.PR, router.RoutePRs)
	case prs.ScrutinyToggleMsg:
		return a, a.toggleScrutiny(m.PR)

	case tuidiff.LeavingMsg:
		a.rebuildFiles()
		a.rebuildPRs()
		return a, router.Navigate(router.RouteFiles)
	case tuidiff.FileMarkedDoneMsg:
		a.markSessionFileDone(m.Path)
		return a, nil
	case tuidiff.StatusMsg:
		a.status.msg = m.Text
		return a, nil
	case tuidiff.OpenEditorMsg:
		return a, a.openInEditor(m)
	case tuidiff.PublishNoteMsg:
		return a, a.publishNote(m)
	case tuidiff.ReplyInlineMsg:
		return a, a.replyInline(m)
	case tuidiff.LoadContributorsMsg:
		return a, loadContributorsCmd(m.Repo)

	case tea.KeyMsg:
		if a.help.Open {
			switch m.String() {
			case "?", "esc", "q", "ctrl+c":
				a.help.Open = false
			}
			return a, nil
		}
		if a.review.Active {
			return a, a.review.Update(m)
		}
		if a.merge.Active {
			return a, a.merge.Update(m)
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
	case router.RouteTodo:
		body = a.todo.View()
	case router.RoutePRs:
		body = a.prs.View()
	case router.RouteFiles:
		body = a.files.View()
	case router.RouteDiff:
		body = a.diff.View()
	case router.RouteComments:
		body = a.comments.View()
	}
	rendered := styles.App.Render(body) + "\n" + lipgloss.NewStyle().Faint(true).Render(a.footer())

	if a.review.Active {
		rendered = centerOverlay(rendered, a.review.Render(), a.layout.width, a.layout.height)
	}
	if a.merge.Active {
		rendered = centerOverlay(rendered, a.merge.Render(), a.layout.width, a.layout.height)
	}
	if a.help.Open {
		rendered = centerOverlay(rendered, a.renderHelp(), a.layout.width, a.layout.height)
	}
	return rendered
}

// renderHelp composes the active view's bindings + always-on globals and
// the view's display label, then hands them to the help package. The view
// enum stays here (not in internal/tui/help) so the package boundary is
// unaware of which views exist.
func (a *app) renderHelp() string {
	var bindings []keys.Binding
	var label string
	switch a.layout.activeView {
	case router.RouteTodo:
		bindings = a.todo.Bindings()
		label = "Todo"
	case router.RoutePRs:
		bindings = a.prs.Bindings()
		label = "All PRs"
	case router.RouteFiles:
		bindings = a.files.Bindings()
		label = "Files"
	case router.RouteDiff:
		bindings = a.diff.Bindings()
		label = "Diff"
	case router.RouteComments:
		bindings = a.comments.Bindings()
		label = "Comments"
	}
	bindings = append(bindings, globalBindings(a)...)
	return a.help.Render(label, bindings)
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
	return overlay.Render(bg, fg, x, y)
}

// --- routing ---

func (a *app) routeKey(key tea.KeyMsg) tea.Cmd {
	switch a.layout.activeView {
	case router.RouteTodo:
		return a.todo.Update(key, globalBindings(a))
	case router.RoutePRs:
		return a.prs.Update(key, globalBindings(a))
	case router.RouteFiles:
		return a.files.Update(key, globalBindings(a))
	case router.RouteDiff:
		return a.diff.Update(key, a.brain, globalBindings(a))
	case router.RouteComments:
		return a.comments.Update(key, globalBindings(a))
	}
	return nil
}

func (a *app) routeToActive(msg tea.Msg) tea.Cmd {
	switch a.layout.activeView {
	case router.RouteTodo:
		return a.todo.Update(msg, globalBindings(a))
	case router.RoutePRs:
		return a.prs.Update(msg, globalBindings(a))
	case router.RouteFiles:
		return a.files.Update(msg, globalBindings(a))
	case router.RouteDiff:
		return a.diff.Update(msg, a.brain, globalBindings(a))
	case router.RouteComments:
		return a.comments.Update(msg, globalBindings(a))
	}
	return nil
}

func (a *app) relayout() {
	h, padV := styles.App.GetFrameSize()
	listW, listH := a.layout.width-h, a.layout.height-padV-1
	a.todo.Resize(listW, listH)
	a.prs.Resize(listW, listH)
	a.files.Resize(listW, listH)
	a.diff.Resize(listW, listH)
	a.diff.SetLayoutSize(a.layout.width, a.layout.height)
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
	a.layout.focus(router.RouteFiles)
	a.files.BackRoute = a.session.listOrigin
	a.rebuildFilesDesc()
	gen := a.status.bumpPoll()
	key := brain.PRKey(pr.Repo, pr.Number)
	cmds := []tea.Cmd{pollTickCmd(gen)}
	if _, cached := a.cache.prComments[key]; !cached {
		cmds = append(cmds, loadCommentsCmd(pr))
	}
	// Resolve stale notes in the background — non-blocking so the files
	// view appears immediately.
	repo, num := pr.Repo, pr.Number
	headSHA := pr.HeadSHA
	cmds = append(cmds, func() tea.Msg {
		go func() {
			n, err := a.brain.ResolveStaleNotes(repo, num, headSHA)
			if err != nil {
				program.Send(statusMsg{"stale notes: " + err.Error()})
				return
			}
			if n > 0 {
				suffix := "notes"
				if n == 1 {
					suffix = "note"
				}
				program.Send(statusMsg{fmt.Sprintf("resolved %d stale %s", n, suffix)})
			}
		}()
		return nil
	})
	if _, cached := a.cache.prFiles[key]; cached {
		a.rebuildFiles()
		a.files.SetTitle(fmt.Sprintf("Files in %s#%d", pr.Repo, pr.Number))
		return tea.Batch(cmds...)
	}
	a.files.SetTitle(fmt.Sprintf("Files in %s#%d (loading...)", pr.Repo, pr.Number))
	a.files.ClearItems()
	cmds = append(cmds, loadFilesCmd(pr))
	return tea.Batch(cmds...)
}

// openFile transitions files → diff, delegating to diff.Model.Open for
// the actual state reset + fetch commands.
func (a *app) openFile(fc gh.FileChange) tea.Cmd {
	if a.session.selectedPR == nil {
		return nil
	}
	a.session.selectedFile = fc.Path
	a.layout.focus(router.RouteDiff)
	pr := a.session.selectedPR
	ghInline := ghInlineForFile(a, fc.Path)
	return a.diff.Open(a.brain, pr, fc, ghInline)
}

// ghInlineForFile filters the cached PR comments down to inline ones on
// the given path. Empty result is fine — comments may not have loaded
// yet, or the file simply has none.
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

// openComments transitions to the PR-level comments view. The comments
// fetch was kicked off when the PR was opened, so usually they're already
// cached; if not, the view shows a "loading" placeholder until
// commentsLoadedMsg lands.
func (a *app) openComments(returnTo router.Route) tea.Cmd {
	if a.session.selectedPR == nil {
		return nil
	}
	a.comments.ReturnTo = returnTo
	a.layout.focus(router.RouteComments)
	a.rebuildComments()
	return nil
}

// rebuildComments hands the comments view the data it needs to render: the
// current PR (or nil) and the cached comment slice (loaded=false means the
// fetch is still in flight).
func (a *app) rebuildComments() {
	pr := a.session.selectedPR
	var c []gh.Comment
	loaded := false
	if pr != nil {
		c, loaded = a.cache.prComments[brain.PRKey(pr.Repo, pr.Number)]
	}
	a.comments.Rebuild(pr, c, loaded)
}

// openCommentsForPR is the shared "C from a list" path: stamp selectedPR,
// kick off a comments fetch if we don't have them yet, and route into the
// comments view. Used by todo.CommentsMsg today; prs/files inline today.
func (a *app) openCommentsForPR(pr gh.PR, returnTo router.Route) tea.Cmd {
	a.session.selectedPR = &pr
	if _, cached := a.cache.prComments[brain.PRKey(pr.Repo, pr.Number)]; !cached {
		return tea.Batch(loadCommentsCmd(pr), a.openComments(returnTo))
	}
	return a.openComments(returnTo)
}

// rebuildTodo walks a.cache.allPRs and emits a todo.Item for each PR with
// outstanding work. Splits into "needs attention" (in-progress, catch-up,
// notes) and "new" (never-touched) buckets, with attention pinning so the
// list doesn't reshuffle as the user marks things reviewed.
func (a *app) rebuildTodo() {
	var actionable, newPRs []todo.Item
	for _, pr := range a.cache.allPRs {
		key := brain.PRKey(pr.Repo, pr.Number)
		ti := a.buildTodoItem(pr)

		// Pin PRs to "needs attention" once they first appear there —
		// prevents the list from shifting under the user.
		isActionableNow := ti != nil && !(len(ti.Tags) == 1 && ti.Tags[0] == "unseen")
		if isActionableNow {
			a.session.pinAttention(key)
		}

		if a.session.isPinnedAttention(key) {
			if ti == nil {
				ti = &todo.Item{PR: pr, Tags: []string{"done"}}
			}
			actionable = append(actionable, *ti)
			continue
		}
		if ti == nil {
			continue
		}
		newPRs = append(newPRs, *ti)
	}
	a.todo.Rebuild(actionable, newPRs, a.outstandingPRCount())
}

// buildTodoItem returns a todo.Item for pr if it needs attention, or nil
// otherwise. Centralizes the brain queries so the todo package can stay
// dumb about brain state.
func (a *app) buildTodoItem(pr gh.PR) *todo.Item {
	if !a.prHasOutstandingWork(pr) {
		return nil
	}
	notes := a.brain.NoteCountForPR(pr.Repo, pr.Number)
	now, soon, _, _ := a.brain.NoteCountByUrgency(pr.Repo, pr.Number)
	cu := a.brain.ActiveSession(pr.Repo, pr.Number)
	touched := a.brain.HasAnyMarks(pr.Repo, pr.Number) ||
		len(a.brain.AllFileReviewedStates(pr.Repo, pr.Number)) > 0

	files, filesLoaded := a.cache.prFiles[brain.PRKey(pr.Repo, pr.Number)]
	var remaining int
	if filesLoaded {
		remaining = a.brain.UnseenCount(pr.Repo, pr.Number, files)
	}

	it := todo.Item{PR: pr, Notes: notes, NotesNow: now, NotesSoon: soon, Remaining: remaining}
	if touched && cu == nil {
		if !filesLoaded || remaining > 0 {
			it.Tags = append(it.Tags, "in-progress")
		}
	}
	if cu != nil {
		it.Tags = append(it.Tags, "catch-up")
		it.Done = cu.FilesDone
		it.Total = cu.FilesTotal
	}
	if !touched && cu == nil {
		it.Tags = append(it.Tags, "unseen")
	}
	if notes > 0 {
		it.Tags = append(it.Tags, "notes")
	}
	return &it
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
	if a.status.msg != "" {
		return a.status.msg
	}
	if a.review.Active {
		return a.review.Footer()
	}
	if a.merge.Active {
		return a.merge.Footer()
	}
	switch a.layout.activeView {
	case router.RouteTodo:
		return a.todo.Footer()
	case router.RoutePRs:
		return a.prs.Footer()
	case router.RouteFiles:
		return a.files.Footer()
	case router.RouteDiff:
		return a.diff.Footer()
	case router.RouteComments:
		return a.comments.Footer()
	}
	return ""
}

// --- async message handlers ---

func (a *app) onPRsLoaded(msg prsLoadedMsg) tea.Cmd {
	if msg.err != nil {
		a.status.msg = "error: " + msg.err.Error()
		return nil
	}
	for _, p := range msg.prs {
		a.cache.markFresh(brain.PRKey(p.Repo, p.Number))
	}
	added := mergePRs(a, msg.prs)
	a.rebuildPRs()
	a.prs.SetTitle(fmt.Sprintf("PRs (%d, loading files…)", len(a.cache.allPRs)))
	go a.brain.SetPRCache(a.cache.allPRs)
	return prefetchAllCmd(added)
}

func (a *app) onFilesLoaded(msg filesLoadedMsg) tea.Cmd {
	if msg.err != nil {
		a.status.msg = "error: " + msg.err.Error()
		return nil
	}
	key := brain.PRKey(msg.pr.Repo, msg.pr.Number)
	a.cache.prFiles[key] = msg.files
	a.rebuildPRs()
	if a.session.selectedPR != nil && brain.PRKey(a.session.selectedPR.Repo, a.session.selectedPR.Number) == key {
		a.rebuildFiles()
		a.files.SetTitle(fmt.Sprintf("Files in %s#%d", msg.pr.Repo, msg.pr.Number))
	}
	if a.brain.IsScrutinized(msg.pr.Repo, msg.pr.Number) {
		return nil
	}
	return autoAdvanceCmd(a.brain, msg.pr, msg.files)
}

func (a *app) onAutoAdvance(msg autoAdvanceMsg) tea.Cmd {
	if len(msg.advancedFiles) > 0 {
		a.rebuildPRs()
		if a.session.selectedPR != nil && brain.PRKey(a.session.selectedPR.Repo, a.session.selectedPR.Number) == msg.prKey {
			a.rebuildFiles()
			a.session.review = a.brain.ActiveSession(a.session.selectedPR.Repo, a.session.selectedPR.Number)
		}
		a.status.msg = fmt.Sprintf("✓ auto-caught-up %d files", len(msg.advancedFiles))
	}
	return nil
}

func (a *app) onPrefetchDone() tea.Cmd {
	if len(a.cache.freshKeys) > 0 {
		a.cache.pruneStale()
		a.rebuildPRs()
		go a.brain.SetPRCache(a.cache.allPRs)
	}
	a.prs.SetTitle(fmt.Sprintf("PRs (%d)", len(a.cache.allPRs)))
	return nil
}

// onEditorDone runs after an external editor exits. For the tea.ExecProcess
// path this fires once the user quits nvim; for the tmux path it fires
// immediately after spawning the pane/window. In both cases we refresh
// the current PR's marks/notes so any changes made in nvim show up.
func (a *app) onEditorDone(msg editorDoneMsg) tea.Cmd {
	if msg.err != nil {
		a.status.msg = "editor: " + msg.err.Error()
		return nil
	}
	if a.session.selectedPR != nil {
		if a.layout.activeView == router.RouteDiff && a.session.selectedFile != "" {
			a.diff.RefreshFromBrain(a.brain)
		}
		a.rebuildFiles()
		a.rebuildPRs()
	}
	return nil
}

// onActionDone reports completion of an agent action. For tmux actions this
// fires as soon as the pane is spawned (not when the chat ends); for the
// fallback inline path it fires when the agent process exits.
func (a *app) onActionDone(msg actionDoneMsg) tea.Cmd {
	if msg.err != nil {
		a.status.msg = fmt.Sprintf("%s: %s", msg.action, msg.err.Error())
		return nil
	}
	a.status.msg = fmt.Sprintf("%s: launched", msg.action)
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
		if err := a.brain.SaveAgentNote(msg.pr.Repo, msg.pr.Number, n.Path, n.Line, n.Body, msg.pr.HeadSHA); err != nil {
			a.status.msg = fmt.Sprintf("%s: save note: %s", msg.action, err.Error())
			return nil
		}
		saved++
	}
	a.status.msg = fmt.Sprintf("%s: %d notes added", msg.action, saved)
	// Refresh list glyphs / diff overlay so new notes show up immediately.
	a.rebuildFiles()
	a.rebuildPRs()
	if a.layout.activeView == router.RouteDiff && a.session.selectedFile != "" {
		a.diff.RefreshNotes(a.brain)
	}
	return nil
}

// onNotePublished lands after a single inline-comment POST. On success we
// stamp the github_comment_id onto the local note so the next press of P
// won't re-publish it, and refresh the diff so the "→GH" marker shows up.
func (a *app) onNotePublished(msg notePublishedMsg) tea.Cmd {
	if msg.err != nil {
		a.status.msg = "publish: " + msg.err.Error()
		return nil
	}
	if err := a.brain.SetNoteGitHubCommentID(msg.noteID, msg.ghID); err != nil {
		a.status.msg = "publish saved on GitHub but local stamp failed: " + err.Error()
		return nil
	}
	if a.layout.activeView == router.RouteDiff && a.session.selectedPR != nil && a.session.selectedFile != "" {
		a.diff.RefreshNotes(a.brain)
	}
	a.status.msg = fmt.Sprintf("published note → GitHub #%d", msg.ghID)
	return nil
}

// onInlineReplyPosted lands after gh.ReplyToInlineComment returns. On
// success we re-fetch the PR's comments so the new reply renders in
// the diff view (RefreshGHInline runs from onCommentsLoaded).
func (a *app) onInlineReplyPosted(msg inlineReplyPostedMsg) tea.Cmd {
	if msg.err != nil {
		a.status.msg = "reply: " + msg.err.Error()
		return nil
	}
	a.status.msg = fmt.Sprintf("reply posted → GitHub #%d", msg.ghID)
	return loadCommentsCmd(gh.PR{Repo: msg.repo, Number: msg.prNum})
}

// onReviewSubmitted lands after gh.SubmitReview returns. We optimistically
// flip the local PR's ReviewDecision so the badge updates instantly; a
// background re-fetch reconciles in case GitHub computed something
// different (e.g. branch protection hasn't accepted the approval).
func (a *app) onReviewSubmitted(msg review.SubmittedMsg) tea.Cmd {
	if msg.Err != nil {
		a.status.msg = "review: " + msg.Err.Error()
		return nil
	}
	a.status.msg = fmt.Sprintf("review submitted: %s on %s#%d", msg.Event, msg.Repo, msg.PRNum)
	if optimistic := optimisticDecision(msg.Event); optimistic != "" {
		for i := range a.cache.allPRs {
			p := &a.cache.allPRs[i]
			if p.Repo == msg.Repo && p.Number == msg.PRNum {
				p.ReviewDecision = optimistic
				break
			}
		}
		a.rebuildPRs()
	}
	return refreshRepoCmd(msg.Repo)
}

// optimisticDecision maps a review event to the ReviewDecision GitHub
// will return once the review lands. COMMENT leaves the decision
// untouched, so we return "" and let the next remote refresh resolve.
func optimisticDecision(event gh.ReviewEvent) string {
	switch event {
	case gh.ReviewApprove:
		return "APPROVED"
	case gh.ReviewRequestChanges:
		return "CHANGES_REQUESTED"
	}
	return ""
}

// onMergeSubmitted lands after gh.MergePR returns. On success we drop the PR
// from allPRs locally so it disappears from the lists without a full
// refetch — the next background refresh would catch it anyway, but that's
// not instant enough for the "M ctrl+s" feedback loop.
func (a *app) onMergeSubmitted(msg merge.SubmittedMsg) tea.Cmd {
	if msg.Err != nil {
		a.status.msg = "merge: " + msg.Err.Error()
		return nil
	}
	a.status.msg = fmt.Sprintf("merged: %s on %s#%d", msg.Method, msg.Repo, msg.PRNum)
	key := brain.PRKey(msg.Repo, msg.PRNum)
	a.cache.dropPR(key)
	delete(a.session.pinnedAttention, key)
	a.rebuildPRs()
	go a.brain.SetPRCache(a.cache.allPRs)
	return nil
}

// onCommentsLoaded caches the GH comment streams for the PR. The comments
// view (if open on this PR) and the diff view (if open on a file in this PR)
// both pull from a.cache.prComments, so a redraw is enough to surface
// them — no per-view re-fetch.
func (a *app) onCommentsLoaded(msg commentsLoadedMsg) tea.Cmd {
	if msg.err != nil {
		a.status.msg = "comments: " + msg.err.Error()
		return nil
	}
	a.cache.prComments[brain.PRKey(msg.repo, msg.prNum)] = msg.comments
	go func() {
		if _, err := a.brain.SyncGitHubComments(msg.repo, msg.prNum); err != nil {
			// Silent — comments are already in-memory; the brain sync
			// is best-effort and will retry on the next remote refresh.
			_ = err
		}
	}()
	if a.layout.activeView == router.RouteComments {
		a.rebuildComments()
	}
	if a.layout.activeView == router.RouteDiff && a.session.selectedPR != nil &&
		a.session.selectedPR.Repo == msg.repo && a.session.selectedPR.Number == msg.prNum {
		a.diff.RefreshGHInline(msg.comments)
	}
	return nil
}

// onContributorsLoaded caches contributors for the repo so future mention
// picks are instant. Errors surface on the status line but don't block
// further attempts — the next ctrl+a will re-fetch.
func (a *app) onContributorsLoaded(msg contributorsLoadedMsg) tea.Cmd {
	if msg.err != nil {
		a.status.msg = "contributors: " + msg.err.Error()
		return nil
	}
	a.diff.SetContributors(msg.repo, msg.contributors)
	return nil
}

// onPollTick re-reads the active PR's marks/notes from the brain. If
// anything changed since the last tick we rebuild items and redraw the
// diff so external writers (nvim in a separate tmux pane) show up.
// Reschedules itself as long as a PR is selected and the tick belongs
// to the current pollGen.
func (a *app) onPollTick(msg pollTickMsg) tea.Cmd {
	if msg.gen != a.status.pollGen || a.session.selectedPR == nil {
		return nil
	}
	if a.layout.activeView == router.RouteDiff && a.session.selectedFile != "" {
		a.diff.RefreshFromBrain(a.brain)
	}

	// Always rebuild item lists — cheap, and catches per-file status
	// flips that don't touch the current diff buffer but change file-list
	// glyphs.
	a.rebuildFiles()
	a.rebuildPRs()

	return pollTickCmd(a.status.pollGen)
}

func pollTickCmd(gen int) tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg { return pollTickMsg{gen: gen} })
}

// onRemoteRefreshTick refreshes GitHub-side data periodically: re-list
// PRs for every configured repo (catches review-decision / CI / merge
// state drift) and re-fetch comments for every PR with a populated
// comment cache (catches incoming replies). Reschedules itself so the
// loop runs for the lifetime of the program.
func (a *app) onRemoteRefreshTick() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(a.cfg.Repos)+len(a.cache.prComments)+1)
	for _, repo := range a.cfg.Repos {
		cmds = append(cmds, refreshRepoCmd(repo))
	}
	for _, pr := range a.cache.allPRs {
		if _, has := a.cache.prComments[brain.PRKey(pr.Repo, pr.Number)]; has {
			cmds = append(cmds, loadCommentsCmd(pr))
		}
	}
	cmds = append(cmds, remoteRefreshTickCmd())
	return tea.Batch(cmds...)
}

func remoteRefreshTickCmd() tea.Cmd {
	return tea.Tick(remoteRefreshInterval, func(time.Time) tea.Msg { return remoteRefreshTickMsg{} })
}

// onPRsRefreshed merges a fresh per-repo listing into the cache: live-
// status fields on existing rows are overwritten, new PRs are appended,
// and PRs no longer in the listing for this repo are dropped (they've
// been merged or closed). HeadSHA/BaseSHA are also updated so subsequent
// openPR uses the current commits.
func (a *app) onPRsRefreshed(msg prsRefreshedMsg) tea.Cmd {
	if msg.err != nil {
		a.status.msg = "refresh: " + msg.err.Error()
		return nil
	}
	byKey := make(map[string]gh.PR, len(msg.prs))
	for _, p := range msg.prs {
		byKey[brain.PRKey(p.Repo, p.Number)] = p
	}
	kept := a.cache.allPRs[:0]
	for _, existing := range a.cache.allPRs {
		if existing.Repo != msg.repo {
			kept = append(kept, existing)
			continue
		}
		key := brain.PRKey(existing.Repo, existing.Number)
		fresh, ok := byKey[key]
		if !ok {
			delete(a.cache.prFiles, key)
			delete(a.cache.prComments, key)
			delete(a.session.pinnedAttention, key)
			continue
		}
		existing.Title = fresh.Title
		existing.Author = fresh.Author
		existing.Body = fresh.Body
		existing.HeadSHA = fresh.HeadSHA
		existing.BaseSHA = fresh.BaseSHA
		existing.ReviewDecision = fresh.ReviewDecision
		existing.IsDraft = fresh.IsDraft
		existing.Mergeable = fresh.Mergeable
		existing.CIStatus = fresh.CIStatus
		kept = append(kept, existing)
		delete(byKey, key)
	}
	a.cache.allPRs = kept
	for _, p := range msg.prs {
		if _, still := byKey[brain.PRKey(p.Repo, p.Number)]; still {
			a.cache.allPRs = append(a.cache.allPRs, p)
		}
	}
	a.rebuildPRs()
	go a.brain.SetPRCache(a.cache.allPRs)
	return nil
}

func (a *app) onMarkFullyReviewed(msg files.MarkFullyReviewedMsg) tea.Cmd {
	pr := a.session.selectedPR
	if pr == nil || len(msg.Paths) == 0 {
		return nil
	}
	if err := a.brain.MarkFullyReviewed(pr.Repo, pr.Number, pr.HeadSHA, pr.BaseSHA, msg.Paths); err != nil {
		a.status.msg = "mark: " + err.Error()
		return nil
	}
	a.rebuildFiles()
	a.rebuildPRs()
	a.status.msg = fmt.Sprintf("marked %s#%d reviewed — %d files", pr.Repo, pr.Number, len(msg.Paths))
	return nil
}

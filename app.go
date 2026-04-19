package main

import (
	"fmt"
	"reflect"
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
// through a.activeView; every view method takes *app so transitions
// and shared lookups have one home.
//
// This split is deliberately shaped for the eventual daemon move:
// everything that would live on the daemon side (brain reads/writes,
// the PR cache, auto-advance state) hangs off app; everything that
// would remain client-local (list cursors, viewport offsets, note
// input) hangs off the per-view structs.
type app struct {
	cfg   *Config
	brain *Brain

	width, height int
	activeView    view

	todo  todoView
	prs   prsView
	files filesView
	diff  diffView

	// Shared PR data. prFiles is keyed by "<repo>#<num>".
	allPRs          []PR
	freshKeys       map[string]bool // keys confirmed still open by a repo listing
	prFiles         map[string][]FileChange
	pinnedAttention map[string]bool // pr keys pinned in todo's "needs attention"

	selectedPR     *PR
	selectedFile   string
	listViewOrigin view // whichever of viewTodo/viewPRs the user drilled from

	catchUpSession *CatchUpSession

	statusMsg string

	// pollGen increments every time a PR is opened; the polling tick
	// carries the generation it was scheduled under, so older loops stop
	// naturally when a newer PR is selected.
	pollGen int
}

func newApp(cfg *Config, brain *Brain) *app {
	a := &app{
		cfg:             cfg,
		brain:           brain,
		activeView:      viewTodo,
		todo:            newTodoView(),
		prs:             newPRsView(),
		files:           newFilesView(),
		diff:            newDiffView(),
		prFiles:         map[string][]FileChange{},
		freshKeys:       map[string]bool{},
		pinnedAttention: map[string]bool{},
	}

	cached := brain.CachedPRs()
	if len(cached) > 0 {
		a.allPRs = cached
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
		a.width, a.height = m.Width, m.Height
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
	case pollTickMsg:
		return a, a.onPollTick(m)

	case tea.KeyMsg:
		return a, a.routeKey(m)
	}

	// Non-key messages the type switch didn't claim — send to the active
	// sub-model so bubbles internals (mouse events, etc.) still work.
	return a, a.routeToActive(msg)
}

func (a *app) View() string {
	var body string
	switch a.activeView {
	case viewTodo:
		body = a.todo.View(a)
	case viewPRs:
		body = a.prs.View(a)
	case viewFiles:
		body = a.files.View(a)
	case viewDiff:
		body = a.diff.View(a)
	}
	return appStyle.Render(body) + "\n" + lipgloss.NewStyle().Faint(true).Render(a.footer())
}

// --- routing ---

func (a *app) routeKey(key tea.KeyMsg) tea.Cmd {
	switch a.activeView {
	case viewTodo:
		return a.todo.Update(a, key)
	case viewPRs:
		return a.prs.Update(a, key)
	case viewFiles:
		return a.files.Update(a, key)
	case viewDiff:
		return a.diff.Update(a, key)
	}
	return nil
}

func (a *app) routeToActive(msg tea.Msg) tea.Cmd {
	switch a.activeView {
	case viewTodo:
		return a.todo.Update(a, msg)
	case viewPRs:
		return a.prs.Update(a, msg)
	case viewFiles:
		return a.files.Update(a, msg)
	case viewDiff:
		return a.diff.Update(a, msg)
	}
	return nil
}

func (a *app) relayout() {
	h, padV := appStyle.GetFrameSize()
	listW, listH := a.width-h, a.height-padV-1
	a.todo.Resize(listW, listH)
	a.prs.Resize(listW, listH)
	a.files.Resize(listW, listH)
	a.diff.Resize(listW, listH)
}

// --- transitions ---

// openPR transitions todo/prs → files, loading the file list if it isn't
// already cached. Bumps pollGen so any in-flight tick from a previous PR
// stops silently.
func (a *app) openPR(pr PR) tea.Cmd {
	a.listViewOrigin = a.activeView
	a.selectedPR = &pr
	a.catchUpSession = a.brain.ActiveCatchUp(pr.Repo, pr.Number)
	a.activeView = viewFiles
	a.files.rebuildDescVP(a)
	a.pollGen++
	key := prKey(pr.Repo, pr.Number)
	if _, cached := a.prFiles[key]; cached {
		a.files.rebuild(a)
		a.files.list.Title = fmt.Sprintf("Files in %s#%d", pr.Repo, pr.Number)
		return pollTickCmd(a.pollGen)
	}
	a.files.loadingFiles = true
	a.files.list.Title = fmt.Sprintf("Files in %s#%d (loading...)", pr.Repo, pr.Number)
	a.files.list.SetItems(nil)
	return tea.Batch(loadFilesCmd(pr), pollTickCmd(a.pollGen))
}

// openFile transitions files → diff, delegating to diffView.open for the
// actual state reset + fetch commands.
func (a *app) openFile(fc FileChange) tea.Cmd {
	return a.diff.open(a, fc)
}

// currentFile returns the FileChange for a.selectedFile from the PR's
// cached file list, if present.
func (a *app) currentFile() (FileChange, bool) {
	if a.selectedPR == nil {
		return FileChange{}, false
	}
	for _, f := range a.prFiles[prKey(a.selectedPR.Repo, a.selectedPR.Number)] {
		if f.Path == a.selectedFile {
			return f, true
		}
	}
	return FileChange{}, false
}

// advanceCatchUpSession advances the active catch-up session by one file.
func (a *app) advanceCatchUpSession() {
	if a.catchUpSession != nil {
		a.brain.CatchUpAdvanceFile(a.catchUpSession.ID)
		a.catchUpSession = a.brain.ActiveCatchUp(a.selectedPR.Repo, a.selectedPR.Number)
	}
}

// --- footer composition ---

func (a *app) footer() string {
	if a.statusMsg != "" {
		return a.statusMsg
	}
	switch a.activeView {
	case viewTodo:
		return a.todo.Footer(a)
	case viewPRs:
		return a.prs.Footer(a)
	case viewFiles:
		return a.files.Footer(a)
	case viewDiff:
		return a.diff.Footer(a)
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
		a.freshKeys[prKey(p.Repo, p.Number)] = true
	}
	added := mergePRs(a, msg.prs)
	a.prs.rebuild(a)
	a.prs.list.Title = fmt.Sprintf("PRs (%d, loading files…)", len(a.allPRs))
	go a.brain.SetPRCache(a.allPRs)
	return prefetchAllCmd(added)
}

func (a *app) onFilesLoaded(msg filesLoadedMsg) tea.Cmd {
	a.files.loadingFiles = false
	if msg.err != nil {
		a.statusMsg = "error: " + msg.err.Error()
		return nil
	}
	key := prKey(msg.pr.Repo, msg.pr.Number)
	a.prFiles[key] = msg.files
	a.prs.rebuild(a)
	if a.selectedPR != nil && prKey(a.selectedPR.Repo, a.selectedPR.Number) == key {
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
		if a.selectedPR != nil && prKey(a.selectedPR.Repo, a.selectedPR.Number) == msg.prKey {
			a.files.rebuild(a)
			a.catchUpSession = a.brain.ActiveCatchUp(a.selectedPR.Repo, a.selectedPR.Number)
		}
		a.statusMsg = fmt.Sprintf("✓ auto-caught-up %d files", len(msg.advancedFiles))
	}
	return nil
}

func (a *app) onPrefetchDone() tea.Cmd {
	if len(a.freshKeys) > 0 {
		var live []PR
		for _, p := range a.allPRs {
			if a.freshKeys[prKey(p.Repo, p.Number)] {
				live = append(live, p)
			}
		}
		a.allPRs = live
		a.prs.rebuild(a)
		go a.brain.SetPRCache(a.allPRs)
	}
	a.prs.list.Title = fmt.Sprintf("PRs (%d)", len(a.allPRs))
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
	if a.selectedPR != nil {
		pr := *a.selectedPR
		if a.activeView == viewDiff && a.selectedFile != "" {
			a.diff.marks = a.brain.HunkMarks(pr.Repo, pr.Number, a.selectedFile)
			a.diff.notes = a.brain.NotesForFile(pr.Repo, pr.Number, a.selectedFile)
			a.diff.redraw()
		}
		a.files.rebuild(a)
		a.prs.rebuild(a)
	}
	return nil
}

// onPollTick re-reads the active PR's marks/notes from the brain. If
// anything changed since the last tick we rebuild items and redraw the
// diff so external writers (nvim in a separate tmux pane) show up.
// Reschedules itself as long as a PR is selected and the tick belongs
// to the current pollGen.
func (a *app) onPollTick(msg pollTickMsg) tea.Cmd {
	if msg.gen != a.pollGen || a.selectedPR == nil {
		return nil
	}
	pr := *a.selectedPR
	changed := false

	if a.activeView == viewDiff && a.selectedFile != "" {
		newMarks := a.brain.HunkMarks(pr.Repo, pr.Number, a.selectedFile)
		if !reflect.DeepEqual(newMarks, a.diff.marks) {
			a.diff.marks = newMarks
			changed = true
		}
		newNotes := a.brain.NotesForFile(pr.Repo, pr.Number, a.selectedFile)
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

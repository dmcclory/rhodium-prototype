package main

import (
	"fmt"

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
	infoVP       viewport.Model // for notes tab
	descVP       viewport.Model // always-visible PR description pane

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
	catchUpMode     bool             // true when showing only the delta since last review
	catchUpOldHead  string           // the head SHA we last reviewed at (f1)
	catchUpOldBase  string           // the base SHA we last reviewed at (b1)
	catchUpClass    Class            // diff4 classification of the catch-up
	catchUpPatch    string           // the delta patch for the current file
	catchUpSession  *CatchUpSession  // active catch-up session for current PR

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
	descVP := viewport.New(0, 0)

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
		descVP:    descVP,
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

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		h, v := appStyle.GetFrameSize()
		listW, listH := msg.Width-h, msg.Height-v-1
		m.prs.SetSize(listW, listH)
		m.sizeFilesView(listW, listH)
		m.diff.Width = listW
		m.diff.Height = listH
		m.infoVP.Width = listW
		m.infoVP.Height = listH
		return m, nil

	case prsLoadedMsg:
		return m.handlePRsLoaded(msg)
	case filesLoadedMsg:
		return m.handleFilesLoaded(msg)
	case autoAdvanceMsg:
		return m.handleAutoAdvance(msg)
	case catchUpLoadedMsg:
		return m.handleCatchUpLoaded(msg)
	case diamondClassifiedMsg:
		return m.handleDiamondClassified(msg)
	case blobLoadedMsg:
		return m.handleBlobLoaded(msg)
	case prefetchDoneMsg:
		return m.handlePrefetchDone()

	case tea.KeyMsg:
		if m.view == viewDiff && m.noting {
			return m.updateDiffNotingKeys(msg)
		}
		if m.view == viewDiff {
			return m.updateDiffKeys(msg)
		}
		return m.updateListKeys(msg)
	}

	// Non-key messages (e.g. mouse, custom) — delegate to the active widget.
	return m.delegateToWidget(msg)
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
		body = m.viewPRs()
	case viewFiles:
		body = m.viewFiles()
	case viewDiff:
		body = m.viewDiff()
	}
	return appStyle.Render(body) + "\n" + lipgloss.NewStyle().Faint(true).Render(m.footer())
}

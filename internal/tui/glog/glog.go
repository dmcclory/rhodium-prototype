// Package glog is the TUI commit-log-with-review-overlay view (the "glog"
// lens). It renders a 3-level tree — commits → files → hunks — that the
// cursor walks: enter toggles a commit or file open/closed, and enter on a
// hunk drills into that commit's file diff at the hunk. The per-commit review
// badges come from rhodium/internal/glog.Rollup.
package glog

import (
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"rhodium/internal/gh"
	coreglog "rhodium/internal/glog"
	"rhodium/internal/tui/keys"
	"rhodium/internal/tui/router"
)

// nodeKind tags a row in the flattened visible-node list.
type nodeKind int

const (
	kindCommit nodeKind = iota
	kindFile
	kindHunk
)

// fileKey identifies a file within a commit, for the per-file expansion map.
type fileKey struct{ c, f int }

// node is one navigable row. ci/fi/hi index into commits → Files → Hunks; the
// unused indices are zero for shallower kinds.
type node struct {
	kind       nodeKind
	ci, fi, hi int
}

// OpenHunkMsg asks the app to open a commit-scoped file diff positioned at a
// hunk. Emitted when the cursor is on a hunk and the user hits enter.
type OpenHunkMsg struct {
	CommitSHA string
	Path      string
	HunkHash  string
}

type Model struct {
	vp      viewport.Model
	pr      *gh.PR
	commits []coreglog.CommitRollup

	expandedCommit map[int]bool     // commit index → show its files
	expandedFile   map[fileKey]bool // {commit,file} → show its hunks
	nodes          []node           // flattened visible rows; rebuilt on change
	cursor         int              // index into nodes

	width  int
	height int

	// BackRoute is where `back` (esc/h) returns — the list the PR was
	// opened from (todo or prs). Set by the app on entry.
	BackRoute router.Route
}

func New() Model {
	return Model{
		vp:             viewport.New(0, 0),
		expandedCommit: map[int]bool{},
		expandedFile:   map[fileKey]bool{},
	}
}

func (m *Model) Resize(w, h int) {
	m.vp.Width = w
	m.vp.Height = h
	m.width = w
	m.height = h
}

// SetCommits replaces the displayed commit rollups and redraws. Used by the
// app once ListPRCommits + FetchCommitFiles + Rollup have completed. Defaults
// to fully expanded — a review pass wants every commit's hunks visible at
// once; enter collapses the parts you've cleared.
func (m *Model) SetCommits(pr *gh.PR, commits []coreglog.CommitRollup) {
	m.pr = pr
	m.commits = commits
	m.expandedCommit = make(map[int]bool, len(commits))
	m.expandedFile = map[fileKey]bool{}
	for ci, c := range commits {
		m.expandedCommit[ci] = true
		for fi := range c.Files {
			m.expandedFile[fileKey{ci, fi}] = true
		}
	}
	m.cursor = 0
	m.rebuild()
}

// RefreshRollups updates the per-commit data in place — recomputed badges
// after marks changed — while preserving the cursor and expansion state. Used
// when returning to the same PR's glog (e.g. backing out of a drilled diff),
// so you land back on the node you left from. The commit/file/hunk structure
// is immutable for a PR, so the existing cursor and expansion keys stay valid.
func (m *Model) RefreshRollups(commits []coreglog.CommitRollup) {
	m.commits = commits
	m.rebuild()
}

func (m *Model) PR() *gh.PR { return m.pr }

func (m *Model) View() string { return m.vp.View() }

func (m *Model) Footer() string {
	return "glog · ↑/↓: move  enter: open/expand  g: files  esc: back"
}

// Update walks the cursor on up/down, routes other keys through this view's
// bindings + the app globals, and delegates the rest to the viewport.
func (m *Model) Update(msg tea.Msg, globals []keys.Binding) tea.Cmd {
	key, isKey := msg.(tea.KeyMsg)
	if !isKey {
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return cmd
	}
	switch key.String() {
	case "up", "k":
		m.moveCursor(-1)
		return nil
	case "down", "j":
		m.moveCursor(1)
		return nil
	}
	if cmd, matched := keys.Dispatch(key.String(), false, m.Bindings(), globals); matched {
		return cmd
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return cmd
}

// Bindings: enter opens/expands the focused node (commit/file toggle, hunk
// drills into the diff), back returns to the originating list, g toggles to
// the files lens.
func (m *Model) Bindings() []keys.Binding {
	return []keys.Binding{
		{
			Name: "open", Keys: []string{"enter", "l", "right"},
			Desc: "expand / open hunk", Group: "Navigate",
			Action: m.onEnter,
		},
		{
			Name: "back", Keys: []string{"esc", "h", "left"},
			Desc: "back", Group: "Navigate",
			Action: func() tea.Cmd { return router.Navigate(m.BackRoute) },
		},
		{
			Name: "files", Keys: []string{"g"},
			Desc: "files view", Group: "View",
			Action: func() tea.Cmd { return router.Navigate(router.RouteFiles) },
		},
	}
}

// onEnter dispatches on the focused node: commit/file toggle expansion, a
// hunk emits OpenHunkMsg so the app drills into that commit's file diff.
func (m *Model) onEnter() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.nodes) {
		return nil
	}
	n := m.nodes[m.cursor]
	switch n.kind {
	case kindCommit:
		m.expandedCommit[n.ci] = !m.expandedCommit[n.ci]
		m.rebuild()
	case kindFile:
		k := fileKey{n.ci, n.fi}
		m.expandedFile[k] = !m.expandedFile[k]
		m.rebuild()
	case kindHunk:
		c := m.commits[n.ci]
		f := c.Files[n.fi]
		h := f.Hunks[n.hi]
		return func() tea.Msg {
			return OpenHunkMsg{CommitSHA: c.Commit.SHA, Path: f.Path, HunkHash: h.Hash}
		}
	}
	return nil
}

func (m *Model) moveCursor(delta int) {
	if len(m.nodes) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.nodes) {
		m.cursor = len(m.nodes) - 1
	}
	m.redraw()
}

// rebuild recomputes the visible-node list after an expansion change, clamps
// the cursor, and redraws.
func (m *Model) rebuild() {
	m.nodes = visibleNodes(m.commits, m.expandedCommit, m.expandedFile)
	if m.cursor >= len(m.nodes) {
		m.cursor = len(m.nodes) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.redraw()
}

func (m *Model) redraw() {
	m.vp.SetContent(renderTree(m.pr, m.commits, m.nodes, m.cursor, m.expandedCommit, m.expandedFile))
}

// visibleNodes flattens commits → files → hunks into the navigable rows,
// honoring per-commit and per-file expansion state.
func visibleNodes(commits []coreglog.CommitRollup, ec map[int]bool, ef map[fileKey]bool) []node {
	var nodes []node
	for ci, c := range commits {
		nodes = append(nodes, node{kind: kindCommit, ci: ci})
		if !ec[ci] {
			continue
		}
		for fi, f := range c.Files {
			nodes = append(nodes, node{kind: kindFile, ci: ci, fi: fi})
			if !ef[fileKey{ci, fi}] {
				continue
			}
			for hi := range f.Hunks {
				nodes = append(nodes, node{kind: kindHunk, ci: ci, fi: fi, hi: hi})
			}
		}
	}
	return nodes
}

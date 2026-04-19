package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type view int

const (
	viewTodo view = iota
	viewPRs
	viewFiles
	viewDiff
)

type fileTab int

const (
	tabFiles fileTab = iota
	tabNotes
)

// --- list items ---

type prItem struct {
	pr          PR
	summary     string
	noteCount   int
	scrutinized bool
}

// todoItem is a compact row in the todo dashboard view. Only PRs with
// outstanding work (in-progress, catch-up, unseen, notes) become todoItems.
type todoItem struct {
	pr        PR
	tags      []string // in stable order: in-progress, catch-up, unseen, notes
	done      int      // catch-up progress — ignored when catch-up absent
	total     int
	remaining int // unseen hunk count — ignored when files not loaded
	notes     int
}

func (i todoItem) Title() string {
	var suffix []string
	for _, t := range i.tags {
		switch t {
		case "in-progress":
			if i.remaining > 0 {
				suffix = append(suffix, fmt.Sprintf("%d new", i.remaining))
			} else {
				suffix = append(suffix, "in progress")
			}
		case "catch-up":
			suffix = append(suffix, fmt.Sprintf("catch-up %d/%d", i.done, i.total))
		case "unseen":
			suffix = append(suffix, "unseen")
		case "notes":
			suffix = append(suffix, fmt.Sprintf("%d notes", i.notes))
		case "done":
			suffix = append(suffix, "✓ done")
		}
	}
	head := fmt.Sprintf("%s#%d  %s  @%s", i.pr.Repo, i.pr.Number, i.pr.Title, i.pr.Author)
	if len(suffix) > 0 {
		head += "  [" + strings.Join(suffix, ", ") + "]"
	}
	return head
}
func (i todoItem) Description() string { return "" }
func (i todoItem) FilterValue() string { return i.Title() }

func (i prItem) Title() string {
	head := fmt.Sprintf("%s#%d  %s  @%s", i.pr.Repo, i.pr.Number, i.pr.Title, i.pr.Author)
	if i.scrutinized {
		head = "[S] " + head
	}
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
	fc           FileChange
	status       FileStatus
	noteCount    int
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
type autoAdvanceMsg struct {
	prKey         string
	advancedFiles []string // file paths that were auto-caught-up
}
type blobLoadedMsg struct {
	content string
	err     error
}
type catchUpLoadedMsg struct {
	path  string
	files []FileChange // delta files from compare API
	err   error
}
type diamondClassifiedMsg struct {
	path    string
	class   Class
	diamond Diamond // file content at all 4 corners
	patch   string  // the catch-up patch (f1→f2 or b2→f2 depending on view)
	err     error
}

// pollTickMsg fires on a slow interval while a PR is selected, prompting the
// TUI to re-read marks/notes from the brain. Primary purpose: pick up changes
// written by an nvim running in a separate tmux pane/window.
//
// gen is a generation counter incremented on each openPR; stale ticks (from
// previous PR sessions) compare unequal and are discarded, so we never have
// more than one live tick loop.
type pollTickMsg struct{ gen int }

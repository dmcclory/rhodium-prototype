package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type view int

const (
	viewPRs view = iota
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

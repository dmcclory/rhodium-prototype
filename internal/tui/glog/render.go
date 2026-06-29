package glog

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"rhodium/internal/gh"
	coreglog "rhodium/internal/glog"
)

var (
	caretStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	markedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green  [✓]
	partialStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // yellow [~]
	shaStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	authorStyle  = lipgloss.NewStyle().Faint(true)
	focusedStyle = lipgloss.NewStyle().Reverse(true).Bold(true)
	summaryStyle = lipgloss.NewStyle().Faint(true)
	headerStyle  = lipgloss.NewStyle().Bold(true)
	statsStyle   = lipgloss.NewStyle().Faint(true)
	hunkStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
)

// badge renders the per-commit mark rollup glyph.
func badge(s coreglog.Status) string {
	switch s {
	case coreglog.StatusAll:
		return markedStyle.Render("[✓]")
	case coreglog.StatusPartial:
		return partialStyle.Render("[~]")
	default:
		return "[ ]"
	}
}

// statusTail is the right-hand summary for a commit row.
func statusTail(c coreglog.CommitRollup) string {
	switch c.Status {
	case coreglog.StatusAll:
		return "✔ reviewed"
	case coreglog.StatusPartial:
		return fmt.Sprintf("◐ %d/%d hunks", c.Marked, c.Total)
	default:
		if c.Total == 0 {
			return "" // no markable hunks (e.g. merge commit)
		}
		return "○ unreviewed"
	}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func caret(expanded bool) string {
	if expanded {
		return caretStyle.Render("▾")
	}
	return caretStyle.Render("▸")
}

func hunkMark(marked bool) string {
	if marked {
		return markedStyle.Render("[✓]")
	}
	return "[ ]"
}

// fileRoll is the per-file hunk-mark summary shown on a file row.
func fileRoll(f coreglog.FileRollup) string {
	switch {
	case f.Total == 0:
		return ""
	case f.Marked >= f.Total:
		return markedStyle.Render(fmt.Sprintf("✔ %d/%d", f.Marked, f.Total))
	case f.Marked == 0:
		return fmt.Sprintf("○ %d/%d", f.Marked, f.Total)
	default:
		return partialStyle.Render(fmt.Sprintf("◐ %d/%d", f.Marked, f.Total))
	}
}

// commitNodeLine renders a commit row (depth 0) with an expand caret.
func commitNodeLine(c coreglog.CommitRollup, expanded bool) string {
	parts := []string{badge(c.Status), shaStyle.Render(shortSHA(c.Commit.SHA)), c.Commit.Title}
	if c.Commit.Author != "" {
		parts = append(parts, authorStyle.Render(c.Commit.Author))
	}
	if tail := statusTail(c); tail != "" {
		parts = append(parts, tail)
	}
	return "  " + caret(expanded) + " " + strings.Join(parts, "  ")
}

// fileNodeLine renders a file row (depth 1) with an expand caret.
func fileNodeLine(f coreglog.FileRollup, expanded bool) string {
	line := "     " + caret(expanded) + " " + f.Path +
		statsStyle.Render(fmt.Sprintf("  +%d −%d", f.Additions, f.Deletions))
	if roll := fileRoll(f); roll != "" {
		line += "  " + roll
	}
	return line
}

// hunkNodeLine renders a hunk row (depth 2, leaf — no caret).
func hunkNodeLine(h coreglog.HunkStatus) string {
	return "         " + hunkStyle.Render(h.Header) + "  " + hunkMark(h.Marked)
}

// renderTree walks the flattened visible-node list and renders each row,
// reverse-highlighting the focused one, then appends a progress summary.
func renderTree(pr *gh.PR, commits []coreglog.CommitRollup, nodes []node, cursor int, ec map[int]bool, ef map[fileKey]bool) string {
	var b strings.Builder

	if pr != nil {
		b.WriteString(headerStyle.Render(fmt.Sprintf(" %s#%d · %q · %d commits", pr.Repo, pr.Number, pr.Title, len(commits))) + "\n\n")
	}

	for idx, n := range nodes {
		var line string
		switch n.kind {
		case kindCommit:
			line = commitNodeLine(commits[n.ci], ec[n.ci])
		case kindFile:
			line = fileNodeLine(commits[n.ci].Files[n.fi], ef[fileKey{n.ci, n.fi}])
		case kindHunk:
			line = hunkNodeLine(commits[n.ci].Files[n.fi].Hunks[n.hi])
		}
		if idx == cursor {
			line = focusedStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}

	reviewed, marked, total := 0, 0, 0
	for _, c := range commits {
		if c.Status == coreglog.StatusAll {
			reviewed++
		}
		marked += c.Marked
		total += c.Total
	}
	b.WriteString("\n" + summaryStyle.Render(fmt.Sprintf("reviewed %d/%d commits · %d/%d hunks", reviewed, len(commits), marked, total)) + "\n")
	return b.String()
}

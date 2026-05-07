// Package prrow holds the shared helpers used to render gh.PR rows in any
// list view: the column-width computation, the status badge, repo#N
// formatting, and the small string-width utilities. Sharing this package
// lets the PRs view and the Todo view render against the same grid without
// duplicating logic or coupling to each other.
package prrow

import (
	"fmt"
	"strings"

	"rhodium/internal/gh"
	"rhodium/internal/tui/styles"

	"github.com/charmbracelet/lipgloss"
)

// MaxTitleWidth caps the title column so a single long PR title doesn't
// push every other column off-screen. Enforced via TruncateDisplay on the
// render path.
const MaxTitleWidth = 60

// Cols is the shared column-width set used to align PR rows. ComputeCols
// walks every row to find each column's max width; the caller stamps the
// same Cols on every row so rendering hits a stable grid.
type Cols struct {
	AnyScrutiny bool // any row is scrutinized → reserve 4 chars at the front
	RepoNum     int
	SysStatus   int  // auto-derived status (CI, conflicts, review decision)
	RevStatus   int  // user-set review status
	Title       int
	Author      int
}

// ComputeCols walks prs and returns the widest visible string per column.
// anyScrutiny is the union of per-row scrutinized flags — the caller owns
// its own item shape and supplies this directly.
// reviewStatuses maps pr_key → user-set status (may be nil).
func ComputeCols(prs []gh.PR, anyScrutiny bool, reviewStatuses map[string]string) Cols {
	c := Cols{AnyScrutiny: anyScrutiny}
	for _, p := range prs {
		if w := lipgloss.Width(RepoNumStr(p)); w > c.RepoNum {
			c.RepoNum = w
		}
		if w := lipgloss.Width(RenderSystemStatus(p)); w > c.SysStatus {
			c.SysStatus = w
		}
		key := fmt.Sprintf("%s#%d", p.Repo, p.Number)
		if w := lipgloss.Width(RenderReviewStatus(reviewStatuses[key])); w > c.RevStatus {
			c.RevStatus = w
		}
		title := TruncateDisplay(p.Title, MaxTitleWidth)
		if w := lipgloss.Width(title); w > c.Title {
			c.Title = w
		}
		author := "@" + p.Author
		if w := lipgloss.Width(author); w > c.Author {
			c.Author = w
		}
	}
	return c
}

// RepoNumStr formats a PR as `repo#N`.
func RepoNumStr(p gh.PR) string {
	return fmt.Sprintf("%s#%d", p.Repo, p.Number)
}

var draftStyle = lipgloss.NewStyle().Faint(true)

// RenderSystemStatus produces the auto-derived status badge from a PR's
// observable state (CI, review decision, mergeability, draft). Returns ""
// when nothing noteworthy — keeps quiet rows quiet.
func RenderSystemStatus(p gh.PR) string {
	var labels []string
	switch {
	case p.IsDraft:
		labels = append(labels, draftStyle.Render("DRAFT"))
	case p.ReviewDecision == "APPROVED":
		labels = append(labels, styles.StatusApproved.Render("APPROVED"))
	case p.ReviewDecision == "CHANGES_REQUESTED":
		labels = append(labels, styles.StatusChanges.Render("CHANGES_REQ"))
	case p.ReviewDecision == "REVIEW_REQUIRED":
		labels = append(labels, styles.StatusReview.Render("REVIEW_REQ"))
	}
	var head string
	if len(labels) > 0 {
		head = "[" + strings.Join(labels, " ") + "]"
	}
	var glyphs []string
	switch p.CIStatus {
	case "SUCCESS":
		glyphs = append(glyphs, styles.StatusApproved.Render("✓"))
	case "FAILURE":
		glyphs = append(glyphs, styles.StatusChanges.Render("✗"))
	case "PENDING":
		glyphs = append(glyphs, styles.StatusReview.Render("•"))
	}
	if p.Mergeable == "CONFLICTING" {
		glyphs = append(glyphs, styles.StatusChanges.Render("⚠"))
	}
	if len(glyphs) > 0 {
		if head != "" {
			head += " "
		}
		head += strings.Join(glyphs, "")
	}
	return head
}

// RenderReviewStatus produces the user-set review status badge. Returns ""
// when no custom status is set.
func RenderReviewStatus(status string) string {
	if status == "" {
		return ""
	}
	return styles.StatusReview.Render(status)
}

// PadRight right-pads s with spaces to the given visible width.
// lipgloss.Width strips ANSI codes, so this works after styling.
func PadRight(s string, w int) string {
	pad := w - lipgloss.Width(s)
	if pad <= 0 {
		return s
	}
	return s + strings.Repeat(" ", pad)
}

// TruncateDisplay clips s to at most w visible columns, replacing the
// last char with `…` when clipping. Operates on runes so multi-byte chars
// don't split mid-byte. Distinct from the byte-oriented truncate used for
// plain-ASCII CLI output.
func TruncateDisplay(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	runes := []rune(s)
	if len(runes) <= 1 || w < 1 {
		return string(runes[:1])
	}
	cut := w - 1
	if cut > len(runes) {
		cut = len(runes)
	}
	return string(runes[:cut]) + "…"
}

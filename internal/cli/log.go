package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"rhodium/internal/brain"
	"rhodium/internal/diff"
	"rhodium/internal/gh"
)

// commitFileStatus is the per-file breakdown within a single commit:
// how many hunks that commit introduced for this file are currently
// marked in the PR-level brain. Used for --verbose output and JSON.
type commitFileStatus struct {
	Path   string `json:"path"`
	Marked int    `json:"marked"`
	Total  int    `json:"total"`
}

// commitStatus is one commit's aggregate review state for rhodium log.
// Marked / Total are sums across Files. The caveat lives here: a commit
// whose hunks were rewritten by later commits in the same PR will show
// Marked=0 even if the net effect has been reviewed — marks are keyed
// on +/- content hash, so the rewritten version hashes differently from
// the original. See 2026-04-21-brain-events-log.md for the longer
// discussion of why we accept this.
type commitStatus struct {
	SHA     string             `json:"sha"`
	Title   string             `json:"title"`
	Author  string             `json:"author"`
	Date    string             `json:"date"`
	Message string             `json:"message,omitempty"`
	Marked  int                `json:"marked"`
	Total   int                `json:"total"`
	Files   []commitFileStatus `json:"files"`
}

// overlayCommitStatus is the pure core of rhodium log: given the files a
// commit introduced and the PR's current marks map (path → set of marked
// hunk hashes), return the commit's review aggregate. Kept free of
// network / DB so it can be exercised in unit tests.
//
// Marks are matched by the same +/- content hash the brain uses. Hunks
// whose content no longer matches a final-PR hunk (e.g. the commit was
// later rewritten) simply don't intersect with any mark — such a commit
// will read as 0/N even if its net effect was reviewed through a
// different hunk further down the history. That's the documented
// approximation; it's the right tradeoff because any more precise
// answer requires commit-SHA-keyed state that breaks under rebase.
func overlayCommitStatus(c gh.Commit, files []gh.FileChange, marksByPath map[string]map[string]bool) commitStatus {
	out := commitStatus{
		SHA:     c.SHA,
		Title:   c.Title,
		Author:  c.Author,
		Date:    c.Date,
		Message: c.Message,
	}
	for _, f := range files {
		hunks := diff.ParseHunks(f.Patch)
		if len(hunks) == 0 {
			continue
		}
		fileMarks := marksByPath[f.Path]
		marked := 0
		for _, h := range hunks {
			if fileMarks[h.Hash] {
				marked++
			}
		}
		out.Files = append(out.Files, commitFileStatus{
			Path: f.Path, Marked: marked, Total: len(hunks),
		})
		out.Marked += marked
		out.Total += len(hunks)
	}
	return out
}

// commitStatusGlyph picks the familiar ✓/◐/blank glyph for a commit's
// aggregate status, matching FileStatus.Glyph() so log lines visually
// line up with todo / state output.
func commitStatusGlyph(s commitStatus) string {
	if s.Total == 0 {
		// Merge commits and empty patches — nothing reviewable; leave blank.
		return " "
	}
	switch {
	case s.Marked == 0:
		return " "
	case s.Marked == s.Total:
		return "✓"
	default:
		return "◐"
	}
}

// cmdLog prints the PR's commits with per-commit review overlay.
// Shells out to gh for commit + file data, joins against hunk_marks
// via overlayCommitStatus, and renders either a tab-aligned table
// (default), a verbose form with per-file lines, or JSON.
func cmdLog(args []string) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	verbose := fs.Bool("verbose", false, "show per-file breakdown under each commit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) != 1 {
		return fmt.Errorf("usage: rhodium log <owner/repo#N>")
	}
	repo, num, err := parsePRRef(pos[0])
	if err != nil {
		return err
	}

	b, err := brain.LoadBrain()
	if err != nil {
		return err
	}
	defer b.Close()

	commits, err := gh.ListPRCommits(repo, num)
	if err != nil {
		return err
	}
	if len(commits) == 0 {
		fmt.Println("log: no commits")
		return nil
	}

	// Build the PR-level marks map once, keyed by path. overlayCommitStatus
	// reads this without a brain round-trip per commit.
	prFiles, err := gh.ListPRFiles(repo, num)
	if err != nil {
		return err
	}
	marksByPath := make(map[string]map[string]bool, len(prFiles))
	for _, f := range prFiles {
		marksByPath[f.Path] = b.HunkMarks(repo, num, f.Path)
	}

	// Fetch per-commit files in input order, overlay each. A parallel fan-out
	// is the obvious optimization but this is a CLI surface — keep it simple
	// until someone complains.
	statuses := make([]commitStatus, 0, len(commits))
	for _, c := range commits {
		files, err := gh.FetchCommitFiles(repo, c.SHA)
		if err != nil {
			return err
		}
		statuses = append(statuses, overlayCommitStatus(c, files, marksByPath))
	}

	// Newest first — GitHub returns oldest first, reverse for git-log parity.
	for i, j := 0, len(statuses)-1; i < j; i, j = i+1, j-1 {
		statuses[i], statuses[j] = statuses[j], statuses[i]
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Key     string         `json:"key"`
			Commits []commitStatus `json:"commits"`
		}{Key: brain.PRKey(repo, num), Commits: statuses})
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, s := range statuses {
		ratio := fmt.Sprintf("%d/%d", s.Marked, s.Total)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			shortSHA(s.SHA),
			commitStatusGlyph(s),
			ratio,
			truncate(s.Title, 50),
			s.Author,
			humanizeTime(s.Date),
		)
		if *verbose {
			for _, f := range s.Files {
				gl := " "
				switch {
				case f.Total == 0 || f.Marked == 0:
					gl = " "
				case f.Marked == f.Total:
					gl = "✓"
				default:
					gl = "◐"
				}
				fmt.Fprintf(tw, "\t\t\t    %s  %d/%d\t%s\t\n", gl, f.Marked, f.Total, f.Path)
			}
		}
	}
	return tw.Flush()
}

// humanizeTime formats an ISO8601 timestamp as a coarse relative string
// for list views. Falls back to the input when parsing fails — CLI
// output should never lose information just because a date field is
// unfamiliar. Thresholds are loose on purpose: these are glance-values,
// not accurate ones. Anything older than a month reverts to YYYY-MM-DD.
func humanizeTime(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

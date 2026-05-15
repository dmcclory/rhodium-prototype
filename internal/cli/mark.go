package cli

import (
	"fmt"

	"rhodium/internal/brain"
)

// cmdMark flips a single hunk mark on (on=true) or off (on=false).
func cmdMark(args []string, on bool) error {
	verb := "mark"
	if !on {
		verb = "unmark"
	}
	_, pos := splitFlags(args)
	if len(pos) != 3 {
		return fmt.Errorf("usage: rhodium %s <owner/repo#N> <file> <hunk-hash>", verb)
	}
	repo, num, err := parsePRRef(pos[0])
	if err != nil {
		return err
	}
	path, hash := pos[1], pos[2]

	b, err := brain.OpenForCLI()
	if err != nil {
		return err
	}
	defer b.Close()

	marks := b.HunkMarks(repo, num, path)
	if on {
		// CLI doesn't have patch context, so we store a sentinel line count
		// of 1. Session line tracking will pick up the real count once the
		// TUI saves marks (which computes from hunk body lines).
		marks[hash] = 1
	} else {
		delete(marks, hash)
	}
	if err := b.SetHunkMarks(repo, num, path, marks); err != nil {
		return err
	}

	// Record the head/base SHAs the reviewer is looking at, so catch-up works
	// consistently whether the mark came from the TUI or nvim.
	for _, p := range b.CachedPRs() {
		if p.Repo == repo && p.Number == num {
			_ = b.SetFileReviewed(repo, num, path, p.HeadSHA, p.BaseSHA, brain.MarkUser)
			break
		}
	}
	return nil
}

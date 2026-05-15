package cli

import (
	"fmt"

	"rhodium/internal/brain"
	"rhodium/internal/gh"
)

// cmdMarkFullyReviewed advances the brain to the current head for every
// file in the PR. The reviewer is declaring "I'm done" — no catch-up
// session is created for skipped files.
func cmdMarkFullyReviewed(args []string) error {
	_, pos := splitFlags(args)
	if len(pos) != 1 {
		return fmt.Errorf("usage: rhodium mark-fully-reviewed <owner/repo#N>")
	}
	repo, num, err := parsePRRef(pos[0])
	if err != nil {
		return err
	}

	b, err := brain.OpenForCLI()
	if err != nil {
		return err
	}
	defer b.Close()

	// Grab goal SHAs from the cached PR entry.
	var goalHead, goalBase string
	for _, p := range b.CachedPRs() {
		if p.Repo == repo && p.Number == num {
			goalHead = p.HeadSHA
			goalBase = p.BaseSHA
			break
		}
	}
	if goalHead == "" {
		return fmt.Errorf("PR %s#%d not in brain cache — run `rhodium todo --sync` first", repo, num)
	}

	files, err := gh.ListPRFiles(repo, num)
	if err != nil {
		return fmt.Errorf("fetching PR files: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("PR %s#%d has no changed files", repo, num)
	}

	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}

	if err := b.MarkFullyReviewed(repo, num, goalHead, goalBase, paths); err != nil {
		return err
	}

	fmt.Printf("marked %s#%d reviewed — %d files\n", repo, num, len(paths))
	return nil
}

package cli

import (
	"fmt"

	"rhodium/internal/brain"
)

// cmdBrainClear drops all hunk marks, file reviews, and review session data
// for a PR. Notes are preserved. Useful when a PR was reviewed at the wrong
// SHA or the reviewer wants a fresh start.
func cmdBrainClear(args []string) error {
	_, pos := splitFlags(args)
	if len(pos) != 1 {
		return fmt.Errorf("usage: rhodium brain clear <owner/repo#N>")
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

	key := brain.PRKey(repo, num)
	affected, err := b.ClearPR(key)
	if err != nil {
		return fmt.Errorf("clear %s: %w", key, err)
	}
	fmt.Printf("cleared %s: %d rows removed (hunk marks, file reviews, sessions)\n", key, affected)
	return nil
}

// cmdBrainForget drops hunk marks and the file-review entry for one file
// within a PR. Notes on that file are preserved. Useful for misclassified
// marks or files that shouldn't have been advanced.
func cmdBrainForget(args []string) error {
	_, pos := splitFlags(args)
	if len(pos) != 2 {
		return fmt.Errorf("usage: rhodium brain forget <owner/repo#N> <path>")
	}
	repo, num, err := parsePRRef(pos[0])
	if err != nil {
		return err
	}
	path := pos[1]

	b, err := brain.LoadBrain()
	if err != nil {
		return err
	}
	defer b.Close()

	key := brain.PRKey(repo, num)
	affected, err := b.ForgetFile(key, path)
	if err != nil {
		return fmt.Errorf("forget %s %s: %w", key, path, err)
	}
	fmt.Printf("forgot %s %s: %d rows removed\n", key, path, affected)
	return nil
}

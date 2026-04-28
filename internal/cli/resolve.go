package cli

import (
	"fmt"
	"strconv"

	"rhodium/internal/brain"
)

// cmdResolve marks one or more notes as resolved by ID.
func cmdResolve(args []string) error {
	_, pos := splitFlags(args)
	if len(pos) == 0 {
		return fmt.Errorf("usage: rhodium resolve <note-id>...")
	}
	b, err := brain.LoadBrain()
	if err != nil {
		return err
	}
	defer b.Close()

	for _, s := range pos {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("note id must be an integer: %q", s)
		}
		if err := b.ResolveNote(id); err != nil {
			return fmt.Errorf("resolve #%d: %w", id, err)
		}
		fmt.Printf("resolved #%d\n", id)
	}
	return nil
}

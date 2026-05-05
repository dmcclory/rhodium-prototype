package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"rhodium/internal/brain"
)

// cmdNotes prints notes for a single PR.
func cmdNotes(args []string) error {
	flags, pos := splitFlags(args)
	fs := flag.NewFlagSet("notes", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	all := fs.Bool("all", false, "include resolved notes")
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: rhodium notes <owner/repo#N>")
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

	filter := brain.NotesActive
	if *all {
		filter = brain.NotesAll
	}
	notes := b.NotesForPR(repo, num, filter)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(notes)
	}

	if len(notes) == 0 {
		fmt.Printf("%s — no notes\n", brain.PRKey(repo, num))
		return nil
	}
	fmt.Printf("%s — %d %s\n\n", brain.PRKey(repo, num), len(notes), pluralize("note", len(notes)))
	var curPath string
	for _, n := range notes {
		if n.Path != curPath {
			if curPath != "" {
				fmt.Println()
			}
			fmt.Println(n.Path)
			curPath = n.Path
		}
		marker := ""
		if n.ResolvedAt != "" {
			marker = " ✓ resolved " + n.ResolvedAt
		}
		urg := urgencyGlyph(n)
		assignee := ""
		if n.Assignee != "" {
			assignee = " [" + n.Assignee + "]"
		}
		fmt.Printf("  [#%d] %sline %d  (%s)%s%s\n", n.ID, urg, n.LineNo, n.CreatedAt, marker, assignee)
		for _, bl := range strings.Split(strings.TrimRight(n.Body, "\n"), "\n") {
			fmt.Printf("    %s\n", bl)
		}
	}
	return nil
}

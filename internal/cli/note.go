package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"rhodium/internal/brain"
	"rhodium/internal/diff"
	"rhodium/internal/gh"
)

// cmdNote saves a note for a specific line. Body read from the positional arg,
// or from stdin when body == "-". Line hash is computed here from the file
// content at that line, so nvim doesn't need to duplicate the hashing.
func cmdNote(args []string) error {
	flags, pos := splitFlags(args, "urgency", "assignee")
	fs := flag.NewFlagSet("note", flag.ContinueOnError)
	urgencyStr := fs.String("urgency", "", "urgency: now, soon, someday")
	assignee := fs.String("assignee", "", "assignee (e.g. @username)")
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if len(pos) != 4 {
		return fmt.Errorf("usage: rhodium note <owner/repo#N> <file> <line> <body|->")
	}
	repo, num, err := parsePRRef(pos[0])
	if err != nil {
		return err
	}
	path := pos[1]
	lineNo, err := strconv.Atoi(pos[2])
	if err != nil {
		return fmt.Errorf("line must be an integer: %w", err)
	}
	body := pos[3]
	if body == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		body = strings.TrimRight(string(data), "\n")
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("empty note body")
	}

	b, err := brain.LoadBrain()
	if err != nil {
		return err
	}
	defer b.Close()

	// Compute line hash from the file at head. If we can't fetch (e.g. offline,
	// new file), fall back to an empty hash — note is still anchored by line
	// number and the drift detector will warn later.
	var lineHash string
	for _, p := range b.CachedPRs() {
		if p.Repo == repo && p.Number == num {
			if content, err := gh.FetchFileAtRef(repo, path, p.HeadSHA); err == nil && content != "" {
				lines := strings.Split(content, "\n")
				if lineNo >= 1 && lineNo <= len(lines) {
					lineHash = hashLine(lines[lineNo-1])
				}
			}
			break
		}
	}
	// Parse urgency flag.
	var urgency brain.Urgency
	if *urgencyStr != "" {
		urgency = brain.Urgency(*urgencyStr)
		if !urgency.Valid() {
			return fmt.Errorf("unknown urgency %q — use now, soon, or someday", *urgencyStr)
		}
	}

	// Save note with urgency/assignee if provided, otherwise use the simple path.
	if urgency != "" || *assignee != "" {
		return b.SaveNoteWithUrgency(repo, num, path, lineNo, lineHash, body, urgency, *assignee)
	}
	return b.SaveNote(repo, num, path, lineNo, lineHash, body)
}

// hashLine wraps a single string as a single-line "+"-prefixed hunk body
// and runs it through the hunk hasher. Used by the CLI surface for note
// line anchoring; the diff view has its own copy for the same purpose.
func hashLine(s string) string {
	return diff.HashHunkBody([]string{"+" + s})
}

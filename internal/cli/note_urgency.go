package cli

import (
	"fmt"

	"rhodium/internal/brain"
)

// cmdNoteSetUrgency sets or clears the urgency on a note.
// Usage: rhodium note set-urgency <id> now|soon|someday|clear
func cmdNoteSetUrgency(args []string) error {
	_, pos := splitFlags(args)
	if len(pos) != 2 {
		return fmt.Errorf("usage: rhodium note set-urgency <note-id> now|soon|someday|clear")
	}

	id, err := parseNoteID(pos[0])
	if err != nil {
		return err
	}

	var urgency brain.Urgency
	switch pos[1] {
	case "now":
		urgency = brain.UrgencyNow
	case "soon":
		urgency = brain.UrgencySoon
	case "someday":
		urgency = brain.UrgencySomeday
	case "clear":
		urgency = ""
	default:
		return fmt.Errorf("unknown urgency %q — use now, soon, someday, or clear", pos[1])
	}

	b, err := brain.LoadBrain()
	if err != nil {
		return err
	}
	defer b.Close()

	if err := b.SetNoteUrgency(id, urgency); err != nil {
		return fmt.Errorf("set urgency on #%d: %w", id, err)
	}
	if urgency == "" {
		fmt.Printf("cleared urgency on #%d\n", id)
	} else {
		fmt.Printf("set urgency on #%d to %s\n", id, urgency)
	}
	return nil
}

// cmdNoteSetAssignee sets or clears the assignee on a note.
// Usage: rhodium note set-assignee <id> <assignee>|clear
func cmdNoteSetAssignee(args []string) error {
	_, pos := splitFlags(args)
	if len(pos) < 2 || len(pos) > 3 {
		return fmt.Errorf("usage: rhodium note set-assignee <note-id> <assignee|clear>")
	}

	id, err := parseNoteID(pos[0])
	if err != nil {
		return err
	}

	var assignee string
	if pos[1] != "clear" {
		assignee = pos[1]
	}

	b, err := brain.LoadBrain()
	if err != nil {
		return err
	}
	defer b.Close()

	if err := b.SetNoteAssignee(id, assignee); err != nil {
		return fmt.Errorf("set assignee on #%d: %w", id, err)
	}
	if assignee == "" {
		fmt.Printf("cleared assignee on #%d\n", id)
	} else {
		fmt.Printf("set assignee on #%d to %s\n", id, assignee)
	}
	return nil
}

// urgencyGlyph returns a one-char display glyph for the note's urgency.
func urgencyGlyph(n brain.Note) string {
	switch n.Urgency {
	case "now":
		return "!"
	case "soon":
		return "·"
	case "someday":
		return "~"
	default:
		return " "
	}
}

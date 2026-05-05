package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"rhodium/internal/brain"
)

func newTestBrain(t *testing.T) *brain.Brain {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	b, err := brain.LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// --- note set-urgency ---

func TestCmdNoteSetUrgency(t *testing.T) {
	b := newTestBrain(t)
	defer b.Close()

	b.SaveNote("acme/web", 42, "a.go", 10, "h1", "test note", "")
	note := b.NotesForFile("acme/web", 42, "a.go")[0]

	if err := cmdNoteSetUrgency([]string{strconv.FormatInt(note.ID, 10), "now"}); err != nil {
		t.Fatal(err)
	}
	note = b.NotesForFile("acme/web", 42, "a.go")[0]
	if note.Urgency != "now" {
		t.Errorf("got %q, want now", note.Urgency)
	}

	// Change to soon.
	if err := cmdNoteSetUrgency([]string{strconv.FormatInt(note.ID, 10), "soon"}); err != nil {
		t.Fatal(err)
	}
	note = b.NotesForFile("acme/web", 42, "a.go")[0]
	if note.Urgency != "soon" {
		t.Errorf("got %q, want soon", note.Urgency)
	}

	// Clear.
	if err := cmdNoteSetUrgency([]string{strconv.FormatInt(note.ID, 10), "clear"}); err != nil {
		t.Fatal(err)
	}
	note = b.NotesForFile("acme/web", 42, "a.go")[0]
	if note.Urgency != "" {
		t.Errorf("got %q, want empty", note.Urgency)
	}

	// Invalid urgency value.
	if err := cmdNoteSetUrgency([]string{strconv.FormatInt(note.ID, 10), "yesterday"}); err == nil {
		t.Error("expected error for invalid urgency")
	}

	// Wrong arg count.
	if err := cmdNoteSetUrgency([]string{"1"}); err == nil {
		t.Error("expected error for wrong arg count")
	}
}

func TestCmdNoteSetUrgencyBadID(t *testing.T) {
	if err := cmdNoteSetUrgency([]string{"not-a-number", "now"}); err == nil {
		t.Error("expected error for non-numeric id")
	}
}

// --- note set-assignee ---

func TestCmdNoteSetAssignee(t *testing.T) {
	b := newTestBrain(t)
	defer b.Close()

	b.SaveNote("acme/web", 42, "a.go", 10, "h1", "test note", "")
	note := b.NotesForFile("acme/web", 42, "a.go")[0]

	if err := cmdNoteSetAssignee([]string{strconv.FormatInt(note.ID, 10), "@alice"}); err != nil {
		t.Fatal(err)
	}
	note = b.NotesForFile("acme/web", 42, "a.go")[0]
	if note.Assignee != "@alice" {
		t.Errorf("got %q, want @alice", note.Assignee)
	}

	// Clear.
	if err := cmdNoteSetAssignee([]string{strconv.FormatInt(note.ID, 10), "clear"}); err != nil {
		t.Fatal(err)
	}
	note = b.NotesForFile("acme/web", 42, "a.go")[0]
	if note.Assignee != "" {
		t.Errorf("got %q, want empty", note.Assignee)
	}
}

func TestCmdNoteSetAssigneeBadID(t *testing.T) {
	if err := cmdNoteSetAssignee([]string{"not-a-number", "@alice"}); err == nil {
		t.Error("expected error for non-numeric id")
	}
}

// --- note --urgency / --assignee flags ---

func TestCmdNoteWithUrgencyFlag(t *testing.T) {
	b := newTestBrain(t)
	defer b.Close()

	if err := cmdNote([]string{"--urgency", "now", "--assignee", "@bob", "acme/web#42", "a.go", "10", "test note"}); err != nil {
		t.Fatal(err)
	}
	note := b.NotesForFile("acme/web", 42, "a.go")[0]
	if note.Urgency != "now" {
		t.Errorf("urgency: got %q, want now", note.Urgency)
	}
	if note.Assignee != "@bob" {
		t.Errorf("assignee: got %q, want @bob", note.Assignee)
	}
}

func TestCmdNoteWithUrgencyFlagInvalid(t *testing.T) {
	if err := cmdNote([]string{"--urgency", "yesterday", "acme/web#42", "a.go", "10", "test"}); err == nil {
		t.Error("expected error for invalid urgency flag")
	}
}

// --- brain clear ---

func TestCmdBrainClear(t *testing.T) {
	b := newTestBrain(t)
	defer b.Close()

	b.SetHunkMarks("acme/web", 42, "a.go", map[string]bool{"h1": true})
	b.SetFileReviewed("acme/web", 42, "a.go", "head1", "base1", brain.MarkUser)
	b.SaveNote("acme/web", 42, "a.go", 10, "h1", "preserve me", "")

	if err := cmdBrainClear([]string{"acme/web#42"}); err != nil {
		t.Fatal(err)
	}

	// Marks cleared.
	if len(b.HunkMarks("acme/web", 42, "a.go")) != 0 {
		t.Error("marks should be cleared")
	}
	// File review cleared.
	if b.FileReviewedState("acme/web", 42, "a.go").HeadSHA != "" {
		t.Error("file review should be cleared")
	}
	// Notes preserved.
	notes := b.NotesForFile("acme/web", 42, "a.go")
	if len(notes) != 1 {
		t.Errorf("notes should be preserved: got %d", len(notes))
	}
}

func TestCmdBrainClearBadRef(t *testing.T) {
	if err := cmdBrainClear([]string{"bad-ref"}); err == nil {
		t.Error("expected error for bad PR ref")
	}
}

// --- brain forget ---

func TestCmdBrainForget(t *testing.T) {
	b := newTestBrain(t)
	defer b.Close()

	b.SetHunkMarks("acme/web", 42, "a.go", map[string]bool{"h1": true})
	b.SetFileReviewed("acme/web", 42, "a.go", "head1", "base1", brain.MarkUser)
	b.SetHunkMarks("acme/web", 42, "b.go", map[string]bool{"h2": true})
	b.SetFileReviewed("acme/web", 42, "b.go", "head1", "base1", brain.MarkUser)
	b.SaveNote("acme/web", 42, "a.go", 10, "h1", "preserve me", "")

	if err := cmdBrainForget([]string{"acme/web#42", "a.go"}); err != nil {
		t.Fatal(err)
	}

	// a.go cleared.
	if len(b.HunkMarks("acme/web", 42, "a.go")) != 0 {
		t.Error("a.go marks should be cleared")
	}
	// b.go untouched.
	if len(b.HunkMarks("acme/web", 42, "b.go")) != 1 {
		t.Error("b.go marks should be untouched")
	}
	// Notes preserved.
	notes := b.NotesForFile("acme/web", 42, "a.go")
	if len(notes) != 1 {
		t.Errorf("notes should be preserved: got %d", len(notes))
	}
}

// --- notes output shows urgency/assignee ---

func TestCmdNotesOutputShowsUrgency(t *testing.T) {
	b := newTestBrain(t)
	defer b.Close()

	b.SaveNoteWithUrgency("acme/web", 42, "a.go", 10, "h1", "urgent!", brain.UrgencyNow, "@alice", "")
	b.SaveNote("acme/web", 42, "a.go", 20, "h2", "plain note", "")

	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_ = cmdNotes([]string{"acme/web#42"})

	w.Close()
	os.Stdout = old

	var buf [1024]byte
	n, _ := r.Read(buf[:])
	out := string(buf[:n])

	if out == "" {
		t.Fatal("got empty output")
	}
	// Check urgency glyph for the "now" note.
	if !strings.Contains(out, "!") {
		t.Errorf("output should contain '!' urgency glyph:\n%s", out)
	}
	// Check assignee.
	if !strings.Contains(out, "@alice") {
		t.Errorf("output should contain '@alice':\n%s", out)
	}
}

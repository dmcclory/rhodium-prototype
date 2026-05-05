package brain

import (
	"path/filepath"
	"testing"

	"rhodium/internal/diff"
)

// --- Urgency type ---

func TestUrgencyValid(t *testing.T) {
	for _, tc := range []struct {
		u    Urgency
		want bool
	}{
		{"", false},
		{UrgencyNow, true},
		{UrgencySoon, true},
		{UrgencySomeday, true},
		{"bogus", false},
	} {
		if got := tc.u.Valid(); got != tc.want {
			t.Errorf("Urgency(%q).Valid() = %v, want %v", tc.u, got, tc.want)
		}
	}
}

func TestUrgencyNext(t *testing.T) {
	cycle := []struct {
		in, want Urgency
	}{
		{"", UrgencyNow},
		{UrgencyNow, UrgencySoon},
		{UrgencySoon, UrgencySomeday},
		{UrgencySomeday, ""},
		{"bogus", ""}, // unknown wraps to empty
	}
	for _, tc := range cycle {
		if got := tc.in.Next(); got != tc.want {
			t.Errorf("Urgency(%q).Next() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- SaveNoteWithUrgency / round-trip ---

func TestSaveNoteWithUrgency(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if err := b.SaveNoteWithUrgency("acme/web", 42, "a.go", 10, "h1", "urgent note", UrgencyNow, "@alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveNoteWithUrgency("acme/web", 42, "a.go", 20, "h2", "someday note", UrgencySomeday, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveNote("acme/web", 42, "a.go", 30, "h3", "plain note", ""); err != nil {
		t.Fatal(err)
	}

	notes := b.NotesForFile("acme/web", 42, "a.go")
	if len(notes) != 3 {
		t.Fatalf("got %d notes, want 3", len(notes))
	}

	// First note: urgency + assignee.
	if notes[0].Urgency != "now" || notes[0].Assignee != "@alice" || notes[0].Body != "urgent note" {
		t.Errorf("note[0]: got urgency=%q assignee=%q body=%q", notes[0].Urgency, notes[0].Assignee, notes[0].Body)
	}

	// Second note: urgency, no assignee.
	if notes[1].Urgency != "someday" || notes[1].Assignee != "" {
		t.Errorf("note[1]: got urgency=%q assignee=%q", notes[1].Urgency, notes[1].Assignee)
	}

	// Third note: no urgency, no assignee (plain SaveNote).
	if notes[2].Urgency != "" || notes[2].Assignee != "" {
		t.Errorf("note[2]: got urgency=%q assignee=%q", notes[2].Urgency, notes[2].Assignee)
	}
}

func TestSaveNoteWithUrgencyJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if err := b.SaveNoteWithUrgency("acme/web", 42, "a.go", 10, "h1", "body", UrgencyNow, "@bob", ""); err != nil {
		t.Fatal(err)
	}

	notes := b.NotesForPR("acme/web", 42, NotesAll)
	if len(notes) != 1 {
		t.Fatal("expected 1 note")
	}

	// Fields are populated (not just in DB but in the struct).
	if notes[0].Urgency != "now" {
		t.Errorf("Urgency: got %q, want now", notes[0].Urgency)
	}
	if notes[0].Assignee != "@bob" {
		t.Errorf("Assignee: got %q, want @bob", notes[0].Assignee)
	}
}

// --- SetNoteUrgency / SetNoteAssignee ---

func TestSetNoteUrgency(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if err := b.SaveNote("acme/web", 42, "a.go", 10, "h1", "note", ""); err != nil {
		t.Fatal(err)
	}
	note := b.NotesForFile("acme/web", 42, "a.go")[0]

	// Set urgency.
	if err := b.SetNoteUrgency(note.ID, UrgencyNow); err != nil {
		t.Fatal(err)
	}
	note = b.NotesForFile("acme/web", 42, "a.go")[0]
	if note.Urgency != "now" {
		t.Errorf("after set: got %q, want now", note.Urgency)
	}

	// Change urgency.
	if err := b.SetNoteUrgency(note.ID, UrgencySoon); err != nil {
		t.Fatal(err)
	}
	note = b.NotesForFile("acme/web", 42, "a.go")[0]
	if note.Urgency != "soon" {
		t.Errorf("after change: got %q, want soon", note.Urgency)
	}

	// Clear urgency.
	if err := b.SetNoteUrgency(note.ID, ""); err != nil {
		t.Fatal(err)
	}
	note = b.NotesForFile("acme/web", 42, "a.go")[0]
	if note.Urgency != "" {
		t.Errorf("after clear: got %q, want empty", note.Urgency)
	}

	// Non-existent note returns error.
	if err := b.SetNoteUrgency(99999, UrgencyNow); err == nil {
		t.Error("expected error for non-existent note")
	}
}

func TestSetNoteAssignee(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if err := b.SaveNote("acme/web", 42, "a.go", 10, "h1", "note", ""); err != nil {
		t.Fatal(err)
	}
	note := b.NotesForFile("acme/web", 42, "a.go")[0]

	if err := b.SetNoteAssignee(note.ID, "@alice"); err != nil {
		t.Fatal(err)
	}
	note = b.NotesForFile("acme/web", 42, "a.go")[0]
	if note.Assignee != "@alice" {
		t.Errorf("after set: got %q, want @alice", note.Assignee)
	}

	// Clear assignee.
	if err := b.SetNoteAssignee(note.ID, ""); err != nil {
		t.Fatal(err)
	}
	note = b.NotesForFile("acme/web", 42, "a.go")[0]
	if note.Assignee != "" {
		t.Errorf("after clear: got %q, want empty", note.Assignee)
	}

	// Non-existent note returns error.
	if err := b.SetNoteAssignee(99999, "@bob"); err == nil {
		t.Error("expected error for non-existent note")
	}
}

// --- NoteCountByUrgency ---

func TestNoteCountByUrgency(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// Seed notes with different urgencies.
	b.SaveNoteWithUrgency("acme/web", 42, "a.go", 1, "h1", "n1", UrgencyNow, "", "")
	b.SaveNoteWithUrgency("acme/web", 42, "a.go", 2, "h2", "n2", UrgencyNow, "", "")
	b.SaveNoteWithUrgency("acme/web", 42, "a.go", 3, "h3", "n3", UrgencySoon, "", "")
	b.SaveNoteWithUrgency("acme/web", 42, "a.go", 4, "h4", "n4", UrgencySomeday, "", "")
	b.SaveNote("acme/web", 42, "a.go", 5, "h5", "n5", "") // untriaged

	now, soon, someday, untriaged := b.NoteCountByUrgency("acme/web", 42)
	if now != 2 {
		t.Errorf("now: got %d, want 2", now)
	}
	if soon != 1 {
		t.Errorf("soon: got %d, want 1", soon)
	}
	if someday != 1 {
		t.Errorf("someday: got %d, want 1", someday)
	}
	if untriaged != 1 {
		t.Errorf("untriaged: got %d, want 1", untriaged)
	}

	// Resolved notes should not count.
	notes := b.NotesForPR("acme/web", 42, NotesAll)
	for _, n := range notes {
		if n.Urgency == "now" && n.Body == "n1" {
			b.ResolveNote(n.ID)
			break
		}
	}
	now, _, _, _ = b.NoteCountByUrgency("acme/web", 42)
	if now != 1 {
		t.Errorf("after resolve: now=%d, want 1", now)
	}
}

// --- ClearPR ---

func TestClearPR(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// Set up state: marks, file reviews, a session, and a note.
	b.SetHunkMarks("acme/web", 42, "a.go", map[string]bool{"h1": true, "h2": true})
	b.SetFileReviewed("acme/web", 42, "a.go", "head1", "base1", MarkUser)
	b.SetFileReviewed("acme/web", 42, "b.go", "head1", "base1", MarkUser)
	b.SaveNote("acme/web", 42, "a.go", 10, "h1", "important note", "")

	files := []SessionFile{{Path: "a.go"}, {Path: "b.go"}}
	_, err = b.CreateSession("acme/web", 42, "head1", "base1", "head1", "base1", files)
	if err != nil {
		t.Fatal(err)
	}

	// Sanity: state exists.
	if len(b.HunkMarks("acme/web", 42, "a.go")) == 0 {
		t.Fatal("marks should exist before clear")
	}
	if b.FileReviewedState("acme/web", 42, "a.go").HeadSHA == "" {
		t.Fatal("file review should exist before clear")
	}
	if b.ActiveSession("acme/web", 42) == nil {
		t.Fatal("session should exist before clear")
	}

	// Clear the PR.
	affected, err := b.ClearPR("acme/web#42")
	if err != nil {
		t.Fatal(err)
	}
	if affected == 0 {
		t.Error("expected some rows removed")
	}

	// All review state gone.
	if len(b.HunkMarks("acme/web", 42, "a.go")) != 0 {
		t.Error("marks should be cleared")
	}
	if b.FileReviewedState("acme/web", 42, "a.go").HeadSHA != "" {
		t.Error("file review should be cleared")
	}
	if b.ActiveSession("acme/web", 42) != nil {
		t.Error("session should be cleared")
	}

	// Notes preserved.
	notes := b.NotesForFile("acme/web", 42, "a.go")
	if len(notes) != 1 || notes[0].Body != "important note" {
		t.Errorf("notes should be preserved: got %+v", notes)
	}

	// Different PR untouched.
	b.SetHunkMarks("acme/web", 99, "x.go", map[string]bool{"h": true})
	if len(b.HunkMarks("acme/web", 99, "x.go")) != 1 {
		t.Error("different PR marks should be untouched")
	}
}

// --- ForgetFile ---

func TestForgetFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// Set up state: marks and file review on two files.
	b.SetHunkMarks("acme/web", 42, "a.go", map[string]bool{"h1": true})
	b.SetFileReviewed("acme/web", 42, "a.go", "head1", "base1", MarkUser)
	b.SetHunkMarks("acme/web", 42, "b.go", map[string]bool{"h2": true})
	b.SetFileReviewed("acme/web", 42, "b.go", "head1", "base1", MarkUser)
	b.SaveNote("acme/web", 42, "a.go", 10, "h1", "note on a", "")

	// Forget a.go.
	affected, err := b.ForgetFile("acme/web#42", "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if affected == 0 {
		t.Error("expected some rows removed")
	}

	// a.go marks and review gone.
	if len(b.HunkMarks("acme/web", 42, "a.go")) != 0 {
		t.Error("a.go marks should be cleared")
	}
	if b.FileReviewedState("acme/web", 42, "a.go").HeadSHA != "" {
		t.Error("a.go file review should be cleared")
	}

	// b.go untouched.
	if len(b.HunkMarks("acme/web", 42, "b.go")) != 1 {
		t.Error("b.go marks should be untouched")
	}
	if b.FileReviewedState("acme/web", 42, "b.go").HeadSHA != "head1" {
		t.Error("b.go file review should be untouched")
	}

	// Notes on a.go preserved.
	notes := b.NotesForFile("acme/web", 42, "a.go")
	if len(notes) != 1 {
		t.Errorf("notes on a.go should be preserved: got %d", len(notes))
	}

	// Forgetting a non-existent file is a no-op (no error).
	affected, err = b.ForgetFile("acme/web#42", "nonexistent.go")
	if err != nil {
		t.Fatal(err)
	}
	if affected != 0 {
		t.Errorf("forgetting non-existent file: got %d affected, want 0", affected)
	}
}

func TestForgetFileSessionReset(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// Create a session with two files, both done.
	files := []SessionFile{{Path: "a.go", Done: false}, {Path: "b.go", Done: false}}
	s, err := b.CreateSession("acme/web", 42, "h1", "b1", "h1", "b1", files)
	if err != nil {
		t.Fatal(err)
	}
	b.SetSessionFileDone(s.ID, "a.go", true)
	b.SetSessionFileDone(s.ID, "b.go", true) // auto-completes

	// Session is completed.
	if b.ActiveSession("acme/web", 42) != nil {
		t.Fatal("session should be completed")
	}

	// Create a new session, mark a.go done.
	s2, _ := b.CreateSession("acme/web", 42, "h2", "b2", "h2", "b2", files)
	b.SetSessionFileDone(s2.ID, "a.go", true)

	// Forget a.go — should reset it to not-done in the active session.
	b.ForgetFile("acme/web#42", "a.go")

	active := b.ActiveSession("acme/web", 42)
	if active == nil {
		t.Fatal("session should still be active")
	}

	sfs := b.SessionFiles(s2.ID)
	for _, sf := range sfs {
		if sf.Path == "a.go" && sf.Done {
			t.Error("a.go should be not-done after forget")
		}
	}
}

// --- lineIsStale ---

func TestLineIsStale(t *testing.T) {
	// Helper to compute the hash for a line.
	lineHash := func(s string) string {
		return diff.HashHunkBody([]string{"+" + s})
	}

	// Content: three lines.
	content := "line one\nline two\nline three"

	t.Run("matching hash is not stale", func(t *testing.T) {
		n := Note{LineNo: 2, LineHash: lineHash("line two")}
		if lineIsStale(n, content) {
			t.Error("expected not stale")
		}
	})

	t.Run("changed hash is stale", func(t *testing.T) {
		n := Note{LineNo: 2, LineHash: lineHash("modified")}
		if !lineIsStale(n, content) {
			t.Error("expected stale")
		}
	})

	t.Run("line out of range is stale", func(t *testing.T) {
		n := Note{LineNo: 100, LineHash: lineHash("anything")}
		if !lineIsStale(n, content) {
			t.Error("expected stale for out-of-range line")
		}
	})

	t.Run("line number zero is stale", func(t *testing.T) {
		n := Note{LineNo: 0, LineHash: lineHash("anything")}
		if !lineIsStale(n, content) {
			t.Error("expected stale for line number 0")
		}
	})

	t.Run("empty line hash is not stale", func(t *testing.T) {
		n := Note{LineNo: 2, LineHash: ""}
		if lineIsStale(n, content) {
			t.Error("expected not stale when hash is empty")
		}
	})

	t.Run("empty content with valid line number is stale", func(t *testing.T) {
		n := Note{LineNo: 1, LineHash: lineHash("something")}
		if !lineIsStale(n, "") {
			t.Error("expected stale for empty content")
		}
	})
}

// --- base_sha persistence ---

func TestNoteBaseSHAPersist(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// Save a note with a base_sha.
	if err := b.SaveNote("acme/web", 42, "a.go", 10, "h1", "note", "abc123"); err != nil {
		t.Fatal(err)
	}

	// Verify it round-trips through NotesForFile.
	f := b.NotesForFile("acme/web", 42, "a.go")
	if len(f) != 1 {
		t.Fatalf("got %d notes, want 1", len(f))
	}
	if f[0].BaseSHA != "abc123" {
		t.Errorf("NotesForFile: base_sha=%q, want abc123", f[0].BaseSHA)
	}

	// Verify it round-trips through NotesForPR.
	p := b.NotesForPR("acme/web", 42, NotesActive)
	if len(p) != 1 {
		t.Fatalf("got %d notes, want 1", len(p))
	}
	if p[0].BaseSHA != "abc123" {
		t.Errorf("NotesForPR: base_sha=%q, want abc123", p[0].BaseSHA)
	}

	// Verify persistence across reload.
	b.Close()
	b2, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()

	r := b2.NotesForFile("acme/web", 42, "a.go")
	if len(r) != 1 || r[0].BaseSHA != "abc123" {
		t.Errorf("after reload: base_sha=%q, want abc123", r[0].BaseSHA)
	}
}

package main

import (
	"path/filepath"
	"testing"
)

const samplePatch = `@@ -1,3 +1,4 @@
 context
-old line
+new line
+extra
@@ -10,2 +11,2 @@
 more ctx
-gone
+added
`

func TestBrainHunkMarks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	fc := FileChange{Path: "src/main.go", Patch: samplePatch}
	hunks := parseHunks(samplePatch)
	if len(hunks) != 2 {
		t.Fatalf("expected 2 hunks, got %d", len(hunks))
	}

	if s := b.Status("acme/web", 42, fc); s != StatusUnseen {
		t.Errorf("fresh: got %v, want StatusUnseen", s)
	}

	// Mark only the first hunk → partial.
	marks := map[string]bool{hunks[0].Hash: true}
	if err := b.SetHunkMarks("acme/web", 42, fc.Path, marks); err != nil {
		t.Fatal(err)
	}
	if s := b.Status("acme/web", 42, fc); s != StatusPartial {
		t.Errorf("one of two: got %v, want StatusPartial", s)
	}

	// Mark both → seen.
	marks[hunks[1].Hash] = true
	if err := b.SetHunkMarks("acme/web", 42, fc.Path, marks); err != nil {
		t.Fatal(err)
	}
	if s := b.Status("acme/web", 42, fc); s != StatusSeen {
		t.Errorf("both: got %v, want StatusSeen", s)
	}

	// Stability: a patch with the same hunks in a different context (shifted line numbers)
	// should still be Seen because we hash +/- only.
	shifted := `@@ -100,3 +100,4 @@
 context
-old line
+new line
+extra
@@ -200,2 +201,2 @@
 more ctx
-gone
+added
`
	if s := b.Status("acme/web", 42, FileChange{Path: "src/main.go", Patch: shifted}); s != StatusSeen {
		t.Errorf("shifted line numbers: got %v, want StatusSeen", s)
	}

	// Reload persistence.
	b2, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if s := b2.Status("acme/web", 42, fc); s != StatusSeen {
		t.Errorf("after reload: got %v, want StatusSeen", s)
	}
}

func TestBrainPRCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if prs := b.CachedPRs(); len(prs) != 0 {
		t.Errorf("fresh cache: got %d, want 0", len(prs))
	}

	want := []PR{
		{Repo: "cli/cli", Number: 1, Title: "fix thing", Author: "alice", HeadSHA: "abc123"},
		{Repo: "charm/bubbletea", Number: 2, Title: "add feature", Author: "bob", HeadSHA: "def456"},
	}
	if err := b.SetPRCache(want); err != nil {
		t.Fatal(err)
	}

	got := b.CachedPRs()
	if len(got) != len(want) {
		t.Fatalf("cache: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Repo != want[i].Repo || got[i].Number != want[i].Number {
			t.Errorf("cache[%d]: got %s#%d, want %s#%d", i, got[i].Repo, got[i].Number, want[i].Repo, want[i].Number)
		}
	}

	// Reload persists.
	b2, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	got2 := b2.CachedPRs()
	if len(got2) != len(want) {
		t.Fatalf("reload cache: got %d, want %d", len(got2), len(want))
	}
}

func TestBrainFileReviews(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// No reviewed state initially.
	if s := b.FileReviewedState("acme/web", 42, "src/main.go"); s.HeadSHA != "" {
		t.Errorf("fresh: got head %q, want empty", s.HeadSHA)
	}

	// Record a review with head and base.
	if err := b.SetFileReviewed("acme/web", 42, "src/main.go", "abc123", "base111"); err != nil {
		t.Fatal(err)
	}
	s := b.FileReviewedState("acme/web", 42, "src/main.go")
	if s.HeadSHA != "abc123" || s.BaseSHA != "base111" {
		t.Errorf("after set: got head=%q base=%q, want abc123/base111", s.HeadSHA, s.BaseSHA)
	}

	// Update to a new head+base — should overwrite.
	if err := b.SetFileReviewed("acme/web", 42, "src/main.go", "def456", "base222"); err != nil {
		t.Fatal(err)
	}
	s = b.FileReviewedState("acme/web", 42, "src/main.go")
	if s.HeadSHA != "def456" || s.BaseSHA != "base222" {
		t.Errorf("after update: got head=%q base=%q, want def456/base222", s.HeadSHA, s.BaseSHA)
	}

	// Different file should be independent.
	if s := b.FileReviewedState("acme/web", 42, "other.go"); s.HeadSHA != "" {
		t.Errorf("different file: got head %q, want empty", s.HeadSHA)
	}

	// AllFileReviewedStates returns all entries for the PR.
	if err := b.SetFileReviewed("acme/web", 42, "other.go", "ghi789", "base333"); err != nil {
		t.Fatal(err)
	}
	states := b.AllFileReviewedStates("acme/web", 42)
	if len(states) != 2 {
		t.Fatalf("all states: got %d, want 2", len(states))
	}
	if s := states["src/main.go"]; s.HeadSHA != "def456" || s.BaseSHA != "base222" {
		t.Errorf("all states[src/main.go]: got head=%q base=%q", s.HeadSHA, s.BaseSHA)
	}
	if s := states["other.go"]; s.HeadSHA != "ghi789" || s.BaseSHA != "base333" {
		t.Errorf("all states[other.go]: got head=%q base=%q", s.HeadSHA, s.BaseSHA)
	}

	// Persists across reload.
	b2, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	s = b2.FileReviewedState("acme/web", 42, "src/main.go")
	if s.HeadSHA != "def456" || s.BaseSHA != "base222" {
		t.Errorf("after reload: got head=%q base=%q", s.HeadSHA, s.BaseSHA)
	}
}

func TestBrainScrutiny(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// Not scrutinized by default.
	if b.IsScrutinized("acme/web", 42) {
		t.Error("fresh: should not be scrutinized")
	}

	// Toggle on.
	if err := b.SetScrutiny("acme/web", 42, true); err != nil {
		t.Fatal(err)
	}
	if !b.IsScrutinized("acme/web", 42) {
		t.Error("after set: should be scrutinized")
	}

	// Idempotent.
	if err := b.SetScrutiny("acme/web", 42, true); err != nil {
		t.Fatal(err)
	}

	// Toggle off.
	if err := b.SetScrutiny("acme/web", 42, false); err != nil {
		t.Fatal(err)
	}
	if b.IsScrutinized("acme/web", 42) {
		t.Error("after unset: should not be scrutinized")
	}

	// Different PR is independent.
	b.SetScrutiny("acme/web", 99, true)
	if b.IsScrutinized("acme/web", 42) {
		t.Error("PR 42 should not be affected by PR 99")
	}
	if !b.IsScrutinized("acme/web", 99) {
		t.Error("PR 99 should be scrutinized")
	}
}

func TestBrainCatchUpSessions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// No active session initially.
	if s := b.ActiveCatchUp("acme/web", 42); s != nil {
		t.Fatal("fresh: should have no active session")
	}

	// Create a session.
	session, err := b.CreateCatchUp("acme/web", 42, "oldHead", "newHead", "oldBase", "newBase", 3)
	if err != nil {
		t.Fatal(err)
	}
	if session.FilesTotal != 3 || session.FilesDone != 0 {
		t.Errorf("new session: total=%d done=%d", session.FilesTotal, session.FilesDone)
	}

	// Should be active.
	active := b.ActiveCatchUp("acme/web", 42)
	if active == nil {
		t.Fatal("should have active session")
	}
	if active.ID != session.ID {
		t.Errorf("active session ID = %d, want %d", active.ID, session.ID)
	}

	// Advance 2 files.
	b.CatchUpAdvanceFile(session.ID)
	b.CatchUpAdvanceFile(session.ID)
	active = b.ActiveCatchUp("acme/web", 42)
	if active == nil || active.FilesDone != 2 {
		t.Fatalf("after 2 advances: got %+v", active)
	}

	// Advance the last file — should auto-complete.
	b.CatchUpAdvanceFile(session.ID)
	active = b.ActiveCatchUp("acme/web", 42)
	if active != nil {
		t.Error("after completing all files: should have no active session")
	}

	// AllActiveCatchUps should be empty.
	if all := b.AllActiveCatchUps(); len(all) != 0 {
		t.Errorf("AllActiveCatchUps: got %d, want 0", len(all))
	}

	// Create another session — completing it manually.
	s2, _ := b.CreateCatchUp("acme/web", 42, "h1", "h2", "b1", "b2", 5)
	b.CompleteCatchUp(s2.ID)
	if s := b.ActiveCatchUp("acme/web", 42); s != nil {
		t.Error("after manual complete: should have no active session")
	}

	// Creating a new session auto-completes the old one.
	b.CreateCatchUp("acme/web", 42, "a", "b", "c", "d", 2)
	b.CreateCatchUp("acme/web", 42, "b", "c", "d", "e", 3)
	all := b.AllActiveCatchUps()
	if len(all) != 1 {
		t.Fatalf("after double create: got %d active sessions, want 1", len(all))
	}
	if all[0].FilesTotal != 3 {
		t.Errorf("latest session total = %d, want 3", all[0].FilesTotal)
	}
}

func TestBrainNotes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// No notes initially.
	if notes := b.NotesForFile("acme/web", 42, "src/main.go"); len(notes) != 0 {
		t.Fatalf("fresh: got %d notes, want 0", len(notes))
	}

	// Save two notes on different lines.
	if err := b.SaveNote("acme/web", 42, "src/main.go", 10, "hash1", "first note"); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveNote("acme/web", 42, "src/main.go", 20, "hash2", "second note"); err != nil {
		t.Fatal(err)
	}

	notes := b.NotesForFile("acme/web", 42, "src/main.go")
	if len(notes) != 2 {
		t.Fatalf("after save: got %d notes, want 2", len(notes))
	}
	if notes[0].Body != "first note" || notes[0].LineNo != 10 {
		t.Errorf("note[0]: got %q on line %d", notes[0].Body, notes[0].LineNo)
	}
	if notes[1].Body != "second note" || notes[1].LineNo != 20 {
		t.Errorf("note[1]: got %q on line %d", notes[1].Body, notes[1].LineNo)
	}

	// Delete first note.
	if err := b.DeleteNote(notes[0].ID); err != nil {
		t.Fatal(err)
	}
	notes = b.NotesForFile("acme/web", 42, "src/main.go")
	if len(notes) != 1 {
		t.Fatalf("after delete: got %d notes, want 1", len(notes))
	}
	if notes[0].Body != "second note" {
		t.Errorf("remaining: got %q, want %q", notes[0].Body, "second note")
	}

	// Notes on a different file shouldn't appear.
	if notes := b.NotesForFile("acme/web", 42, "other.go"); len(notes) != 0 {
		t.Fatalf("other file: got %d notes, want 0", len(notes))
	}
}

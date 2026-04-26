package rhodium

import (
	"encoding/json"
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

func TestBrainReviewSessions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	files := []SessionFile{
		{Path: "a.go", Class: "b1_b2"},
		{Path: "b.go", Class: "conflict"},
		{Path: "c.go", Class: "b1_b2_f1"},
	}

	// No active session initially.
	if s := b.ActiveSession("acme/web", 42); s != nil {
		t.Fatal("fresh: should have no active session")
	}

	session, err := b.CreateSession("acme/web", 42, "newHead", "newBase", "newHead", "newBase", files)
	if err != nil {
		t.Fatal(err)
	}
	if session.FilesTotal != 3 || session.FilesDone != 0 {
		t.Errorf("new session: total=%d done=%d", session.FilesTotal, session.FilesDone)
	}

	active := b.ActiveSession("acme/web", 42)
	if active == nil {
		t.Fatal("should have active session")
	}
	if active.ID != session.ID {
		t.Errorf("active session ID = %d, want %d", active.ID, session.ID)
	}

	// Snapshot files survived.
	sfs := b.SessionFiles(session.ID)
	if len(sfs) != 3 || sfs[0].Path != "a.go" || sfs[1].Class != "conflict" {
		t.Errorf("session files didn't round-trip: %+v", sfs)
	}

	// Mark two files done.
	b.SetSessionFileDone(session.ID, "a.go", true)
	b.SetSessionFileDone(session.ID, "b.go", true)
	active = b.ActiveSession("acme/web", 42)
	if active == nil || active.FilesDone != 2 {
		t.Fatalf("after 2 done: got %+v", active)
	}

	// Last file done → auto-complete.
	b.SetSessionFileDone(session.ID, "c.go", true)
	if s := b.ActiveSession("acme/web", 42); s != nil {
		t.Error("after all done: should have no active session")
	}
	if all := b.AllActiveSessions(); len(all) != 0 {
		t.Errorf("AllActiveSessions: got %d, want 0", len(all))
	}

	// Create another session, complete it manually.
	s2, _ := b.CreateSession("acme/web", 42, "h1", "b1", "h1", "b1", files)
	b.CompleteSession(s2.ID)
	if s := b.ActiveSession("acme/web", 42); s != nil {
		t.Error("after manual complete: should have no active session")
	}

	// Creating a new session auto-completes the prior one.
	b.CreateSession("acme/web", 42, "h2", "b2", "h2", "b2", files[:2])
	b.CreateSession("acme/web", 42, "h3", "b3", "h3", "b3", files)
	all := b.AllActiveSessions()
	if len(all) != 1 {
		t.Fatalf("after double create: got %d active sessions, want 1", len(all))
	}
	if all[0].FilesTotal != 3 {
		t.Errorf("latest session total = %d, want 3", all[0].FilesTotal)
	}

	// Toggling done back to not-done un-completes (via SetSessionFileDone with false).
	latest := b.ActiveSession("acme/web", 42)
	b.SetSessionFileDone(latest.ID, "a.go", true)
	b.SetSessionFileDone(latest.ID, "b.go", true)
	b.SetSessionFileDone(latest.ID, "c.go", true) // auto-completes
	if s := b.ActiveSession("acme/web", 42); s != nil {
		t.Error("expected auto-complete after marking all done")
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

// eventPayload unmarshals an Event's JSON payload into a generic map for
// field-level assertions. Caller passes the Event.Payload string.
func eventPayload(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal payload %q: %v", raw, err)
	}
	return out
}

// kindsOf returns the kind strings of the given events in their original
// (newest-first) order. Handy for asserting the sequence of events a
// single mutator call produced without over-specifying payload contents.
func kindsOf(evs []Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return out
}

func TestBrainEventsMarks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	hunks := parseHunks(samplePatch)
	h0, h1 := hunks[0].Hash, hunks[1].Hash

	// First write: both hunks turning on → two mark.set, zero mark.clear.
	if err := b.SetHunkMarks("acme/web", 42, "src/main.go", map[string]bool{h0: true, h1: true}); err != nil {
		t.Fatal(err)
	}
	evs := b.RecentEvents(EventFilter{PRKey: "acme/web#42", KindPrefix: "mark."})
	if len(evs) != 2 {
		t.Fatalf("after first write: got %d mark events, want 2", len(evs))
	}
	seen := map[string]bool{}
	for _, e := range evs {
		if e.Kind != "mark.set" {
			t.Errorf("first write: got kind %q, want mark.set", e.Kind)
		}
		if e.Path != "src/main.go" {
			t.Errorf("path: got %q, want src/main.go", e.Path)
		}
		p := eventPayload(t, e.Payload)
		seen[p["hunk_hash"].(string)] = true
	}
	if !seen[h0] || !seen[h1] {
		t.Errorf("expected both hunk hashes in events, got %v", seen)
	}

	// Second write: drop h0, keep h1 → one mark.clear for h0, zero sets.
	if err := b.SetHunkMarks("acme/web", 42, "src/main.go", map[string]bool{h1: true}); err != nil {
		t.Fatal(err)
	}
	evs = b.RecentEvents(EventFilter{PRKey: "acme/web#42", KindPrefix: "mark.", Limit: 1})
	if len(evs) != 1 || evs[0].Kind != "mark.clear" {
		t.Fatalf("drop h0: got %v, want [mark.clear]", kindsOf(evs))
	}
	if p := eventPayload(t, evs[0].Payload); p["hunk_hash"] != h0 {
		t.Errorf("clear payload: got %v, want hunk_hash=%s", p, h0)
	}

	// Third write: same set → no new events (nothing toggled).
	before := len(b.RecentEvents(EventFilter{KindPrefix: "mark.", Limit: 100}))
	if err := b.SetHunkMarks("acme/web", 42, "src/main.go", map[string]bool{h1: true}); err != nil {
		t.Fatal(err)
	}
	after := len(b.RecentEvents(EventFilter{KindPrefix: "mark.", Limit: 100}))
	if after != before {
		t.Errorf("no-op write emitted events: before=%d after=%d", before, after)
	}
}

func TestBrainEventsNotes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if err := b.SaveNote("acme/web", 42, "src/main.go", 10, "hash1", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveAgentNote("acme/web", 42, "src/main.go", 20, "from agent"); err != nil {
		t.Fatal(err)
	}

	adds := b.RecentEvents(EventFilter{PRKey: "acme/web#42", KindPrefix: "note.add"})
	if len(adds) != 2 {
		t.Fatalf("expected 2 note.add events, got %d", len(adds))
	}
	// Newest first: agent note was saved second.
	agent := eventPayload(t, adds[0].Payload)
	if agent["source"] != "agent" || agent["body"] != "from agent" || agent["line_hash"] != "" {
		t.Errorf("agent note payload: %v", agent)
	}
	human := eventPayload(t, adds[1].Payload)
	if human["source"] != "human" || human["body"] != "hello" || human["line_hash"] != "hash1" {
		t.Errorf("human note payload: %v", human)
	}
	if _, ok := human["note_id"]; !ok {
		t.Error("human note_id missing from payload")
	}

	// Grab the human note's row id and resolve it.
	notes := b.NotesForFile("acme/web", 42, "src/main.go")
	var humanID int64
	for _, n := range notes {
		if n.Source == "human" {
			humanID = n.ID
		}
	}
	if err := b.ResolveNote(humanID); err != nil {
		t.Fatal(err)
	}
	ev := b.RecentEvents(EventFilter{KindPrefix: "note.resolve", Limit: 1})
	if len(ev) != 1 {
		t.Fatalf("resolve: got %d events", len(ev))
	}
	if p := eventPayload(t, ev[0].Payload); int64(p["note_id"].(float64)) != humanID {
		t.Errorf("resolve payload: got %v, want note_id=%d", p, humanID)
	}

	// Resolving again is a no-op (no new event).
	before := len(b.RecentEvents(EventFilter{KindPrefix: "note.resolve"}))
	if err := b.ResolveNote(humanID); err != nil {
		t.Fatal(err)
	}
	after := len(b.RecentEvents(EventFilter{KindPrefix: "note.resolve"}))
	if after != before {
		t.Errorf("repeat resolve emitted event: before=%d after=%d", before, after)
	}

	// Delete carries the body forward so replay can resurrect the note.
	if err := b.DeleteNote(humanID); err != nil {
		t.Fatal(err)
	}
	ev = b.RecentEvents(EventFilter{KindPrefix: "note.delete", Limit: 1})
	if len(ev) != 1 {
		t.Fatalf("delete: got %d events", len(ev))
	}
	p := eventPayload(t, ev[0].Payload)
	if p["body"] != "hello" || p["source"] != "human" || p["line_hash"] != "hash1" {
		t.Errorf("delete payload missing content: %v", p)
	}
	if _, ok := p["resolved_at"]; !ok {
		t.Error("deleted note was resolved; expected resolved_at in payload")
	}

	// Deleting a missing id is a no-op (no event).
	before = len(b.RecentEvents(EventFilter{KindPrefix: "note.delete"}))
	if err := b.DeleteNote(99999); err != nil {
		t.Fatal(err)
	}
	after = len(b.RecentEvents(EventFilter{KindPrefix: "note.delete"}))
	if after != before {
		t.Errorf("delete of missing id emitted event: before=%d after=%d", before, after)
	}
}

func TestBrainEventsFileReviewsAndScrutiny(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if err := b.SetFileReviewed("acme/web", 42, "src/main.go", "abc", "base"); err != nil {
		t.Fatal(err)
	}
	ev := b.RecentEvents(EventFilter{KindPrefix: "file.reviewed", Limit: 1})
	if len(ev) != 1 {
		t.Fatalf("file.reviewed: got %d events", len(ev))
	}
	if ev[0].PRKey != "acme/web#42" || ev[0].Path != "src/main.go" {
		t.Errorf("file.reviewed scope: got pr=%q path=%q", ev[0].PRKey, ev[0].Path)
	}
	p := eventPayload(t, ev[0].Payload)
	if p["head_sha"] != "abc" || p["base_sha"] != "base" {
		t.Errorf("file.reviewed payload: %v", p)
	}

	if err := b.SetScrutiny("acme/web", 42, true); err != nil {
		t.Fatal(err)
	}
	if err := b.SetScrutiny("acme/web", 42, false); err != nil {
		t.Fatal(err)
	}
	evs := b.RecentEvents(EventFilter{PRKey: "acme/web#42", KindPrefix: "scrutiny.set"})
	if len(evs) != 2 {
		t.Fatalf("scrutiny.set: got %d events, want 2", len(evs))
	}
	// Newest first.
	if p := eventPayload(t, evs[0].Payload); p["on"] != false {
		t.Errorf("latest scrutiny: got %v, want on=false", p)
	}
	if p := eventPayload(t, evs[1].Payload); p["on"] != true {
		t.Errorf("earlier scrutiny: got %v, want on=true", p)
	}
}

func TestBrainEventsSessions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	files := []SessionFile{{Path: "a.go", Class: "x"}, {Path: "b.go", Class: "y"}}
	s, err := b.CreateSession("acme/web", 42, "h1", "b1", "h1", "b1", files)
	if err != nil {
		t.Fatal(err)
	}

	ev := b.RecentEvents(EventFilter{KindPrefix: "session.create", Limit: 1})
	if len(ev) != 1 {
		t.Fatalf("session.create: got %d events", len(ev))
	}
	p := eventPayload(t, ev[0].Payload)
	if int64(p["session_id"].(float64)) != s.ID {
		t.Errorf("session.create id: got %v, want %d", p["session_id"], s.ID)
	}
	if fs, ok := p["files"].([]any); !ok || len(fs) != 2 {
		t.Errorf("session.create files: %v", p["files"])
	}

	// Finish file a.go → one session.file.done event, no complete yet.
	if err := b.SetSessionFileDone(s.ID, "a.go", true); err != nil {
		t.Fatal(err)
	}
	dones := b.RecentEvents(EventFilter{KindPrefix: "session.file.done"})
	if len(dones) != 1 {
		t.Fatalf("after 1 done: got %d events", len(dones))
	}
	if completes := b.RecentEvents(EventFilter{KindPrefix: "session.complete"}); len(completes) != 0 {
		t.Errorf("premature complete: got %d events", len(completes))
	}

	// Finish last file → file.done + auto session.complete (both in the same tx).
	if err := b.SetSessionFileDone(s.ID, "b.go", true); err != nil {
		t.Fatal(err)
	}
	completes := b.RecentEvents(EventFilter{KindPrefix: "session.complete", Limit: 1})
	if len(completes) != 1 {
		t.Fatalf("auto-complete: got %d events, want 1", len(completes))
	}
	if p := eventPayload(t, completes[0].Payload); p["reason"] != "auto" {
		t.Errorf("auto-complete reason: got %v, want auto", p["reason"])
	}

	// Manual complete on an already-complete session emits no new event.
	before := len(b.RecentEvents(EventFilter{KindPrefix: "session.complete"}))
	if err := b.CompleteSession(s.ID); err != nil {
		t.Fatal(err)
	}
	after := len(b.RecentEvents(EventFilter{KindPrefix: "session.complete"}))
	if after != before {
		t.Errorf("CompleteSession on complete session emitted event: before=%d after=%d", before, after)
	}

	// Creating a new session while one is still live logs session.complete
	// for the superseded one plus session.create for the new one.
	s2, _ := b.CreateSession("acme/web", 42, "h2", "b2", "h2", "b2", files)
	if _, err := b.CreateSession("acme/web", 42, "h3", "b3", "h3", "b3", files); err != nil {
		t.Fatal(err)
	}
	recent := b.RecentEvents(EventFilter{KindPrefix: "session.complete", Limit: 1})
	if len(recent) != 1 {
		t.Fatalf("supersede: got %d events", len(recent))
	}
	p = eventPayload(t, recent[0].Payload)
	if p["reason"] != "superseded" || int64(p["session_id"].(float64)) != s2.ID {
		t.Errorf("supersede payload: %v, want superseded for id=%d", p, s2.ID)
	}
}

func TestBrainEventsFilter(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	b.SetScrutiny("acme/web", 1, true)
	b.SetScrutiny("acme/web", 2, true)
	b.SetFileReviewed("acme/web", 1, "a.go", "h", "b")

	// Limit honored.
	if evs := b.RecentEvents(EventFilter{Limit: 2}); len(evs) != 2 {
		t.Errorf("limit=2: got %d", len(evs))
	}

	// PRKey narrows to one PR's events.
	pr1 := b.RecentEvents(EventFilter{PRKey: "acme/web#1"})
	if len(pr1) != 2 {
		t.Fatalf("pr1: got %d events, want 2", len(pr1))
	}
	for _, e := range pr1 {
		if e.PRKey != "acme/web#1" {
			t.Errorf("pr1 leaked event: %+v", e)
		}
	}

	// KindPrefix narrows by kind family.
	scrutiny := b.RecentEvents(EventFilter{KindPrefix: "scrutiny."})
	if len(scrutiny) != 2 {
		t.Fatalf("scrutiny prefix: got %d", len(scrutiny))
	}
	for _, e := range scrutiny {
		if e.Kind != "scrutiny.set" {
			t.Errorf("scrutiny prefix leaked event: %q", e.Kind)
		}
	}

	// Reverse-chronological order.
	all := b.RecentEvents(EventFilter{})
	for i := 1; i < len(all); i++ {
		if all[i-1].ID < all[i].ID {
			t.Errorf("not reverse-chronological: ids %d then %d", all[i-1].ID, all[i].ID)
		}
	}
}

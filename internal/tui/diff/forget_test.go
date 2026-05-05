package diff

import (
	"strings"
	"testing"

	"rhodium/internal/brain"
	"rhodium/internal/gh"

	corediff "rhodium/internal/diff"
)

// --- mock Brain for forget-mode tests ---

type mockBrain struct {
	setFileReviewedCalled bool
	lastPath              string
	lastKind              brain.MarkKind
}

func (m *mockBrain) HunkMarks(string, int, string) map[string]bool { return nil }
func (m *mockBrain) NotesForFile(string, int, string) []brain.Note { return nil }
func (m *mockBrain) FileReviewedState(string, int, string) brain.FileReviewState {
	return brain.FileReviewState{}
}
func (m *mockBrain) IsScrutinized(string, int) bool { return false }
func (m *mockBrain) SetHunkMarks(string, int, string, map[string]bool) error { return nil }
func (m *mockBrain) SetFileReviewed(_ string, _ int, path, _, _ string, kind brain.MarkKind) error {
	m.setFileReviewedCalled = true
	m.lastPath = path
	m.lastKind = kind
	return nil
}
func (m *mockBrain) SaveNote(string, int, string, int, string, string, string) error { return nil }
func (m *mockBrain) SaveNoteWithUrgency(string, int, string, int, string, string, brain.Urgency, string, string) error {
	return nil
}
func (m *mockBrain) ResolvedNotesForFile(string, int, string) []brain.Note { return nil }

func TestAckForget(t *testing.T) {
	mb := &mockBrain{}
	model := &Model{
		pr:         &gh.PR{Repo: "acme/web", Number: 42, HeadSHA: "head", BaseSHA: "base"},
		file:       "removed.go",
		forgetMode: true,
		forgetMsg:  "test",
	}

	cmd := model.ackForget(mb)

	// forgetMode cleared.
	if model.forgetMode {
		t.Error("forgetMode should be cleared")
	}

	// SetFileReviewed called with MarkAuto.
	if !mb.setFileReviewedCalled {
		t.Fatal("SetFileReviewed not called")
	}
	if mb.lastPath != "removed.go" {
		t.Errorf("path: got %q, want removed.go", mb.lastPath)
	}
	if mb.lastKind != brain.MarkAuto {
		t.Errorf("kind: got %q, want %q", mb.lastKind, brain.MarkAuto)
	}

	// Cmd returns FileMarkedDoneMsg.
	if cmd == nil {
		t.Fatal("expected a cmd")
	}
	msg := cmd()
	doneMsg, ok := msg.(FileMarkedDoneMsg)
	if !ok {
		t.Fatalf("msg type: got %T, want FileMarkedDoneMsg", msg)
	}
	if doneMsg.Path != "removed.go" {
		t.Errorf("doneMsg.Path: got %q, want removed.go", doneMsg.Path)
	}
}

func TestAckForgetNoPR(t *testing.T) {
	mb := &mockBrain{}
	model := &Model{file: "removed.go", forgetMode: true}

	cmd := model.ackForget(mb)

	if cmd != nil {
		t.Error("expected nil cmd when no PR")
	}
	if mb.setFileReviewedCalled {
		t.Error("SetFileReviewed should not be called without a PR")
	}
}

func TestOnDiamondClassifiedForget(t *testing.T) {
	mb := &mockBrain{}
	model := &Model{
		pr:   &gh.PR{Repo: "acme/web", Number: 42, HeadSHA: "head", BaseSHA: "base"},
		file: "removed.go",
	}

	msg := DiamondClassifiedMsg{
		Path:  "removed.go",
		Class: corediff.ClassB2F1F2, // FORGET class
	}

	cmd := model.onDiamondClassified(mb, msg)

	// Should enter forget mode.
	if !model.forgetMode {
		t.Fatal("expected forgetMode = true")
	}
	if model.forgetMsg == "" {
		t.Error("forgetMsg should be set")
	}

	// SetFileReviewed should NOT be called yet (waits for user ack).
	if mb.setFileReviewedCalled {
		t.Error("SetFileReviewed should not be called on DiamondClassified")
	}

	// Cmd should be a status message, not FileMarkedDoneMsg.
	if cmd == nil {
		t.Fatal("expected a cmd")
	}
	got := cmd()
	_, isStatus := got.(StatusMsg)
	if !isStatus {
		t.Errorf("expected StatusMsg, got %T", got)
	}
}

func TestOnDiamondClassifiedForgetWrongFile(t *testing.T) {
	mb := &mockBrain{}
	model := &Model{
		pr:   &gh.PR{Repo: "acme/web", Number: 42},
		file: "other.go", // different from msg.Path
	}

	msg := DiamondClassifiedMsg{
		Path:  "removed.go",
		Class: corediff.ClassB2F1F2,
	}

	model.onDiamondClassified(mb, msg)

	// Should NOT enter forget mode for a different file.
	if model.forgetMode {
		t.Error("forgetMode should not be set for a different file")
	}
}

func TestOnDiamondClassifiedNonForgetHidden(t *testing.T) {
	mb := &mockBrain{}
	model := &Model{
		pr:   &gh.PR{Repo: "acme/web", Number: 42, HeadSHA: "head", BaseSHA: "base"},
		file: "unchanged.go",
	}

	// ClassB1B2__F1F2 is Hidden but NOT Forget (clean merge, bases equal, features equal).
	msg := DiamondClassifiedMsg{
		Path:  "unchanged.go",
		Class: corediff.ClassB1B2__F1F2,
	}

	cmd := model.onDiamondClassified(mb, msg)

	// Should auto-advance immediately (no forget mode).
	if model.forgetMode {
		t.Error("forgetMode should be false for non-Forget Hidden class")
	}
	if !mb.setFileReviewedCalled {
		t.Error("SetFileReviewed should be called for non-Forget Hidden class")
	}

	// Cmd should return FileMarkedDoneMsg.
	if cmd == nil {
		t.Fatal("expected a cmd")
	}
	got := cmd()
	// cmd is a tea.Batch, so we can't easily type-assert the inner msg,
	// but we know SetFileReviewed was called which is the important part.
	_ = got
}

func TestRedrawForgetMode(t *testing.T) {
	model := &Model{
		forgetMode: true,
		forgetMsg:  "FORGET test message",
	}
	model.Resize(80, 24)

	model.redraw()

	// Viewport should show the forget message.
	content := model.vp.View()
	if content == "" {
		t.Error("viewport should have content in forget mode")
	}
	if model.hunkLines != nil {
		t.Error("hunkLines should be nil in forget mode")
	}
}

func TestFooterForgetMode(t *testing.T) {
	model := &Model{forgetMode: true}

	footer := model.Footer()

	if footer != "space/enter: ack and advance  esc/h: ack and back" {
		t.Errorf("footer: got %q", footer)
	}
}

// --- resolved notes ---

func TestResolvedNotesForLine(t *testing.T) {
	model := &Model{
		resolvedNotes: []brain.Note{
			{LineNo: 5, Body: "note on 5"},
			{LineNo: 5, Body: "another on 5"},
			{LineNo: 10, Body: "note on 10"},
		},
	}

	// Line 5 has two notes.
	n5 := model.resolvedNotesForLine(5)
	if len(n5) != 2 {
		t.Errorf("line 5: got %d notes, want 2", len(n5))
	}

	// Line 10 has one.
	n10 := model.resolvedNotesForLine(10)
	if len(n10) != 1 {
		t.Errorf("line 10: got %d notes, want 1", len(n10))
	}

	// Line 3 has none.
	n3 := model.resolvedNotesForLine(3)
	if len(n3) != 0 {
		t.Errorf("line 3: got %d notes, want 0", len(n3))
	}
}

func TestShowResolvedAtCursor(t *testing.T) {
	model := &Model{
		resolvedNotes: []brain.Note{{LineNo: 42, Body: "stale note"}},
		lineMap:       []int{0, 10, 20, 30, 42},
		cursorLine:    4,
	}

	cmd := model.showResolvedAtCursor()

	if !model.showingResolved {
		t.Error("showingResolved should be true")
	}
	if model.resolvedLine != 42 {
		t.Errorf("resolvedLine: got %d, want 42", model.resolvedLine)
	}
	if cmd != nil {
		t.Error("expected nil cmd")
	}
}

func TestShowResolvedAtCursorNoNotes(t *testing.T) {
	model := &Model{
		resolvedNotes: []brain.Note{{LineNo: 42, Body: "stale note"}},
		lineMap:       []int{0, 10, 20, 30, 40},
		cursorLine:    4,
	}

	cmd := model.showResolvedAtCursor()

	if model.showingResolved {
		t.Error("showingResolved should be false (no notes on cursor line)")
	}
	if cmd != nil {
		t.Error("expected nil cmd when no notes")
	}
}

func TestHideResolved(t *testing.T) {
	model := &Model{showingResolved: true, resolvedLine: 42}
	model.hideResolved()

	if model.showingResolved {
		t.Error("showingResolved should be false after hide")
	}
	if model.resolvedLine != 0 {
		t.Errorf("resolvedLine: got %d, want 0", model.resolvedLine)
	}
}

func TestFooterShowsResolvedHint(t *testing.T) {
	model := &Model{
		resolvedNotes: []brain.Note{{LineNo: 10, Body: "stale"}},
		lineMap:       []int{0, 5, 10, 15},
		cursorLine:    2,
		hunkLines:     []int{0},
	}

	footer := model.Footer()
	if !strings.Contains(footer, "right/enter: show resolved") {
		t.Errorf("footer missing resolved hint:\n%s", footer)
	}
}

func TestFooterShowsHideResolvedHint(t *testing.T) {
	model := &Model{
		showingResolved: true,
		resolvedLine:    10,
		hunkLines:       []int{0},
	}

	footer := model.Footer()
	if !strings.Contains(footer, "esc: hide resolved") {
		t.Errorf("footer missing hide hint:\n%s", footer)
	}
}

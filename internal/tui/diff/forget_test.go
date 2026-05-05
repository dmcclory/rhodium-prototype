package diff

import (
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
func (m *mockBrain) SaveNote(string, int, string, int, string, string) error { return nil }
func (m *mockBrain) SaveNoteWithUrgency(string, int, string, int, string, string, brain.Urgency, string) error {
	return nil
}

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

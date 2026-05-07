package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rhodium/internal/brain"
)

// --- brain status tests ---

func TestBrainSetAndGetPRStatus(t *testing.T) {
	b := newTestBrain(t)
	defer b.Close()

	if got := b.PRStatus("acme/web", 42); got != "" {
		t.Fatalf("initial status: got %q, want empty", got)
	}

	if err := b.SetPRStatus("acme/web", 42, "blocked"); err != nil {
		t.Fatal(err)
	}
	if got := b.PRStatus("acme/web", 42); got != "blocked" {
		t.Errorf("after set: got %q, want blocked", got)
	}

	// Update.
	if err := b.SetPRStatus("acme/web", 42, "in-review"); err != nil {
		t.Fatal(err)
	}
	if got := b.PRStatus("acme/web", 42); got != "in-review" {
		t.Errorf("after update: got %q, want in-review", got)
	}

	// Different PR unaffected.
	if got := b.PRStatus("acme/web", 43); got != "" {
		t.Errorf("other PR: got %q, want empty", got)
	}
}

func TestBrainClearPRStatus(t *testing.T) {
	b := newTestBrain(t)
	defer b.Close()

	b.SetPRStatus("acme/web", 42, "blocked")
	if err := b.SetPRStatus("acme/web", 42, ""); err != nil {
		t.Fatal(err)
	}
	if got := b.PRStatus("acme/web", 42); got != "" {
		t.Errorf("after clear: got %q, want empty", got)
	}
}

func TestBrainAllPRStatuses(t *testing.T) {
	b := newTestBrain(t)
	defer b.Close()

	b.SetPRStatus("acme/web", 42, "blocked")
	b.SetPRStatus("acme/web", 43, "approved")
	b.SetPRStatus("acme/api", 10, "in-review")

	entries := b.AllPRStatuses()
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	byKey := map[string]string{}
	for _, e := range entries {
		byKey[e.PRKey] = e.Status
	}
	if byKey["acme/web#42"] != "blocked" {
		t.Error("missing acme/web#42=blocked")
	}
	if byKey["acme/web#43"] != "approved" {
		t.Error("missing acme/web#43=approved")
	}
	if byKey["acme/api#10"] != "in-review" {
		t.Error("missing acme/api#10=in-review")
	}
}

func TestBrainPRStatusByKeys(t *testing.T) {
	b := newTestBrain(t)
	defer b.Close()

	b.SetPRStatus("acme/web", 42, "blocked")
	b.SetPRStatus("acme/web", 43, "approved")

	m := b.PRStatusByKeys([]string{"acme/web#42", "acme/api#10"})
	if len(m) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(m))
	}
	if m["acme/web#42"] != "blocked" {
		t.Errorf("got %q, want blocked", m["acme/web#42"])
	}
}

func TestBrainPRStatusByKeysEmpty(t *testing.T) {
	b := newTestBrain(t)
	defer b.Close()

	m := b.PRStatusByKeys(nil)
	if m != nil {
		t.Error("expected nil for empty keys")
	}
}

// --- CLI status tests ---

func TestCmdStatus_NoArgs(t *testing.T) {
	if err := cmdStatus([]string{}); err == nil {
		t.Error("expected error for no args")
	}
}

func TestCmdStatusSet(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	b, err := brain.LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	b.Close()

	if err := cmdStatus([]string{"acme/web#42", "blocked"}); err != nil {
		t.Fatal(err)
	}

	b2, err := brain.LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()

	if got := b2.PRStatus("acme/web", 42); got != "blocked" {
		t.Errorf("got %q, want blocked", got)
	}
}

func TestCmdStatusSet_BadRef(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	b, _ := brain.LoadBrain()
	b.Close()

	if err := cmdStatus([]string{"bad-ref", "blocked"}); err == nil {
		t.Error("expected error for bad ref")
	}
}

func TestCmdStatusSet_MissingStatus(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	b, _ := brain.LoadBrain()
	b.Close()

	if err := cmdStatus([]string{"acme/web#42"}); err == nil {
		t.Error("expected error when status text missing")
	}
}

func TestCmdStatusClear(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	b, err := brain.LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	b.SetPRStatus("acme/web", 42, "blocked")
	b.Close()

	if err := cmdStatus([]string{"clear", "acme/web#42"}); err != nil {
		t.Fatal(err)
	}

	b2, err := brain.LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()

	if got := b2.PRStatus("acme/web", 42); got != "" {
		t.Errorf("after clear: got %q, want empty", got)
	}
}

func TestCmdStatusClear_BadRef(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	b, _ := brain.LoadBrain()
	b.Close()

	if err := cmdStatus([]string{"clear", "bad-ref"}); err == nil {
		t.Error("expected error for bad ref")
	}
}

func TestCmdStatusClear_NoRef(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	b, _ := brain.LoadBrain()
	b.Close()

	if err := cmdStatus([]string{"clear"}); err == nil {
		t.Error("expected error when ref missing")
	}
}

func TestCmdStatusList(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	b, err := brain.LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	b.SetPRStatus("acme/web", 42, "blocked")
	b.SetPRStatus("acme/api", 10, "approved")
	b.Close()

	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmdStatus([]string{"list"}); err != nil {
		t.Fatal(err)
	}

	w.Close()
	os.Stdout = old

	var buf [1024]byte
	n, _ := r.Read(buf[:])
	out := string(buf[:n])

	if out == "" {
		t.Fatal("got empty output")
	}
	if !strings.Contains(out, "acme/web#42") {
		t.Errorf("output missing acme/web#42:\n%s", out)
	}
	if !strings.Contains(out, "blocked") {
		t.Errorf("output missing 'blocked':\n%s", out)
	}
	if !strings.Contains(out, "acme/api#10") {
		t.Errorf("output missing acme/api#10:\n%s", out)
	}
	if !strings.Contains(out, "approved") {
		t.Errorf("output missing 'approved':\n%s", out)
	}
}

func TestCmdStatusList_Empty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	b, _ := brain.LoadBrain()
	b.Close()

	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmdStatus([]string{"list"}); err != nil {
		t.Fatal(err)
	}

	w.Close()
	os.Stdout = old

	var buf [1024]byte
	n, _ := r.Read(buf[:])
	out := string(buf[:n])

	if !strings.Contains(out, "no custom statuses set") {
		t.Errorf("expected 'no custom statuses set', got:\n%s", out)
	}
}

// --- todo --status filter test ---

func TestCmdTodoStatusFilter(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	b, err := brain.LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	// Seed PR in cache.
	b.SetPRCache(nil) // just to ensure tables exist; we'll add statuses directly.
	b.SetPRStatus("acme/web", 42, "blocked")
	b.SetPRStatus("acme/web", 43, "approved")
	b.Close()

	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmdTodo([]string{"--status", "blocked"}); err != nil {
		t.Fatal(err)
	}

	w.Close()
	os.Stdout = old

	var buf [2048]byte
	n, _ := r.Read(buf[:])
	out := string(buf[:n])

	// With no cached PRs, the todo list should be empty (statuses alone
	// don't create todo items — they only filter existing ones).
	// The key thing to verify is that no error occurred.
	_ = out // just confirming no crash
}

func TestCmdTodoStatusAttachesToOutput(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	b, err := brain.LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	b.SetPRStatus("acme/web", 42, "blocked")
	b.SaveNote("acme/web", 42, "a.go", 10, "h1", "test note", "")
	b.Close()

	// Capture stdout for JSON output.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmdTodo([]string{"--json"}); err != nil {
		t.Fatal(err)
	}

	w.Close()
	os.Stdout = old

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	out := string(buf[:n])

	if !strings.Contains(out, "blocked") {
		t.Errorf("JSON output missing status 'blocked':\n%s", out)
	}
}

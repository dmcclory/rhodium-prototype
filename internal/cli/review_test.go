package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes a JSON config file to dir and sets RHODIUM_CONFIG.
func writeConfig(t *testing.T, dir string, content string) {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RHODIUM_CONFIG", path)
}

// --- argument validation ---

func TestCmdReview_NoArgs(t *testing.T) {
	// No args at all → usage error.
	if err := cmdReview([]string{}); err == nil {
		t.Error("expected error for no args")
	}
}

func TestCmdReview_MissingFirstPassFlag(t *testing.T) {
	// Missing --first-pass → usage error.
	if err := cmdReview([]string{"owner/repo#42"}); err == nil {
		t.Error("expected error without --first-pass flag")
	}
}

func TestCmdReview_BadPRRef(t *testing.T) {
	// Bad PR ref → parse error.
	if err := cmdReview([]string{"--first-pass", "bad-ref"}); err == nil {
		t.Error("expected error for bad PR ref")
	}
}

func TestCmdReview_TooManyArgs(t *testing.T) {
	// Two positional args → usage error.
	if err := cmdReview([]string{"--first-pass", "owner/repo#42", "owner/repo#43"}); err == nil {
		t.Error("expected error for too many positional args")
	}
}

// --- config / agent validation ---

func TestCmdReview_NoConfig(t *testing.T) {
	// Remove any existing config env to force "no config" error.
	t.Setenv("RHODIUM_CONFIG", "")
	// Also need a brain so we get past brain loading first; set to nonexistent.
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	// Point to nonexistent config.
	t.Setenv("RHODIUM_CONFIG", filepath.Join(dir, "nonexistent.json"))

	if err := cmdReview([]string{"--first-pass", "acme/web#42"}); err == nil {
		t.Error("expected error for missing config")
	}
}

func TestCmdReview_NoAgents(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	writeConfig(t, dir, `{"repos": ["acme/web"], "agents": []}`)

	if err := cmdReview([]string{"--first-pass", "acme/web#42"}); err == nil {
		t.Error("expected error when no agents configured")
	}
}

func TestCmdReview_NoDefaultAgent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	// Agents exist but default_agent points to a nonexistent one.
	writeConfig(t, dir, `{
		"repos": ["acme/web"],
		"agents": [{"name": "claude", "command": "claude", "oneshot_args": ["-p"]}],
		"default_agent": "nonexistent"
	}`)

	if err := cmdReview([]string{"--first-pass", "acme/web#42"}); err == nil {
		t.Error("expected error when default agent not found")
	}
}

// --- action validation ---

func TestCmdReview_NoFirstPassAction(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	// User overrides actions with no first-pass action at all.
	writeConfig(t, dir, `{
		"repos": ["acme/web"],
		"agents": [{"name": "claude", "command": "claude", "oneshot_args": ["-p"]}],
		"actions": [{"key": "x", "name": "explain", "mode": "oneshot", "worktree": false, "context": "patches", "delivery": "inline-notes", "prompt_template": "explain this"}]
	}`)

	if err := cmdReview([]string{"--first-pass", "acme/web#42"}); err == nil {
		t.Error("expected error when no first-pass action configured")
	}
}

func TestCmdReview_WrongDelivery(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	// First-pass action exists but uses tmux delivery instead of inline-notes.
	writeConfig(t, dir, `{
		"repos": ["acme/web"],
		"agents": [{"name": "claude", "command": "claude", "oneshot_args": ["-p"]}],
		"actions": [{
			"key": "f",
			"name": "first-pass",
			"mode": "oneshot",
			"worktree": false,
			"context": "patches",
			"delivery": "tmux",
			"prompt_template": "review this"
		}]
	}`)

	if err := cmdReview([]string{"--first-pass", "acme/web#42"}); err == nil {
		t.Error("expected error when first-pass action uses wrong delivery")
	}
}

// --- first-pass action lookup by name fallback ---

func TestCmdReview_FirstPassActionByNameFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))
	// Action named "first-pass" but with a different key — should still match.
	writeConfig(t, dir, `{
		"repos": ["acme/web"],
		"agents": [{"name": "claude", "command": "claude", "oneshot_args": ["-p"]}],
		"actions": [{
			"key": "r",
			"name": "first-pass",
			"mode": "oneshot",
			"worktree": false,
			"context": "patches",
			"delivery": "inline-notes",
			"prompt_template": "review this"
		}]
	}`)

	// This gets past action validation and fails at gh.ListPRFiles (no gh binary
	// in test env), which is expected. We just want to confirm it didn't fail
	// with "no first-pass action configured".
	err := cmdReview([]string{"--first-pass", "acme/web#42"})
	if err == nil {
		t.Fatal("expected an error (no gh binary in test env)")
	}
	// The error should NOT be about missing action.
	if strings.Contains(err.Error(), "no first-pass action configured") {
		t.Errorf("should have found action by name fallback, got: %v", err)
	}
}

package brain

import (
	"path/filepath"
	"testing"
)

// TestOpenForCLI_NoMigration_ConcurrentWithLoadBrain verifies that
// OpenForCLI does not run migrations and can open a DB concurrently with a
// live LoadBrain handle without blocking or erroring. This is the primary
// scenario: the TUI holds a long-lived LoadBrain handle while CLI shell-outs
// (e.g. from a tmux pane / nvim) open the same DB many times.
func TestOpenForCLI_NoMigration_ConcurrentWithLoadBrain(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	tui, err := LoadBrain()
	if err != nil {
		t.Fatalf("LoadBrain: %v", err)
	}
	defer tui.Close()

	cli, err := OpenForCLI()
	if err != nil {
		t.Fatalf("OpenForCLI while TUI handle is open: %v", err)
	}
	defer cli.Close()

	// Sanity: CLI handle can read.
	if _, err := cli.db.Exec(`SELECT 1`); err != nil {
		t.Fatalf("CLI handle exec: %v", err)
	}
}

// TestOpenForCLI_SkipsAheadCheck simulates the version-skew scenario from
// the finding text: a CLI invocation against a brain whose schema is
// "ahead" of what this binary's migrations know about. LoadBrain rejects
// this (correctly — it might corrupt newer columns). OpenForCLI must
// succeed because it skips the migration path entirely.
func TestOpenForCLI_SkipsAheadCheck(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	// First, initialize the DB via LoadBrain so it has the goose schema.
	b, err := LoadBrain()
	if err != nil {
		t.Fatalf("LoadBrain initial: %v", err)
	}

	// Insert a fake "future" migration version row to fake a newer-than-this-
	// binary schema. Use 999999999 — well beyond any real migration number.
	if _, err := b.db.Exec(
		`INSERT INTO goose_db_version (version_id, is_applied, tstamp) VALUES (?, 1, datetime('now'))`,
		int64(999999999),
	); err != nil {
		t.Fatalf("seed future version: %v", err)
	}
	b.Close()

	// LoadBrain MUST reject the ahead-of-binary DB.
	if _, err := LoadBrain(); err == nil {
		t.Fatalf("LoadBrain accepted an ahead DB; want failure")
	}

	// OpenForCLI MUST succeed against the same DB.
	cli, err := OpenForCLI()
	if err != nil {
		t.Fatalf("OpenForCLI rejected an ahead DB: %v", err)
	}
	defer cli.Close()
}

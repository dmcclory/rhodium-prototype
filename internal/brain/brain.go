// Package brain owns rhodium's local sqlite-backed state: review marks,
// notes, sessions, the PR cache, and the append-only event log. The Brain
// struct holds the *sql.DB; all behavior hangs off methods on it. Migrations
// are embedded by the rhodium/migrations package and applied on LoadBrain.
package brain

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"rhodium/internal/gh"
	"rhodium/migrations"
	"strconv"
	"strings"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

type Brain struct {
	db *sql.DB
}

// PRKey is the canonical "<repo>#<number>" identifier brain uses to scope
// rows to a PR. Exported so callers building cache keys (e.g. the TUI's
// prFiles map) format them the same way.
func PRKey(repo string, number int) string {
	return fmt.Sprintf("%s#%d", repo, number)
}

func brainPath() (string, error) {
	if p := os.Getenv("RHODIUM_BRAIN"); p != "" {
		return p, nil
	}
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "rhodium", "brain.db"), nil
}

func LoadBrain() (*Brain, error) {
	path, err := brainPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("open brain db: %w", err)
	}
	if err := runMigrations(db, path); err != nil {
		db.Close()
		return nil, err
	}
	return &Brain{db: db}, nil
}

// OpenForCLI opens the brain SQLite database WITHOUT running migrations.
// CLI shell-outs use this so they don't race the live TUI's long-lived
// handle: a tmux pane running an older rhodium from $PATH against a brain
// already migrated by the TUI would otherwise trip checkBrainNotAhead and
// mid-review the nvim plugin would lose access.
//
// Production code paths that NEED migrations (the TUI startup, and the
// `brain` / `brain-clear` admin subcommands) keep calling LoadBrain.
// Everything else should use OpenForCLI — the schema is whatever the most
// recent TUI launch produced.
func OpenForCLI() (*Brain, error) {
	path, err := brainPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// busy_timeout=5000 (ms) tells SQLite to wait up to 5s for a write lock
	// instead of returning SQLITE_BUSY immediately when the TUI holds the
	// write side; CLI commands are short-lived so the wait is bounded.
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open brain db: %w", err)
	}
	return &Brain{db: db}, nil
}

func (b *Brain) Close() error {
	return b.db.Close()
}

func runMigrations(db *sql.DB, path string) error {
	if err := bootstrapPreGooseDB(db); err != nil {
		return fmt.Errorf("bootstrap brain db: %w", err)
	}
	if err := checkBrainNotAhead(db, path); err != nil {
		return err
	}
	if err := ensureHashTable(db); err != nil {
		return fmt.Errorf("prepare hash table: %w", err)
	}
	if err := checkMigrationHashes(db, path); err != nil {
		return err
	}
	if err := backupBrainIfPending(db, path); err != nil {
		return fmt.Errorf("back up brain db: %w", err)
	}
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.Up(db, "."); err != nil {
		return fmt.Errorf("migrate brain db: %w", err)
	}
	if err := recordMigrationHashes(db); err != nil {
		return fmt.Errorf("record migration hashes: %w", err)
	}
	return nil
}

// checkBrainNotAhead refuses to open a brain db whose schema version exceeds
// the newest migration this binary ships with. That typically means an older
// rhodium is being pointed at a database already upgraded by a newer one, and
// silently running against it could corrupt newer columns or tables.
func checkBrainNotAhead(db *sql.DB, path string) error {
	current, err := currentBrainVersion(db)
	if err != nil {
		return fmt.Errorf("read brain schema version: %w", err)
	}
	max, err := maxEmbeddedMigration()
	if err != nil {
		return fmt.Errorf("scan embedded migrations: %w", err)
	}
	if current > max {
		return fmt.Errorf(
			"brain db at %s is at schema version %d, but this build of rhodium only knows migrations up to version %d.\n"+
				"You are likely running an older rhodium against a database upgraded by a newer one.\n"+
				"Upgrade rhodium, or point RHODIUM_BRAIN at a different database.",
			path, current, max)
	}
	return nil
}

func currentBrainVersion(db *sql.DB) (int64, error) {
	var hasGoose int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='goose_db_version'`).Scan(&hasGoose); err != nil {
		return 0, err
	}
	if hasGoose == 0 {
		return 0, nil
	}
	var v sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&v); err != nil {
		return 0, err
	}
	if !v.Valid {
		return 0, nil
	}
	return v.Int64, nil
}

func maxEmbeddedMigration() (int64, error) {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return 0, err
	}
	var max int64
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		underscore := strings.IndexByte(name, '_')
		if underscore <= 0 {
			continue
		}
		v, err := strconv.ParseInt(name[:underscore], 10, 64)
		if err != nil {
			continue
		}
		if v > max {
			max = v
		}
	}
	return max, nil
}

// bootstrapPreGooseDB brings databases created before goose was introduced up
// to the v1 schema by applying the historical ad-hoc column additions. Once
// every table matches 00001_initial_schema, goose.Up runs as a no-op against
// CREATE TABLE IF NOT EXISTS and stamps the DB at version 1.
func bootstrapPreGooseDB(db *sql.DB) error {
	var hasNotes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='notes'`).Scan(&hasNotes); err != nil {
		return err
	}
	if hasNotes == 0 {
		return nil
	}
	var hasGoose int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='goose_db_version'`).Scan(&hasGoose); err != nil {
		return err
	}
	if hasGoose != 0 {
		return nil
	}
	patches := []struct {
		column string
		ddl    string
	}{
		{"resolved_at", `ALTER TABLE notes ADD COLUMN resolved_at TEXT`},
		{"source", `ALTER TABLE notes ADD COLUMN source TEXT NOT NULL DEFAULT 'human'`},
	}
	for _, p := range patches {
		var have int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('notes') WHERE name = ?`, p.column).Scan(&have); err != nil {
			return fmt.Errorf("inspect notes.%s: %w", p.column, err)
		}
		if have == 0 {
			if _, err := db.Exec(p.ddl); err != nil {
				return fmt.Errorf("patch notes.%s: %w", p.column, err)
			}
		}
	}
	return nil
}

// backupBrainIfPending snapshots the current DB to brain.db.bak-v{N} whenever
// goose.Up is about to change the schema. Skips fresh DBs (nothing to save)
// and skips when a same-version backup already exists (preserves the earliest
// known-good copy instead of overwriting it with a later, possibly-broken one).
func backupBrainIfPending(db *sql.DB, path string) error {
	current, err := currentBrainVersion(db)
	if err != nil {
		return err
	}
	if current == 0 {
		return nil
	}
	max, err := maxEmbeddedMigration()
	if err != nil {
		return err
	}
	if current >= max {
		return nil
	}
	backup := fmt.Sprintf("%s.bak-v%d", path, current)
	if _, err := os.Stat(backup); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := db.Exec(`VACUUM INTO ?`, backup); err != nil {
		return fmt.Errorf("vacuum into %s: %w", backup, err)
	}
	return nil
}

// ensureHashTable creates the side table that tracks the sha256 of each
// applied migration's source file. It is infra for the migration tooling
// itself, not part of the application schema, so it lives outside goose.
func ensureHashTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS brain_migration_hashes (
		version_id INTEGER PRIMARY KEY,
		sha256     TEXT NOT NULL
	)`)
	return err
}

// checkMigrationHashes refuses to start if any already-applied migration's
// file content has changed since it was applied. Catches the case where
// someone edits a migration that's already been run against a DB — goose
// would silently skip the edit, leaving schema and code divergent.
func checkMigrationHashes(db *sql.DB, path string) error {
	applied, err := appliedVersions(db)
	if err != nil {
		return err
	}
	for _, v := range applied {
		file, disk, err := migrationFileAndHash(v)
		if err != nil {
			return err
		}
		if file == "" {
			continue
		}
		var stored sql.NullString
		if err := db.QueryRow(`SELECT sha256 FROM brain_migration_hashes WHERE version_id = ?`, v).Scan(&stored); err != nil && err != sql.ErrNoRows {
			return err
		}
		if !stored.Valid {
			continue
		}
		if stored.String != disk {
			return fmt.Errorf(
				"brain db at %s was migrated with a version of %s whose content has since changed.\n"+
					"Expected sha256 %s, got %s.\n"+
					"Revert the migration file, or reset the database (a backup may exist at %s.bak-v*).",
				path, file, stored.String, disk, path)
		}
	}
	return nil
}

// recordMigrationHashes inserts hash rows for any applied migration that
// doesn't yet have one. Runs after goose.Up so newly-applied versions are
// captured, and also picks up versions that were applied before this feature
// existed (those get trusted on first sight).
func recordMigrationHashes(db *sql.DB) error {
	applied, err := appliedVersions(db)
	if err != nil {
		return err
	}
	for _, v := range applied {
		file, disk, err := migrationFileAndHash(v)
		if err != nil {
			return err
		}
		if file == "" {
			continue
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO brain_migration_hashes (version_id, sha256) VALUES (?, ?)`, v, disk); err != nil {
			return err
		}
	}
	return nil
}

func appliedVersions(db *sql.DB) ([]int64, error) {
	var hasGoose int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='goose_db_version'`).Scan(&hasGoose); err != nil {
		return nil, err
	}
	if hasGoose == 0 {
		return nil, nil
	}
	rows, err := db.Query(`SELECT DISTINCT version_id FROM goose_db_version WHERE is_applied = 1 AND version_id > 0 ORDER BY version_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// migrationFileAndHash locates the embedded migration file for a given
// version and returns its name and sha256 hex digest. Returns "" if no file
// matches (applied version with no corresponding file — unusual but not
// necessarily fatal here; other guards handle that).
func migrationFileAndHash(version int64) (string, string, error) {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return "", "", err
	}
	prefix := fmt.Sprintf("%05d_", version)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		data, err := migrations.FS.ReadFile(e.Name())
		if err != nil {
			return "", "", err
		}
		sum := sha256.Sum256(data)
		return e.Name(), hex.EncodeToString(sum[:]), nil
	}
	return "", "", nil
}

// BrainStatus is a read-only snapshot of the migration state, suitable for
// `rhodium brain status`. Produced without running any migration, so it works
// even when LoadBrain would refuse to open the DB (downgrade guard, hash
// mismatch, etc).
type BrainStatus struct {
	Path           string                 `json:"path"`
	Exists         bool                   `json:"exists"`
	CurrentVersion int64                  `json:"current_version"`
	MaxEmbedded    int64                  `json:"max_embedded"`
	EmbeddedCount  int                    `json:"embedded_count"`
	Pending        int                    `json:"pending"`
	Ahead          bool                   `json:"ahead"`
	Migrations     []BrainMigrationStatus `json:"migrations"`
	HashMismatches []BrainMigrationStatus `json:"hash_mismatches,omitempty"`
	Backups        []string               `json:"backups,omitempty"`
}

type BrainMigrationStatus struct {
	Version int64  `json:"version"`
	File    string `json:"file"`
	Pending bool   `json:"pending"`
}

// embeddedMigration is one .sql file in the embedded migrations FS.
type embeddedMigration struct {
	version int64
	file    string
}

func InspectBrain() (BrainStatus, error) {
	path, err := brainPath()
	if err != nil {
		return BrainStatus{}, err
	}
	status := BrainStatus{Path: path}

	embeds, maxEmbedded, err := scanEmbeddedMigrations()
	if err != nil {
		return status, err
	}
	status.EmbeddedCount = len(embeds)
	status.MaxEmbedded = maxEmbedded

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return status, nil
		}
		return status, err
	}
	status.Exists = true

	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return status, err
	}
	defer db.Close()

	status.CurrentVersion, err = currentBrainVersion(db)
	if err != nil {
		return status, err
	}
	versions, err := appliedVersions(db)
	if err != nil {
		return status, err
	}
	applied := map[int64]bool{}
	for _, v := range versions {
		applied[v] = true
	}

	for _, e := range embeds {
		status.Migrations = append(status.Migrations, BrainMigrationStatus{
			Version: e.version,
			File:    e.file,
			Pending: !applied[e.version],
		})
		if !applied[e.version] && e.version <= status.MaxEmbedded {
			status.Pending++
		}
	}
	status.Ahead = status.CurrentVersion > status.MaxEmbedded

	mismatches, err := migrationHashMismatches(db, versions)
	if err != nil {
		return status, err
	}
	status.HashMismatches = mismatches

	status.Backups = findBrainBackups(path)
	return status, nil
}

// scanEmbeddedMigrations enumerates the *.sql files in migrations.FS and
// returns them sorted by name plus the highest version number found.
// Files without a leading "<n>_" prefix are skipped silently.
func scanEmbeddedMigrations() ([]embeddedMigration, int64, error) {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return nil, 0, err
	}
	var embeds []embeddedMigration
	var maxEmbedded int64
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		us := strings.IndexByte(name, '_')
		if us <= 0 {
			continue
		}
		v, err := strconv.ParseInt(name[:us], 10, 64)
		if err != nil {
			continue
		}
		embeds = append(embeds, embeddedMigration{version: v, file: name})
		if v > maxEmbedded {
			maxEmbedded = v
		}
	}
	return embeds, maxEmbedded, nil
}

// migrationHashMismatches compares stored migration content hashes
// against the on-disk files for each applied version. A mismatch means
// the migration source has been edited after it was applied — a
// developer error worth flagging in `brain status` output.
//
// Returns nil mismatches (not an error) if the hash table doesn't exist
// yet — older databases predate it and have nothing to compare.
func migrationHashMismatches(db *sql.DB, versions []int64) ([]BrainMigrationStatus, error) {
	var hasHashTable int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='brain_migration_hashes'`).Scan(&hasHashTable)
	if hasHashTable == 0 {
		return nil, nil
	}
	var mismatches []BrainMigrationStatus
	for _, v := range versions {
		file, disk, err := migrationFileAndHash(v)
		if err != nil {
			return nil, err
		}
		if file == "" {
			continue
		}
		var stored sql.NullString
		if err := db.QueryRow(`SELECT sha256 FROM brain_migration_hashes WHERE version_id = ?`, v).Scan(&stored); err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if stored.Valid && stored.String != disk {
			mismatches = append(mismatches, BrainMigrationStatus{Version: v, File: file})
		}
	}
	return mismatches, nil
}

// findBrainBackups returns the .bak-v* sibling files alongside the brain
// db. Best-effort: returns nil on any read error.
func findBrainBackups(path string) []string {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var backups []string
	for _, e := range dirEntries {
		name := e.Name()
		if strings.HasPrefix(name, base+".bak-v") {
			backups = append(backups, filepath.Join(dir, name))
		}
	}
	return backups
}

func (b *Brain) CachedPRs() []gh.PR {
	rows, err := b.db.Query(`SELECT repo, number, title, author, head_sha, base_sha, body FROM pr_cache`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []gh.PR
	for rows.Next() {
		var p gh.PR
		if rows.Scan(&p.Repo, &p.Number, &p.Title, &p.Author, &p.HeadSHA, &p.BaseSHA, &p.Body) == nil {
			out = append(out, p)
		}
	}
	return out
}

func (b *Brain) SetPRCache(prs []gh.PR) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM pr_cache`); err != nil {
		return err
	}
	for _, p := range prs {
		if _, err := tx.Exec(`INSERT INTO pr_cache (repo, number, title, author, head_sha, base_sha, body) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.Repo, p.Number, p.Title, p.Author, p.HeadSHA, p.BaseSHA, p.Body); err != nil {
			return err
		}
	}
	return tx.Commit()
}

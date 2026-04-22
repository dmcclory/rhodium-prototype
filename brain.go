package main

import (
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// FileStatus is the reviewer's per-file state: none / some / all hunks marked.
type FileStatus int

const (
	StatusUnseen  FileStatus = iota // no hunks marked
	StatusPartial                   // some but not all current hunks marked
	StatusSeen                      // every current hunk is marked, or the file has no hunks
)

func (s FileStatus) Glyph() string {
	switch s {
	case StatusSeen:
		return "✓"
	case StatusPartial:
		return "◐"
	default:
		return " "
	}
}

type Brain struct {
	db *sql.DB
}

func prKey(repo string, number int) string {
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
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
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
	entries, err := migrationsFS.ReadDir("migrations")
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
	entries, err := migrationsFS.ReadDir("migrations")
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
		data, err := migrationsFS.ReadFile("migrations/" + e.Name())
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

func InspectBrain() (BrainStatus, error) {
	path, err := brainPath()
	if err != nil {
		return BrainStatus{}, err
	}
	status := BrainStatus{Path: path}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return status, err
	}
	type embedded struct {
		version int64
		file    string
	}
	var embeds []embedded
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
		embeds = append(embeds, embedded{version: v, file: name})
		if v > status.MaxEmbedded {
			status.MaxEmbedded = v
		}
	}
	status.EmbeddedCount = len(embeds)

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

	applied := map[int64]bool{}
	status.CurrentVersion, err = currentBrainVersion(db)
	if err != nil {
		return status, err
	}
	versions, err := appliedVersions(db)
	if err != nil {
		return status, err
	}
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

	var hasHashTable int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='brain_migration_hashes'`).Scan(&hasHashTable)
	if hasHashTable != 0 {
		for _, v := range versions {
			file, disk, err := migrationFileAndHash(v)
			if err != nil {
				return status, err
			}
			if file == "" {
				continue
			}
			var stored sql.NullString
			if err := db.QueryRow(`SELECT sha256 FROM brain_migration_hashes WHERE version_id = ?`, v).Scan(&stored); err != nil && err != sql.ErrNoRows {
				return status, err
			}
			if stored.Valid && stored.String != disk {
				status.HashMismatches = append(status.HashMismatches, BrainMigrationStatus{Version: v, File: file})
			}
		}
	}

	dir := filepath.Dir(path)
	base := filepath.Base(path)
	if dirEntries, err := os.ReadDir(dir); err == nil {
		for _, e := range dirEntries {
			name := e.Name()
			if strings.HasPrefix(name, base+".bak-v") {
				status.Backups = append(status.Backups, filepath.Join(dir, name))
			}
		}
	}

	return status, nil
}

func (b *Brain) Close() error {
	return b.db.Close()
}

// Event is a single row from brain_events: one mutation of brain state.
// Payload stays as raw JSON here; callers that care about the shape
// per-kind unmarshal it themselves.
type Event struct {
	ID      int64
	TS      string
	Kind    string
	PRKey   string
	Path    string
	Payload string
}

// EventFilter narrows RecentEvents. Zero-value fields are ignored, so
// an empty filter returns the most recent events across the whole brain.
// KindPrefix matches via SQL LIKE so "mark." grabs both mark.set and
// mark.clear. Limit <= 0 defaults to 100 — the log is append-only and
// can grow without bound, so callers should always page.
type EventFilter struct {
	PRKey      string
	KindPrefix string
	Limit      int
}

// execer is the shared subset of *sql.DB and *sql.Tx that logEvent needs,
// so mutators already holding a transaction can write the state change
// and its event atomically.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// logEvent appends one row to brain_events. Callers inside a transaction
// must pass that tx as x so the event shares the state write's atomicity
// — if the tx rolls back, no orphan event survives. Payload is marshalled
// to JSON; a nil payload becomes "{}".
func logEvent(x execer, kind, prKey, path string, payload any) error {
	var body string
	if payload == nil {
		body = "{}"
	} else {
		buf, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal event payload: %w", err)
		}
		body = string(buf)
	}
	_, err := x.Exec(
		`INSERT INTO brain_events (kind, pr_key, path, payload) VALUES (?, ?, ?, ?)`,
		kind, prKey, path, body)
	return err
}

// RecentEvents returns events in reverse-chronological order (newest
// first) subject to the filter. Intended for a future `rhodium brain
// log` CLI; exposed now so tests can assert the log is being populated.
func (b *Brain) RecentEvents(filter EventFilter) []Event {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	var (
		where []string
		args  []any
	)
	if filter.PRKey != "" {
		where = append(where, `pr_key = ?`)
		args = append(args, filter.PRKey)
	}
	if filter.KindPrefix != "" {
		where = append(where, `kind LIKE ?`)
		args = append(args, filter.KindPrefix+"%")
	}
	q := `SELECT id, ts, kind, pr_key, path, payload FROM brain_events`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := b.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if rows.Scan(&e.ID, &e.TS, &e.Kind, &e.PRKey, &e.Path, &e.Payload) == nil {
			out = append(out, e)
		}
	}
	return out
}

// ReviewSession is an ordered snapshot of the diff set the reviewer is
// stepping through, kept stable across a single reviewing pass even if the
// PR moves under them. Iron calls this the `Review_session`, separate from
// the brain (long-term "what do I know"). Rhodium's session is narrower:
// diff-set stability + resumability + progress tracking. Brain advancement
// (file_reviews) still happens per-file on mark-save; see
// `project_session_gate_semantics.md` for why gate-at-completion is deferred.
//
// FilesTotal / FilesDone are denormalized from review_session_files on read.
type ReviewSession struct {
	ID          int64
	PRKey       string
	HeadSHA     string
	BaseSHA     string
	GoalHead    string
	GoalBase    string
	FilesTotal  int
	FilesDone   int
	StartedAt   string
	CompletedAt string
}

// SessionFile is one row from review_session_files — a single path's
// snapshot within a session.
type SessionFile struct {
	Path  string
	Class string
	Done  bool
}

// ActiveSession returns the current session for a PR — the most recent one
// without a completed_at. Does not check whether the PR head has moved;
// callers deciding whether to resume must compare HeadSHA/BaseSHA themselves
// (mirrors Iron: a session is only reusable if the corners still match).
func (b *Brain) ActiveSession(repo string, pr int) *ReviewSession {
	key := prKey(repo, pr)
	var s ReviewSession
	err := b.db.QueryRow(
		`SELECT id, pr_key, head_sha, base_sha, goal_head, goal_base, started_at
		 FROM review_sessions WHERE pr_key = ? AND completed_at IS NULL ORDER BY id DESC LIMIT 1`, key,
	).Scan(&s.ID, &s.PRKey, &s.HeadSHA, &s.BaseSHA, &s.GoalHead, &s.GoalBase, &s.StartedAt)
	if err != nil {
		return nil
	}
	b.hydrateSessionCounts(&s)
	return &s
}

func (b *Brain) hydrateSessionCounts(s *ReviewSession) {
	b.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(done), 0) FROM review_session_files WHERE session_id = ?`, s.ID,
	).Scan(&s.FilesTotal, &s.FilesDone)
}

// CreateSession snapshots a new session for a PR with the given file list.
// Any existing active session for the PR is completed first — only one
// session is live at a time. goalHead / goalBase are recorded for a future
// gate-at-completion brain advance (currently unused; see
// project_session_gate_semantics.md).
func (b *Brain) CreateSession(repo string, pr int, headSHA, baseSHA, goalHead, goalBase string, files []SessionFile) (*ReviewSession, error) {
	key := prKey(repo, pr)
	tx, err := b.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Capture any active session we're about to supersede so we can emit a
	// session.complete event for it — replay-from-events needs to see the
	// same "creating implicitly completes prior" invariant.
	var superseded sql.NullInt64
	if err := tx.QueryRow(
		`SELECT id FROM review_sessions WHERE pr_key = ? AND completed_at IS NULL ORDER BY id DESC LIMIT 1`, key,
	).Scan(&superseded); err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE review_sessions SET completed_at = datetime('now') WHERE pr_key = ? AND completed_at IS NULL`, key); err != nil {
		return nil, err
	}
	if superseded.Valid {
		if err := logEvent(tx, "session.complete", key, "", map[string]any{"session_id": superseded.Int64, "reason": "superseded"}); err != nil {
			return nil, err
		}
	}
	res, err := tx.Exec(
		`INSERT INTO review_sessions (pr_key, head_sha, base_sha, goal_head, goal_base) VALUES (?, ?, ?, ?, ?)`,
		key, headSHA, baseSHA, goalHead, goalBase)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	for _, f := range files {
		done := 0
		if f.Done {
			done = 1
		}
		if _, err := tx.Exec(
			`INSERT INTO review_session_files (session_id, path, class, done) VALUES (?, ?, ?, ?)`,
			id, f.Path, f.Class, done); err != nil {
			return nil, err
		}
	}
	payload := map[string]any{
		"session_id": id,
		"head_sha":   headSHA,
		"base_sha":   baseSHA,
		"goal_head":  goalHead,
		"goal_base":  goalBase,
		"files":      files,
	}
	if err := logEvent(tx, "session.create", key, "", payload); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s := &ReviewSession{
		ID: id, PRKey: key,
		HeadSHA: headSHA, BaseSHA: baseSHA,
		GoalHead: goalHead, GoalBase: goalBase,
	}
	b.hydrateSessionCounts(s)
	return s, nil
}

// SetSessionFileDone marks a session-file as done (or not-done). If the
// session is fully done after this write, it is auto-completed (and the
// event log captures both the file toggle and the auto-complete).
func (b *Brain) SetSessionFileDone(sessionID int64, path string, done bool) error {
	d := 0
	if done {
		d = 1
	}
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Resolve pr_key up front so events for this session scope correctly.
	var key string
	if err := tx.QueryRow(`SELECT pr_key FROM review_sessions WHERE id = ?`, sessionID).Scan(&key); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}

	if _, err := tx.Exec(
		`UPDATE review_session_files SET done = ? WHERE session_id = ? AND path = ?`, d, sessionID, path,
	); err != nil {
		return err
	}
	if err := logEvent(tx, "session.file.done", key, path, map[string]any{"session_id": sessionID, "done": done}); err != nil {
		return err
	}
	res, err := tx.Exec(
		`UPDATE review_sessions SET completed_at = datetime('now')
		 WHERE id = ? AND completed_at IS NULL
		   AND NOT EXISTS (SELECT 1 FROM review_session_files WHERE session_id = ? AND done = 0)`,
		sessionID, sessionID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if err := logEvent(tx, "session.complete", key, "", map[string]any{"session_id": sessionID, "reason": "auto"}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CompleteSession marks a session complete regardless of its files. Used
// when the reviewer navigates away or the session is being superseded.
// Emits session.complete only when the UPDATE actually transitions the
// row from incomplete → complete, so repeated calls don't spam events.
func (b *Brain) CompleteSession(sessionID int64) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var key string
	var completed sql.NullString
	if err := tx.QueryRow(
		`SELECT pr_key, completed_at FROM review_sessions WHERE id = ?`, sessionID,
	).Scan(&key, &completed); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	if _, err := tx.Exec(`UPDATE review_sessions SET completed_at = datetime('now') WHERE id = ?`, sessionID); err != nil {
		return err
	}
	if !completed.Valid {
		if err := logEvent(tx, "session.complete", key, "", map[string]any{"session_id": sessionID, "reason": "manual"}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SessionFiles returns the files belonging to a session, in insertion order.
func (b *Brain) SessionFiles(sessionID int64) []SessionFile {
	rows, err := b.db.Query(
		`SELECT path, class, done FROM review_session_files WHERE session_id = ? ORDER BY rowid`, sessionID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []SessionFile
	for rows.Next() {
		var f SessionFile
		var done int
		if rows.Scan(&f.Path, &f.Class, &done) == nil {
			f.Done = done == 1
			out = append(out, f)
		}
	}
	return out
}

// AllActiveSessions returns every currently-active session across PRs.
func (b *Brain) AllActiveSessions() []ReviewSession {
	rows, err := b.db.Query(
		`SELECT id, pr_key, head_sha, base_sha, goal_head, goal_base, started_at
		 FROM review_sessions WHERE completed_at IS NULL ORDER BY started_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ReviewSession
	for rows.Next() {
		var s ReviewSession
		if rows.Scan(&s.ID, &s.PRKey, &s.HeadSHA, &s.BaseSHA, &s.GoalHead, &s.GoalBase, &s.StartedAt) == nil {
			b.hydrateSessionCounts(&s)
			out = append(out, s)
		}
	}
	return out
}

// IsScrutinized returns whether a PR is marked for full scrutiny.
func (b *Brain) IsScrutinized(repo string, pr int) bool {
	key := prKey(repo, pr)
	var exists bool
	b.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pr_scrutiny WHERE pr_key = ?)`, key).Scan(&exists)
	return exists
}

// SetScrutiny marks or unmarks a PR for scrutiny.
func (b *Brain) SetScrutiny(repo string, pr int, on bool) error {
	key := prKey(repo, pr)
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if on {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO pr_scrutiny (pr_key) VALUES (?)`, key); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(`DELETE FROM pr_scrutiny WHERE pr_key = ?`, key); err != nil {
			return err
		}
	}
	if err := logEvent(tx, "scrutiny.set", key, "", map[string]any{"on": on}); err != nil {
		return err
	}
	return tx.Commit()
}

func (b *Brain) HasAnyMarks(repo string, pr int) bool {
	key := prKey(repo, pr)
	var exists bool
	b.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM hunk_marks WHERE pr_key = ?)`, key).Scan(&exists)
	return exists
}

func (b *Brain) HunkMarks(repo string, pr int, path string) map[string]bool {
	key := prKey(repo, pr)
	rows, err := b.db.Query(`SELECT hunk_hash FROM hunk_marks WHERE pr_key = ? AND path = ?`, key, path)
	if err != nil {
		return map[string]bool{}
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var h string
		if rows.Scan(&h) == nil {
			out[h] = true
		}
	}
	return out
}

func (b *Brain) SetHunkMarks(repo string, pr int, path string, marks map[string]bool) error {
	key := prKey(repo, pr)
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Snapshot prior marks before the bulk replace so we can emit one event
	// per actual toggle (rather than one coarse "marks.replace"). Per-hunk
	// events make future per-hunk undo trivial.
	prior := map[string]bool{}
	rows, err := tx.Query(`SELECT hunk_hash FROM hunk_marks WHERE pr_key = ? AND path = ?`, key, path)
	if err != nil {
		return err
	}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return err
		}
		prior[h] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}

	if _, err := tx.Exec(`DELETE FROM hunk_marks WHERE pr_key = ? AND path = ?`, key, path); err != nil {
		return err
	}
	for h, on := range marks {
		if on {
			if _, err := tx.Exec(`INSERT INTO hunk_marks (pr_key, path, hunk_hash) VALUES (?, ?, ?)`, key, path, h); err != nil {
				return err
			}
		}
	}
	for h, on := range marks {
		if on && !prior[h] {
			if err := logEvent(tx, "mark.set", key, path, map[string]string{"hunk_hash": h}); err != nil {
				return err
			}
		}
	}
	for h := range prior {
		if !marks[h] {
			if err := logEvent(tx, "mark.clear", key, path, map[string]string{"hunk_hash": h}); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (b *Brain) CachedPRs() []PR {
	rows, err := b.db.Query(`SELECT repo, number, title, author, head_sha, base_sha, body FROM pr_cache`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []PR
	for rows.Next() {
		var p PR
		if rows.Scan(&p.Repo, &p.Number, &p.Title, &p.Author, &p.HeadSHA, &p.BaseSHA, &p.Body) == nil {
			out = append(out, p)
		}
	}
	return out
}

func (b *Brain) SetPRCache(prs []PR) error {
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

type Note struct {
	ID              int64  `json:"id"`
	PRKey           string `json:"pr_key"`
	Path            string `json:"path"`
	LineNo          int    `json:"line_no"`
	LineHash        string `json:"line_hash"`
	Body            string `json:"body"`
	Source          string `json:"source"` // "human" (typed via `c`) or "agent" (first-pass review)
	CreatedAt       string `json:"created_at"`
	ResolvedAt      string `json:"resolved_at,omitempty"`
	GitHubCommentID int64  `json:"github_comment_id,omitempty"` // 0 = local only; else the id GitHub returned
}

// NoteFilter controls whether NotesForPR / NotesForFile / PRKeysWithNotes
// include resolved notes. Counts always reflect Active-only so resolved
// notes drop out of the todo dashboard.
type NoteFilter int

const (
	NotesActive NoteFilter = iota // resolved_at IS NULL
	NotesAll                      // active + resolved
)

// PRKeysWithNotes returns every pr_key that has at least one active note, sorted.
func (b *Brain) PRKeysWithNotes() []string {
	rows, err := b.db.Query(`SELECT DISTINCT pr_key FROM notes WHERE resolved_at IS NULL`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			out = append(out, k)
		}
	}
	return out
}

func (b *Brain) NoteCountForPR(repo string, pr int) int {
	key := prKey(repo, pr)
	var count int
	b.db.QueryRow(`SELECT COUNT(*) FROM notes WHERE pr_key = ? AND resolved_at IS NULL`, key).Scan(&count)
	return count
}

func (b *Brain) NoteCountForFile(repo string, pr int, path string) int {
	key := prKey(repo, pr)
	var count int
	b.db.QueryRow(`SELECT COUNT(*) FROM notes WHERE pr_key = ? AND path = ? AND resolved_at IS NULL`, key, path).Scan(&count)
	return count
}

func (b *Brain) NotesForPR(repo string, pr int, filter NoteFilter) []Note {
	key := prKey(repo, pr)
	q := `SELECT id, pr_key, path, line_no, line_hash, body, source, created_at, resolved_at, github_comment_id FROM notes WHERE pr_key = ?`
	if filter == NotesActive {
		q += ` AND resolved_at IS NULL`
	}
	q += ` ORDER BY path, line_no, id`
	rows, err := b.db.Query(q, key)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var n Note
		var resolved sql.NullString
		var ghID sql.NullInt64
		if rows.Scan(&n.ID, &n.PRKey, &n.Path, &n.LineNo, &n.LineHash, &n.Body, &n.Source, &n.CreatedAt, &resolved, &ghID) == nil {
			if resolved.Valid {
				n.ResolvedAt = resolved.String
			}
			if ghID.Valid {
				n.GitHubCommentID = ghID.Int64
			}
			out = append(out, n)
		}
	}
	return out
}

func (b *Brain) NotesForFile(repo string, pr int, path string) []Note {
	key := prKey(repo, pr)
	rows, err := b.db.Query(
		`SELECT id, pr_key, path, line_no, line_hash, body, source, created_at, resolved_at, github_comment_id
		 FROM notes WHERE pr_key = ? AND path = ? AND resolved_at IS NULL ORDER BY line_no, id`,
		key, path)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var n Note
		var resolved sql.NullString
		var ghID sql.NullInt64
		if rows.Scan(&n.ID, &n.PRKey, &n.Path, &n.LineNo, &n.LineHash, &n.Body, &n.Source, &n.CreatedAt, &resolved, &ghID) == nil {
			if resolved.Valid {
				n.ResolvedAt = resolved.String
			}
			if ghID.Valid {
				n.GitHubCommentID = ghID.Int64
			}
			out = append(out, n)
		}
	}
	return out
}

func (b *Brain) SaveNote(repo string, pr int, path string, lineNo int, lineHash, body string) error {
	return b.insertNote(prKey(repo, pr), path, lineNo, lineHash, body, "human")
}

// insertNote is the shared path for human and agent notes: one tx that
// writes the row and the note.add event. The event payload carries the
// full body so a future replay-from-events can reconstruct the note even
// if the row has since been hard-deleted.
func (b *Brain) insertNote(key, path string, lineNo int, lineHash, body, source string) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO notes (pr_key, path, line_no, line_hash, body, source) VALUES (?, ?, ?, ?, ?, ?)`,
		key, path, lineNo, lineHash, body, source)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	payload := map[string]any{
		"note_id":   id,
		"line_no":   lineNo,
		"line_hash": lineHash,
		"source":    source,
		"body":      body,
	}
	if err := logEvent(tx, "note.add", key, path, payload); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveAgentNote records a note produced by an inline-notes action. Agents
// don't see per-line hashes so line_hash stays empty; source="agent" keeps
// these filterable away from human notes in future UI work.
func (b *Brain) SaveAgentNote(repo string, pr int, path string, lineNo int, body string) error {
	return b.insertNote(prKey(repo, pr), path, lineNo, "", body, "agent")
}

// SetNoteGitHubCommentID stamps the id GitHub returned on the note, so we
// know the note has been published and can skip it on re-publish. Idempotent:
// re-stamping the same id is a no-op and emits no event.
func (b *Brain) SetNoteGitHubCommentID(id, ghID int64) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var key, path string
	var existing sql.NullInt64
	switch err = tx.QueryRow(
		`SELECT pr_key, path, github_comment_id FROM notes WHERE id = ?`, id,
	).Scan(&key, &path, &existing); err {
	case sql.ErrNoRows:
		return fmt.Errorf("note %d not found", id)
	case nil:
	default:
		return err
	}
	if existing.Valid && existing.Int64 == ghID {
		return nil
	}
	if _, err := tx.Exec(`UPDATE notes SET github_comment_id = ? WHERE id = ?`, ghID, id); err != nil {
		return err
	}
	if err := logEvent(tx, "note.publish", key, path, map[string]any{
		"note_id":           id,
		"github_comment_id": ghID,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// ResolveNote marks a note as resolved (soft delete — the row stays so
// `rhodium notes --all` can show history). Idempotent: resolving an
// already-resolved or missing note is a no-op and emits no event.
func (b *Brain) ResolveNote(id int64) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var key, path string
	var resolved sql.NullString
	switch err = tx.QueryRow(
		`SELECT pr_key, path, resolved_at FROM notes WHERE id = ?`, id,
	).Scan(&key, &path, &resolved); err {
	case sql.ErrNoRows:
		return nil
	case nil:
	default:
		return err
	}
	if resolved.Valid {
		return nil
	}
	if _, err := tx.Exec(`UPDATE notes SET resolved_at = datetime('now') WHERE id = ?`, id); err != nil {
		return err
	}
	if err := logEvent(tx, "note.resolve", key, path, map[string]any{"note_id": id}); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteNote hard-deletes a note row. The event payload captures the
// deleted row's contents (body, line anchor, source, resolved_at) so a
// future undo/replay can resurrect the note without any other source.
func (b *Brain) DeleteNote(id int64) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var (
		key, path, body, source, lineHash string
		lineNo                             int
		resolved                           sql.NullString
	)
	switch err = tx.QueryRow(
		`SELECT pr_key, path, line_no, line_hash, body, source, resolved_at FROM notes WHERE id = ?`, id,
	).Scan(&key, &path, &lineNo, &lineHash, &body, &source, &resolved); err {
	case sql.ErrNoRows:
		return nil
	case nil:
	default:
		return err
	}
	if _, err := tx.Exec(`DELETE FROM notes WHERE id = ?`, id); err != nil {
		return err
	}
	payload := map[string]any{
		"note_id":   id,
		"line_no":   lineNo,
		"line_hash": lineHash,
		"source":    source,
		"body":      body,
	}
	if resolved.Valid {
		payload["resolved_at"] = resolved.String
	}
	if err := logEvent(tx, "note.delete", key, path, payload); err != nil {
		return err
	}
	return tx.Commit()
}

func (b *Brain) Status(repo string, pr int, fc FileChange) FileStatus {
	hunks := parseHunks(fc.Patch)
	if len(hunks) == 0 {
		return StatusSeen
	}
	marks := b.HunkMarks(repo, pr, fc.Path)
	matched := 0
	for _, h := range hunks {
		if marks[h.Hash] {
			matched++
		}
	}
	switch {
	case matched == 0:
		return StatusUnseen
	case matched == len(hunks):
		return StatusSeen
	default:
		return StatusPartial
	}
}

// FileReviewState holds the base and head SHAs at which a file was last reviewed.
type FileReviewState struct {
	HeadSHA string
	BaseSHA string
}

// SetFileReviewed records the PR head and base SHAs at which a file was last
// reviewed. Called alongside mark saves so we know what version the reviewer saw.
func (b *Brain) SetFileReviewed(repo string, pr int, path, headSHA, baseSHA string) error {
	key := prKey(repo, pr)
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT INTO file_reviews (pr_key, path, head_sha, base_sha, reviewed_at)
		VALUES (?, ?, ?, ?, datetime('now'))
		ON CONFLICT (pr_key, path) DO UPDATE SET head_sha = excluded.head_sha, base_sha = excluded.base_sha, reviewed_at = excluded.reviewed_at`,
		key, path, headSHA, baseSHA); err != nil {
		return err
	}
	payload := map[string]any{"head_sha": headSHA, "base_sha": baseSHA}
	if err := logEvent(tx, "file.reviewed", key, path, payload); err != nil {
		return err
	}
	return tx.Commit()
}

// FileReviewedState returns the head and base SHAs the reviewer last saw for
// this file. Returns zero FileReviewState if the file has never been reviewed.
func (b *Brain) FileReviewedState(repo string, pr int, path string) FileReviewState {
	key := prKey(repo, pr)
	var s FileReviewState
	b.db.QueryRow(`SELECT head_sha, base_sha FROM file_reviews WHERE pr_key = ? AND path = ?`, key, path).Scan(&s.HeadSHA, &s.BaseSHA)
	return s
}

// AllFileReviewedStates returns every (path → FileReviewState) for a given PR.
func (b *Brain) AllFileReviewedStates(repo string, pr int) map[string]FileReviewState {
	key := prKey(repo, pr)
	rows, err := b.db.Query(`SELECT path, head_sha, base_sha FROM file_reviews WHERE pr_key = ?`, key)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]FileReviewState{}
	for rows.Next() {
		var p string
		var s FileReviewState
		if rows.Scan(&p, &s.HeadSHA, &s.BaseSHA) == nil {
			out[p] = s
		}
	}
	return out
}

func (b *Brain) UnseenCount(repo string, pr int, files []FileChange) int {
	n := 0
	for _, f := range files {
		if b.Status(repo, pr, f) != StatusSeen {
			n++
		}
	}
	return n
}

package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

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
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS hunk_marks (
			pr_key    TEXT NOT NULL,
			path      TEXT NOT NULL,
			hunk_hash TEXT NOT NULL,
			PRIMARY KEY (pr_key, path, hunk_hash)
		);
		CREATE TABLE IF NOT EXISTS pr_cache (
			repo     TEXT    NOT NULL,
			number   INTEGER NOT NULL,
			title    TEXT    NOT NULL,
			author   TEXT    NOT NULL,
			head_sha TEXT    NOT NULL,
			base_sha TEXT    NOT NULL DEFAULT '',
			body     TEXT    NOT NULL DEFAULT '',
			PRIMARY KEY (repo, number)
		);
		CREATE TABLE IF NOT EXISTS notes (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			pr_key     TEXT    NOT NULL,
			path       TEXT    NOT NULL,
			line_no    INTEGER NOT NULL,
			line_hash  TEXT    NOT NULL,
			body       TEXT    NOT NULL,
			created_at TEXT    NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_notes_file ON notes (pr_key, path);

		CREATE TABLE IF NOT EXISTS file_reviews (
			pr_key      TEXT NOT NULL,
			path        TEXT NOT NULL,
			head_sha    TEXT NOT NULL,
			base_sha    TEXT NOT NULL DEFAULT '',
			reviewed_at TEXT NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (pr_key, path)
		);

		CREATE TABLE IF NOT EXISTS pr_scrutiny (
			pr_key TEXT NOT NULL PRIMARY KEY
		);

		CREATE TABLE IF NOT EXISTS catch_up_sessions (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			pr_key      TEXT    NOT NULL,
			old_head    TEXT    NOT NULL,
			new_head    TEXT    NOT NULL,
			old_base    TEXT    NOT NULL DEFAULT '',
			new_base    TEXT    NOT NULL DEFAULT '',
			files_total INTEGER NOT NULL DEFAULT 0,
			files_done  INTEGER NOT NULL DEFAULT 0,
			created_at  TEXT    NOT NULL DEFAULT (datetime('now')),
			completed_at TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_catchup_pr ON catch_up_sessions (pr_key);
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate brain db: %w", err)
	}
	return &Brain{db: db}, nil
}

func (b *Brain) Close() error {
	return b.db.Close()
}

// CatchUpSession represents a persistent catch-up session.
type CatchUpSession struct {
	ID          int64
	PRKey       string
	OldHead     string
	NewHead     string
	OldBase     string
	NewBase     string
	FilesTotal  int
	FilesDone   int
	CreatedAt   string
	CompletedAt string // empty if still active
}

// ActiveCatchUp returns the active (incomplete) catch-up session for a PR, or nil.
func (b *Brain) ActiveCatchUp(repo string, pr int) *CatchUpSession {
	key := prKey(repo, pr)
	var s CatchUpSession
	var completedAt sql.NullString
	err := b.db.QueryRow(
		`SELECT id, pr_key, old_head, new_head, old_base, new_base, files_total, files_done, created_at, completed_at
		 FROM catch_up_sessions WHERE pr_key = ? AND completed_at IS NULL ORDER BY id DESC LIMIT 1`, key,
	).Scan(&s.ID, &s.PRKey, &s.OldHead, &s.NewHead, &s.OldBase, &s.NewBase, &s.FilesTotal, &s.FilesDone, &s.CreatedAt, &completedAt)
	if err != nil {
		return nil
	}
	if completedAt.Valid {
		s.CompletedAt = completedAt.String
	}
	return &s
}

// CreateCatchUp creates a new catch-up session. If there's already an active
// session for this PR, it completes it first.
func (b *Brain) CreateCatchUp(repo string, pr int, oldHead, newHead, oldBase, newBase string, filesTotal int) (*CatchUpSession, error) {
	key := prKey(repo, pr)
	// Complete any existing active session.
	b.db.Exec(`UPDATE catch_up_sessions SET completed_at = datetime('now') WHERE pr_key = ? AND completed_at IS NULL`, key)

	res, err := b.db.Exec(
		`INSERT INTO catch_up_sessions (pr_key, old_head, new_head, old_base, new_base, files_total) VALUES (?, ?, ?, ?, ?, ?)`,
		key, oldHead, newHead, oldBase, newBase, filesTotal)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &CatchUpSession{
		ID: id, PRKey: key,
		OldHead: oldHead, NewHead: newHead, OldBase: oldBase, NewBase: newBase,
		FilesTotal: filesTotal,
	}, nil
}

// CatchUpAdvanceFile increments the files_done counter and marks the session
// complete if all files are done.
func (b *Brain) CatchUpAdvanceFile(sessionID int64) error {
	_, err := b.db.Exec(`UPDATE catch_up_sessions SET files_done = files_done + 1 WHERE id = ?`, sessionID)
	if err != nil {
		return err
	}
	// Auto-complete if done.
	_, err = b.db.Exec(`UPDATE catch_up_sessions SET completed_at = datetime('now') WHERE id = ? AND files_done >= files_total`, sessionID)
	return err
}

// CompleteCatchUp marks a session as completed.
func (b *Brain) CompleteCatchUp(sessionID int64) error {
	_, err := b.db.Exec(`UPDATE catch_up_sessions SET completed_at = datetime('now') WHERE id = ?`, sessionID)
	return err
}

// AllActiveCatchUps returns all active catch-up sessions across all PRs.
func (b *Brain) AllActiveCatchUps() []CatchUpSession {
	rows, err := b.db.Query(
		`SELECT id, pr_key, old_head, new_head, old_base, new_base, files_total, files_done, created_at
		 FROM catch_up_sessions WHERE completed_at IS NULL ORDER BY created_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []CatchUpSession
	for rows.Next() {
		var s CatchUpSession
		if rows.Scan(&s.ID, &s.PRKey, &s.OldHead, &s.NewHead, &s.OldBase, &s.NewBase, &s.FilesTotal, &s.FilesDone, &s.CreatedAt) == nil {
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
	if on {
		_, err := b.db.Exec(`INSERT OR IGNORE INTO pr_scrutiny (pr_key) VALUES (?)`, key)
		return err
	}
	_, err := b.db.Exec(`DELETE FROM pr_scrutiny WHERE pr_key = ?`, key)
	return err
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
	ID        int64
	PRKey     string
	Path      string
	LineNo    int
	LineHash  string
	Body      string
	CreatedAt string
}

func (b *Brain) NoteCountForPR(repo string, pr int) int {
	key := prKey(repo, pr)
	var count int
	b.db.QueryRow(`SELECT COUNT(*) FROM notes WHERE pr_key = ?`, key).Scan(&count)
	return count
}

func (b *Brain) NoteCountForFile(repo string, pr int, path string) int {
	key := prKey(repo, pr)
	var count int
	b.db.QueryRow(`SELECT COUNT(*) FROM notes WHERE pr_key = ? AND path = ?`, key, path).Scan(&count)
	return count
}

func (b *Brain) NotesForPR(repo string, pr int) []Note {
	key := prKey(repo, pr)
	rows, err := b.db.Query(
		`SELECT id, pr_key, path, line_no, line_hash, body, created_at FROM notes WHERE pr_key = ? ORDER BY path, line_no, id`,
		key)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var n Note
		if rows.Scan(&n.ID, &n.PRKey, &n.Path, &n.LineNo, &n.LineHash, &n.Body, &n.CreatedAt) == nil {
			out = append(out, n)
		}
	}
	return out
}

func (b *Brain) NotesForFile(repo string, pr int, path string) []Note {
	key := prKey(repo, pr)
	rows, err := b.db.Query(
		`SELECT id, pr_key, path, line_no, line_hash, body, created_at FROM notes WHERE pr_key = ? AND path = ? ORDER BY line_no, id`,
		key, path)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var n Note
		if rows.Scan(&n.ID, &n.PRKey, &n.Path, &n.LineNo, &n.LineHash, &n.Body, &n.CreatedAt) == nil {
			out = append(out, n)
		}
	}
	return out
}

func (b *Brain) SaveNote(repo string, pr int, path string, lineNo int, lineHash, body string) error {
	key := prKey(repo, pr)
	_, err := b.db.Exec(
		`INSERT INTO notes (pr_key, path, line_no, line_hash, body) VALUES (?, ?, ?, ?, ?)`,
		key, path, lineNo, lineHash, body)
	return err
}

func (b *Brain) DeleteNote(id int64) error {
	_, err := b.db.Exec(`DELETE FROM notes WHERE id = ?`, id)
	return err
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
	_, err := b.db.Exec(`
		INSERT INTO file_reviews (pr_key, path, head_sha, base_sha, reviewed_at)
		VALUES (?, ?, ?, ?, datetime('now'))
		ON CONFLICT (pr_key, path) DO UPDATE SET head_sha = excluded.head_sha, base_sha = excluded.base_sha, reviewed_at = excluded.reviewed_at`,
		key, path, headSHA, baseSHA)
	return err
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

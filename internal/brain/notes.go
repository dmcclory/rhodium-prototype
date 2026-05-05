package brain

import (
	"database/sql"
	"fmt"
	"strings"

	"rhodium/internal/diff"
	"rhodium/internal/gh"
)

// Urgency is the triage level on a note, modeled after Iron's CR urgency
// (Now / Soon / Someday). Empty means "untriaged" — the note exists but
// hasn't been prioritized yet.
type Urgency string

const (
	UrgencyNow     Urgency = "now"
	UrgencySoon    Urgency = "soon"
	UrgencySomeday Urgency = "someday"
)

func (u Urgency) Valid() bool {
	switch u {
	case UrgencyNow, UrgencySoon, UrgencySomeday:
		return true
	}
	return false
}

// Next cycles to the next urgency level: "" → now → soon → someday → "".
func (u Urgency) Next() Urgency {
	switch u {
	case "":
		return UrgencyNow
	case UrgencyNow:
		return UrgencySoon
	case UrgencySoon:
		return UrgencySomeday
	default:
		return ""
	}
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
	Urgency         string `json:"urgency,omitempty"`           // "now" / "soon" / "someday" / empty
	Assignee        string `json:"assignee,omitempty"`          // "@username" or empty
	BaseSHA         string `json:"base_sha,omitempty"`          // PR head SHA when note was created
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
	key := PRKey(repo, pr)
	var count int
	b.db.QueryRow(`SELECT COUNT(*) FROM notes WHERE pr_key = ? AND resolved_at IS NULL`, key).Scan(&count)
	return count
}

func (b *Brain) NoteCountForFile(repo string, pr int, path string) int {
	key := PRKey(repo, pr)
	var count int
	b.db.QueryRow(`SELECT COUNT(*) FROM notes WHERE pr_key = ? AND path = ? AND resolved_at IS NULL`, key, path).Scan(&count)
	return count
}

func (b *Brain) NotesForPR(repo string, pr int, filter NoteFilter) []Note {
	key := PRKey(repo, pr)
	q := `SELECT id, pr_key, path, line_no, line_hash, body, source, created_at, resolved_at, github_comment_id, urgency, assignee, base_sha FROM notes WHERE pr_key = ?`
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
		var resolved, urgency, assignee, baseSHA sql.NullString
		var ghID sql.NullInt64
		if rows.Scan(&n.ID, &n.PRKey, &n.Path, &n.LineNo, &n.LineHash, &n.Body, &n.Source, &n.CreatedAt, &resolved, &ghID, &urgency, &assignee, &baseSHA) == nil {
			if resolved.Valid {
				n.ResolvedAt = resolved.String
			}
			if ghID.Valid {
				n.GitHubCommentID = ghID.Int64
			}
			if urgency.Valid {
				n.Urgency = urgency.String
			}
			if assignee.Valid {
				n.Assignee = assignee.String
			}
			if baseSHA.Valid {
				n.BaseSHA = baseSHA.String
			}
			out = append(out, n)
		}
	}
	return out
}

func (b *Brain) NotesForFile(repo string, pr int, path string) []Note {
	key := PRKey(repo, pr)
	rows, err := b.db.Query(
		`SELECT id, pr_key, path, line_no, line_hash, body, source, created_at, resolved_at, github_comment_id, urgency, assignee, base_sha
		 FROM notes WHERE pr_key = ? AND path = ? AND resolved_at IS NULL ORDER BY line_no, id`,
		key, path)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var n Note
		var resolved, urgency, assignee, baseSHA sql.NullString
		var ghID sql.NullInt64
		if rows.Scan(&n.ID, &n.PRKey, &n.Path, &n.LineNo, &n.LineHash, &n.Body, &n.Source, &n.CreatedAt, &resolved, &ghID, &urgency, &assignee, &baseSHA) == nil {
			if resolved.Valid {
				n.ResolvedAt = resolved.String
			}
			if ghID.Valid {
				n.GitHubCommentID = ghID.Int64
			}
			if urgency.Valid {
				n.Urgency = urgency.String
			}
			if assignee.Valid {
				n.Assignee = assignee.String
			}
			if baseSHA.Valid {
				n.BaseSHA = baseSHA.String
			}
			out = append(out, n)
		}
	}
	return out
}

func (b *Brain) SaveNote(repo string, pr int, path string, lineNo int, lineHash, body, baseSHA string) error {
	return b.insertNote(PRKey(repo, pr), path, lineNo, lineHash, body, "human", "", "", baseSHA)
}

// SaveNoteWithUrgency is like SaveNote but also records urgency and assignee.
func (b *Brain) SaveNoteWithUrgency(repo string, pr int, path string, lineNo int, lineHash, body string, urgency Urgency, assignee, baseSHA string) error {
	u := ""
	if urgency.Valid() {
		u = string(urgency)
	}
	return b.insertNote(PRKey(repo, pr), path, lineNo, lineHash, body, "human", u, assignee, baseSHA)
}

// insertNote is the shared path for human and agent notes: one tx that
// writes the row and the note.add event. The event payload carries the
// full body so a future replay-from-events can reconstruct the note even
// if the row has since been hard-deleted.
func (b *Brain) insertNote(key, path string, lineNo int, lineHash, body, source, urgency, assignee, baseSHA string) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO notes (pr_key, path, line_no, line_hash, body, source, urgency, assignee, base_sha) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key, path, lineNo, lineHash, body, source, urgency, assignee, baseSHA)
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
	if urgency != "" {
		payload["urgency"] = urgency
	}
	if assignee != "" {
		payload["assignee"] = assignee
	}
	if baseSHA != "" {
		payload["base_sha"] = baseSHA
	}
	if err := logEvent(tx, "note.add", key, path, payload); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveAgentNote records a note produced by an inline-notes action. Agents
// don't see per-line hashes so line_hash stays empty; source="agent" keeps
// these filterable away from human notes in future UI work.
func (b *Brain) SaveAgentNote(repo string, pr int, path string, lineNo int, body string, baseSHA string) error {
	return b.insertNote(PRKey(repo, pr), path, lineNo, "", body, "agent", "", "", baseSHA)
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
// deleted row's contents (body, line anchor, source, resolved_at, urgency,
// assignee) so a future undo/replay can resurrect the note without any
// other source.
func (b *Brain) DeleteNote(id int64) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var (
		key, path, body, source, lineHash string
		lineNo                            int
		resolved, urgency, assignee       sql.NullString
	)
	switch err = tx.QueryRow(
		`SELECT pr_key, path, line_no, line_hash, body, source, resolved_at, urgency, assignee FROM notes WHERE id = ?`, id,
	).Scan(&key, &path, &lineNo, &lineHash, &body, &source, &resolved, &urgency, &assignee); err {
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
	if urgency.Valid {
		payload["urgency"] = urgency.String
	}
	if assignee.Valid {
		payload["assignee"] = assignee.String
	}
	if err := logEvent(tx, "note.delete", key, path, payload); err != nil {
		return err
	}
	return tx.Commit()
}

// SetNoteUrgency sets the urgency level on a note. Pass an empty string to
// clear urgency (back to untriaged).
func (b *Brain) SetNoteUrgency(id int64, urgency Urgency) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var key, path string
	switch err = tx.QueryRow(
		`SELECT pr_key, path FROM notes WHERE id = ?`, id,
	).Scan(&key, &path); err {
	case sql.ErrNoRows:
		return fmt.Errorf("note %d not found", id)
	case nil:
	default:
		return err
	}
	var u string
	if urgency.Valid() {
		u = string(urgency)
	}
	if _, err := tx.Exec(`UPDATE notes SET urgency = ? WHERE id = ?`, u, id); err != nil {
		return err
	}
	if err := logEvent(tx, "note.set-urgency", key, path, map[string]any{
		"note_id": id,
		"urgency": u,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// SetNoteAssignee sets the assignee on a note. Pass an empty string to
// clear the assignee.
func (b *Brain) SetNoteAssignee(id int64, assignee string) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var key, path string
	switch err = tx.QueryRow(
		`SELECT pr_key, path FROM notes WHERE id = ?`, id,
	).Scan(&key, &path); err {
	case sql.ErrNoRows:
		return fmt.Errorf("note %d not found", id)
	case nil:
	default:
		return err
	}
	if _, err := tx.Exec(`UPDATE notes SET assignee = ? WHERE id = ?`, assignee, id); err != nil {
		return err
	}
	if err := logEvent(tx, "note.set-assignee", key, path, map[string]any{
		"note_id":  id,
		"assignee": assignee,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// NoteCountByUrgency returns the count of active notes grouped by urgency.
func (b *Brain) NoteCountByUrgency(repo string, pr int) (now, soon, someday, untriaged int) {
	key := PRKey(repo, pr)
	rows, err := b.db.Query(
		`SELECT urgency, COUNT(*) FROM notes WHERE pr_key = ? AND resolved_at IS NULL GROUP BY urgency`, key)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var u sql.NullString
		var count int
		if rows.Scan(&u, &count) == nil {
			if u.Valid {
				switch u.String {
				case "now":
					now = count
				case "soon":
					soon = count
				case "someday":
					someday = count
				default:
					untriaged = count
				}
			} else {
				untriaged = count
			}
		}
	}
	return
}

// ResolveStaleNotes checks each active note on a PR against the current head
// SHA. If a note's underlying line content has changed (hash mismatch or the
// line no longer exists), the note is resolved as stale. Returns the number
// of notes that were auto-resolved.
func (b *Brain) ResolveStaleNotes(repo string, pr int, headSHA string) (int, error) {
	if headSHA == "" {
		return 0, nil
	}

	notes := b.NotesForPR(repo, pr, NotesActive)
	if len(notes) == 0 {
		return 0, nil
	}

	// Group notes by path to minimize file fetches.
	byPath := map[string][]Note{}
	for _, n := range notes {
		byPath[n.Path] = append(byPath[n.Path], n)
	}

	var resolved int
	for path, pathNotes := range byPath {
		content, err := gh.FetchFileAtRef(repo, path, headSHA)
		if err != nil || content == "" {
			// Can't fetch the file — resolve all notes on it as stale.
			for _, n := range pathNotes {
				if err := b.resolveNoteByID(n.ID); err != nil {
					return resolved, err
				}
				resolved++
			}
			continue
		}

		for _, n := range pathNotes {
			if lineIsStale(n, content) {
				if err := b.resolveNoteByID(n.ID); err != nil {
					return resolved, err
				}
				resolved++
			}
		}
	}
	return resolved, nil
}

// resolveNoteByID resolves a single note by ID without re-querying pr_key/path.
// Internal helper used by ResolveStaleNotes to avoid redundant lookups.
func (b *Brain) resolveNoteByID(id int64) error {
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
		return nil // already resolved
	}
	if _, err := tx.Exec(`UPDATE notes SET resolved_at = datetime('now') WHERE id = ?`, id); err != nil {
		return err
	}
	if err := logEvent(tx, "note.resolve", key, path, map[string]any{"note_id": id, "reason": "stale"}); err != nil {
		return err
	}
	return tx.Commit()
}

// ResolvedNotesForFile returns resolved notes for a specific file.
// These are notes that were manually resolved or auto-resolved as stale.
func (b *Brain) ResolvedNotesForFile(repo string, pr int, path string) []Note {
	key := PRKey(repo, pr)
	rows, err := b.db.Query(
		`SELECT id, pr_key, path, line_no, line_hash, body, source, created_at, resolved_at, github_comment_id, urgency, assignee, base_sha
		 FROM notes WHERE pr_key = ? AND path = ? AND resolved_at IS NOT NULL ORDER BY line_no, id`,
		key, path)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var n Note
		var resolved, urgency, assignee, baseSHA sql.NullString
		var ghID sql.NullInt64
		if rows.Scan(&n.ID, &n.PRKey, &n.Path, &n.LineNo, &n.LineHash, &n.Body, &n.Source, &n.CreatedAt, &resolved, &ghID, &urgency, &assignee, &baseSHA) == nil {
			if resolved.Valid {
				n.ResolvedAt = resolved.String
			}
			if ghID.Valid {
				n.GitHubCommentID = ghID.Int64
			}
			if urgency.Valid {
				n.Urgency = urgency.String
			}
			if assignee.Valid {
				n.Assignee = assignee.String
			}
			if baseSHA.Valid {
				n.BaseSHA = baseSHA.String
			}
			out = append(out, n)
		}
	}
	return out
}

// lineIsStale checks whether a note anchored to a specific line in a file
// is still valid given the current file content. Returns true if the line
// number is out of range or the content hash has changed.
func lineIsStale(n Note, content string) bool {
	if n.LineHash == "" {
		// No hash to compare against — assume not stale.
		return false
	}
	lines := strings.Split(content, "\n")
	idx := n.LineNo - 1
	if idx < 0 || idx >= len(lines) {
		// Line no longer exists in the file.
		return true
	}
	currentHash := diff.HashHunkBody([]string{"+" + lines[idx]})
	return currentHash != n.LineHash
}

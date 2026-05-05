package brain

import (
	"database/sql"
	"fmt"
)

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
	key := PRKey(repo, pr)
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
	key := PRKey(repo, pr)
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

// MarkFullyReviewed advances the brain to the current goal (head/base) for
// every path in the PR. This is the rhodium equivalent of Iron's
// mark_fully_reviewed: the reviewer declares "I'm done" and the brain
// catches up — no questions asked. Any active session is completed first.
func (b *Brain) MarkFullyReviewed(repo string, pr int, goalHead, goalBase string, allPaths []string) error {
	key := PRKey(repo, pr)

	// Complete any active session for this PR.
	existing := b.ActiveSession(repo, pr)
	if existing != nil {
		if err := b.CompleteSession(existing.ID); err != nil {
			return fmt.Errorf("completing active session: %w", err)
		}
	}

	if len(allPaths) == 0 {
		return nil
	}

	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, path := range allPaths {
		if _, err := tx.Exec(`
			INSERT INTO file_reviews (pr_key, path, head_sha, base_sha, mark_kind, reviewed_at)
			VALUES (?, ?, ?, ?, ?, datetime('now'))
			ON CONFLICT (pr_key, path) DO UPDATE
				SET head_sha = excluded.head_sha, base_sha = excluded.base_sha, mark_kind = excluded.mark_kind, reviewed_at = excluded.reviewed_at`,
			key, path, goalHead, goalBase, MarkAuto); err != nil {
			return err
		}
	}

	if err := logEvent(tx, "brain.fully_reviewed", key, "", map[string]any{
		"goal_head": goalHead,
		"goal_base": goalBase,
		"files":     len(allPaths),
	}); err != nil {
		return err
	}

	return tx.Commit()
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

// ClearPR drops all hunk marks, file reviews, and review session data for a
// PR. Notes are preserved. Useful when a PR was reviewed at the wrong SHA or
// the reviewer wants a fresh start.
func (b *Brain) ClearPR(prKey string) (int64, error) {
	tx, err := b.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var affected int64

	// Drop hunk marks.
	res, err := tx.Exec(`DELETE FROM hunk_marks WHERE pr_key = ?`, prKey)
	if err != nil {
		return 0, fmt.Errorf("hunk_marks: %w", err)
	}
	n, _ := res.RowsAffected()
	affected += n

	// Drop file reviews.
	res, err = tx.Exec(`DELETE FROM file_reviews WHERE pr_key = ?`, prKey)
	if err != nil {
		return 0, fmt.Errorf("file_reviews: %w", err)
	}
	n, _ = res.RowsAffected()
	affected += n

	// Complete all sessions for this PR and drop their file entries.
	var sessionIDs []int64
	rows, err := tx.Query(`SELECT id FROM review_sessions WHERE pr_key = ?`, prKey)
	if err != nil {
		return 0, fmt.Errorf("review_sessions: %w", err)
	}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			sessionIDs = append(sessionIDs, id)
		}
	}
	rows.Close()

	if _, err := tx.Exec(`UPDATE review_sessions SET completed_at = datetime('now') WHERE pr_key = ? AND completed_at IS NULL`, prKey); err != nil {
		return 0, fmt.Errorf("complete sessions: %w", err)
	}
	res, err = tx.Exec(`DELETE FROM review_session_files WHERE session_id IN (SELECT id FROM review_sessions WHERE pr_key = ?)`, prKey)
	if err != nil {
		return 0, fmt.Errorf("session files: %w", err)
	}
	n, _ = res.RowsAffected()
	affected += n

	// Log the clear event.
	if err := logEvent(tx, "brain.clear", prKey, "", map[string]any{
		"rows_removed": affected,
		"sessions":     len(sessionIDs),
	}); err != nil {
		return 0, err
	}

	return affected, tx.Commit()
}

// ForgetFile drops hunk marks and the file-review entry for one file within
// a PR. Notes on that file are preserved. Useful for misclassified marks or
// files that shouldn't have been advanced.
func (b *Brain) ForgetFile(prKey, path string) (int64, error) {
	tx, err := b.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var affected int64

	res, err := tx.Exec(`DELETE FROM hunk_marks WHERE pr_key = ? AND path = ?`, prKey, path)
	if err != nil {
		return 0, fmt.Errorf("hunk_marks: %w", err)
	}
	n, _ := res.RowsAffected()
	affected += n

	res, err = tx.Exec(`DELETE FROM file_reviews WHERE pr_key = ? AND path = ?`, prKey, path)
	if err != nil {
		return 0, fmt.Errorf("file_reviews: %w", err)
	}
	n, _ = res.RowsAffected()
	affected += n

	// Mark the file as not-done in any active session.
	res, err = tx.Exec(
		`UPDATE review_session_files SET done = 0 WHERE session_id IN
		 (SELECT id FROM review_sessions WHERE pr_key = ? AND completed_at IS NULL)
		 AND path = ?`, prKey, path)
	if err != nil {
		return 0, fmt.Errorf("session files: %w", err)
	}
	n, _ = res.RowsAffected()
	affected += n

	if err := logEvent(tx, "brain.forget", prKey, path, map[string]any{
		"rows_removed": affected,
	}); err != nil {
		return 0, err
	}

	return affected, tx.Commit()
}

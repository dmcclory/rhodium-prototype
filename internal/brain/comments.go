package brain

import (
	"database/sql"
	"fmt"

	"rhodium/internal/gh"
)

// GitHubComment is the brain's read-only mirror of a GitHub comment
// (issue, review, or inline) synced via the API.
type GitHubComment struct {
	ID        int64
	PRKey     string
	GHID      int64
	Type      string // "issue", "review", or "inline"
	Author    string
	Body      string
	CreatedAt string
	State     string // review only
	Path      string // inline only
	Line      int    // inline only
}

// SyncGitHubComments fetches all comment streams for a PR from GitHub and
// upserts them into the brain's github_comments table. Comments that
// already exist (matched by gh_id) are skipped. Returns the count of new
// comments inserted.
func (b *Brain) SyncGitHubComments(repo string, pr int) (int, error) {
	comments, err := gh.FetchPRComments(repo, pr)
	if err != nil {
		return 0, fmt.Errorf("fetch comments: %w", err)
	}
	return b.SyncGitHubCommentsFromData(comments, repo, pr)
}

// SyncGitHubCommentsFromData upserts a slice of gh.Comment into the
// github_comments table. Extracted from SyncGitHubComments so it can be
// unit-tested without shelling out to `gh`.
func (b *Brain) SyncGitHubCommentsFromData(comments []gh.Comment, repo string, pr int) (int, error) {
	key := PRKey(repo, pr)
	tx, err := b.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	inserted := 0
	for _, c := range comments {
		result, err := tx.Exec(`INSERT OR IGNORE INTO github_comments
			(pr_key, gh_id, type, author, body, created_at, state, path, line)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			key, c.GHID, c.Type, c.Author, c.Body, c.CreatedAt, c.State, c.Path, c.Line)
		if err != nil {
			return inserted, fmt.Errorf("insert comment %d: %w", c.GHID, err)
		}
		n, _ := result.RowsAffected()
		if n > 0 {
			inserted++
		}
	}

	if err := tx.Commit(); err != nil {
		return inserted, err
	}
	return inserted, nil
}

// GitHubCommentsForPR returns all synced GitHub comments for a PR, ordered
// by creation time.
func (b *Brain) GitHubCommentsForPR(repo string, pr int) []GitHubComment {
	rows, err := b.db.Query(`
		SELECT id, pr_key, gh_id, type, author, body, created_at, state, path, line
		FROM github_comments WHERE pr_key = ? ORDER BY created_at, id`,
		PRKey(repo, pr))
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanGitHubComments(rows)
}

// GitHubCommentsForFile returns inline GitHub comments for a specific file
// in a PR.
func (b *Brain) GitHubCommentsForFile(repo string, pr int, path string) []GitHubComment {
	rows, err := b.db.Query(`
		SELECT id, pr_key, gh_id, type, author, body, created_at, state, path, line
		FROM github_comments
		WHERE pr_key = ? AND type = 'inline' AND path = ?
		ORDER BY line, created_at, id`,
		PRKey(repo, pr), path)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanGitHubComments(rows)
}

// GitHubCommentCountForPR returns the number of synced GitHub comments for
// a PR.
func (b *Brain) GitHubCommentCountForPR(repo string, pr int) int {
	var count int
	b.db.QueryRow(`SELECT COUNT(*) FROM github_comments WHERE pr_key = ?`,
		PRKey(repo, pr)).Scan(&count)
	return count
}

// HasGitHubComments reports whether any GitHub comments have been synced
// for the given PR.
func (b *Brain) HasGitHubComments(repo string, pr int) bool {
	var exists int
	b.db.QueryRow(`SELECT 1 FROM github_comments WHERE pr_key = ? LIMIT 1`,
		PRKey(repo, pr)).Scan(&exists)
	return exists == 1
}

// scanGitHubComments iterates a sql.Rows from the github_comments table
// and returns the parsed slice.
func scanGitHubComments(rows *sql.Rows) []GitHubComment {
	var out []GitHubComment
	for rows.Next() {
		var c GitHubComment
		var state, path sql.NullString
		var line sql.NullInt64
		if rows.Scan(&c.ID, &c.PRKey, &c.GHID, &c.Type, &c.Author, &c.Body, &c.CreatedAt, &state, &path, &line) != nil {
			continue
		}
		if state.Valid {
			c.State = state.String
		}
		if path.Valid {
			c.Path = path.String
		}
		if line.Valid {
			c.Line = int(line.Int64)
		}
		out = append(out, c)
	}
	return out
}

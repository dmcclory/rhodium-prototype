-- +goose Up

-- Replace the narrow catch_up_sessions counter with a first-class review
-- session: a stable snapshot of the file list + classifications at open
-- time, resumable across app restarts as long as the PR hasn't moved
-- (head_sha / base_sha unchanged).
--
-- goal_head / goal_base mirror head_sha / base_sha at session creation;
-- they're the "brain advance target" that CompleteSession writes through
-- to file_reviews when the session completes. If the PR head moves while
-- a session is live, the session is abandoned (not resumed) and a fresh
-- one is created at the new corners.
CREATE TABLE IF NOT EXISTS review_sessions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_key       TEXT    NOT NULL,
    head_sha     TEXT    NOT NULL,
    base_sha     TEXT    NOT NULL DEFAULT '',
    goal_head    TEXT    NOT NULL,
    goal_base    TEXT    NOT NULL DEFAULT '',
    started_at   TEXT    NOT NULL DEFAULT (datetime('now')),
    completed_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_review_sessions_pr ON review_sessions (pr_key);

-- review_session_files is the per-file snapshot: which paths are in the
-- diff set, what class they were assigned at session open, whether the
-- reviewer has marked them done within this session. `class` is advisory
-- (display hint), not load-bearing — the diff view reclassifies on open
-- if needed. done is 0/1 (sqlite has no bool).
CREATE TABLE IF NOT EXISTS review_session_files (
    session_id INTEGER NOT NULL REFERENCES review_sessions(id) ON DELETE CASCADE,
    path       TEXT    NOT NULL,
    class      TEXT    NOT NULL DEFAULT '',
    done       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (session_id, path)
);

DROP INDEX IF EXISTS idx_catchup_pr;
DROP TABLE IF EXISTS catch_up_sessions;

-- +goose Down
CREATE TABLE IF NOT EXISTS catch_up_sessions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_key       TEXT    NOT NULL,
    old_head     TEXT    NOT NULL,
    new_head     TEXT    NOT NULL,
    old_base     TEXT    NOT NULL DEFAULT '',
    new_base     TEXT    NOT NULL DEFAULT '',
    files_total  INTEGER NOT NULL DEFAULT 0,
    files_done   INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT    NOT NULL DEFAULT (datetime('now')),
    completed_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_catchup_pr ON catch_up_sessions (pr_key);

DROP TABLE IF EXISTS review_session_files;
DROP INDEX IF EXISTS idx_review_sessions_pr;
DROP TABLE IF EXISTS review_sessions;

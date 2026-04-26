-- +goose Up
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
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_key      TEXT    NOT NULL,
    path        TEXT    NOT NULL,
    line_no     INTEGER NOT NULL,
    line_hash   TEXT    NOT NULL,
    body        TEXT    NOT NULL,
    source      TEXT    NOT NULL DEFAULT 'human',
    created_at  TEXT    NOT NULL DEFAULT (datetime('now')),
    resolved_at TEXT
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

-- +goose Down
DROP INDEX IF EXISTS idx_catchup_pr;
DROP TABLE IF EXISTS catch_up_sessions;
DROP TABLE IF EXISTS pr_scrutiny;
DROP TABLE IF EXISTS file_reviews;
DROP INDEX IF EXISTS idx_notes_file;
DROP TABLE IF EXISTS notes;
DROP TABLE IF EXISTS pr_cache;
DROP TABLE IF EXISTS hunk_marks;

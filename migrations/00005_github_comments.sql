-- +goose Up

-- Read-only cache of GitHub comments pulled via the API. Kept separate from
-- local notes (which live in the `notes` table) because GitHub comments are
-- an external source of truth — they're never created, edited, or deleted
-- by rhodium itself.
CREATE TABLE IF NOT EXISTS github_comments (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_key     TEXT    NOT NULL,
    gh_id      INTEGER NOT NULL UNIQUE,  -- GitHub's comment id; dedup key
    type       TEXT    NOT NULL,          -- "issue", "review", or "inline"
    author     TEXT    NOT NULL,
    body       TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    state      TEXT,                      -- review only: APPROVED, CHANGES_REQUESTED, etc.
    path       TEXT,                      -- inline only: relative file path
    line       INTEGER                    -- inline only: new-file line number
);

-- +goose Down
DROP TABLE IF EXISTS github_comments;

-- +goose Up
-- Local-only status tracking for PRs. Complements GitHub's native review
-- states without duplicating them. Stored per pr_key so it survives PR
-- cache refreshes and is cleared on archive/merge.
CREATE TABLE pr_statuses (
    pr_key      TEXT NOT NULL PRIMARY KEY,
    status      TEXT NOT NULL,
    set_at      TEXT NOT NULL DEFAULT (datetime('now')),
    set_by      TEXT NOT NULL DEFAULT 'user'
);

-- +goose Down
DROP TABLE pr_statuses;

-- +goose Up

-- Track which local notes have been published as GitHub PR review comments.
-- NULL means "local only"; a non-null value is the id GitHub returned from
-- POST /repos/:o/:r/pulls/:n/comments. Keyed by that id so a future feature
-- can re-fetch the comment (to detect outside edits / resolution) without
-- any extra mapping table.
ALTER TABLE notes ADD COLUMN github_comment_id INTEGER;

-- +goose Down
-- SQLite < 3.35 doesn't DROP COLUMN, but rhodium requires modern SQLite
-- (WAL + pragma_table_info are already used elsewhere) so this is safe.
ALTER TABLE notes DROP COLUMN github_comment_id;

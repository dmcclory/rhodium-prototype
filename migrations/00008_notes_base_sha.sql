-- +goose Up
-- Records the PR head SHA at the time a note was created, enabling
-- staleness detection: when the PR moves and the underlying line content
-- changes, the note is considered stale.
ALTER TABLE notes ADD COLUMN base_sha TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE notes DROP COLUMN base_sha;

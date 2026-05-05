-- +goose Up
ALTER TABLE notes ADD COLUMN urgency TEXT;
ALTER TABLE notes ADD COLUMN assignee TEXT;

-- +goose Down
-- SQLite doesn't support DROP COLUMN on older versions, but modern
-- SQLite (3.35.0+) does. rhodium requires 3.35.0+ for ALTER TABLE.
ALTER TABLE notes DROP COLUMN urgency;
ALTER TABLE notes DROP COLUMN assignee;

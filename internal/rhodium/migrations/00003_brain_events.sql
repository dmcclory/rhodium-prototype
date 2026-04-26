-- +goose Up

-- Append-only log of every brain-state mutation. Each mutator in brain.go
-- writes an event alongside its table write in the same transaction, so
-- events are as durable as the state they describe: a rolled-back write
-- leaves no event behind.
--
-- Tables remain the source of truth; this log is a durable side-record
-- that unlocks future features (undo, replay onto a fresh DB, cross-
-- machine sync). It is intentionally *not* read back into the tables
-- by any current code path.
--
-- `id` is the logical clock — strictly monotonic per brain. `ts` is for
-- display only. `pr_key` and `path` are hoisted out of the JSON payload
-- so per-PR / per-file filters stay indexable; they are empty strings
-- for events that don't scope to one.
--
-- `pr_cache` writes are deliberately *not* logged: that table mirrors
-- GitHub state and isn't part of the reviewer's brain.
CREATE TABLE IF NOT EXISTS brain_events (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    ts      TEXT    NOT NULL DEFAULT (datetime('now')),
    kind    TEXT    NOT NULL,
    pr_key  TEXT    NOT NULL DEFAULT '',
    path    TEXT    NOT NULL DEFAULT '',
    payload TEXT    NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_brain_events_pr   ON brain_events (pr_key, id);
CREATE INDEX IF NOT EXISTS idx_brain_events_kind ON brain_events (kind, id);

-- +goose Down
DROP INDEX IF EXISTS idx_brain_events_kind;
DROP INDEX IF EXISTS idx_brain_events_pr;
DROP TABLE IF EXISTS brain_events;

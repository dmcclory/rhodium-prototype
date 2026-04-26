# Brain migrations

The brain db (`~/.local/share/rhodium/brain.db`, overridable via
`$RHODIUM_BRAIN`) is schema-migrated with [goose][goose]. Migration files
in this directory are embedded into the binary via `//go:embed` and
applied on every `LoadBrain()` call. End users never run a separate
migration command.

[goose]: https://github.com/pressly/goose

## Adding a migration

1. Create a new file named `NNNNN_short_name.sql` in this directory,
   where `NNNNN` is the next zero-padded integer (e.g. `00002_...`).
2. Structure it with goose annotations:

   ```sql
   -- +goose Up
   ALTER TABLE notes ADD COLUMN priority INTEGER NOT NULL DEFAULT 0;

   -- +goose Down
   -- SQLite can't drop columns before 3.35; leave empty or omit if
   -- rollback isn't meaningful.
   ```

3. `go build ./...` — the new file is picked up automatically because
   the whole directory is embedded via `//go:embed migrations/*.sql`.
4. First run of any rhodium command applies it and records its sha256
   into `brain_migration_hashes`.

### SQLite ALTER TABLE gotchas

- Adding columns is safe; dropping and renaming columns require SQLite
  ≥ 3.35 (modernc.org/sqlite is modern enough).
- Changing a column's type needs the "create new table, copy rows,
  drop old, rename" dance — goose will run that fine inside one
  migration, just write it out explicitly.
- If a migration can't run in a transaction (rare), annotate with
  `-- +goose NO TRANSACTION`. **Avoid this when possible** — it's the
  one case where a failed migration can leave the DB half-migrated,
  and our backup guard is the only safety net.

## Guardrails (all in `brain.go`)

Everything below runs inside `runMigrations(db, path)` on every
`LoadBrain()` call. They're ordered so each later step can assume the
earlier checks passed.

### 1. `bootstrapPreGooseDB`

Handles databases created before goose was introduced — the initial
`brain.db` had a schema maintained via inline `CREATE TABLE IF NOT
EXISTS` + ad-hoc `ALTER TABLE` guards for `resolved_at` and `source`.
If a DB has a `notes` table but no `goose_db_version` table, this
function applies any of those historical column-adds that are still
missing, so that when `goose.Up` runs it finds every table exactly at
the v1 schema and stamps cleanly at version 1.

Can be deleted once you're confident no pre-goose DBs exist in the
wild — for a solo tool that's "once you've run the new binary once on
every machine you care about."

### 2. `checkBrainNotAhead` (downgrade guard)

Refuses to open a DB whose `goose_db_version` exceeds the newest
migration this binary ships with. This catches the "old binary
against a newer DB" case, which would otherwise silently read/write
against columns the code doesn't know exist.

Error message points at `$RHODIUM_BRAIN` and tells the user to
upgrade rhodium or redirect to a different DB.

### 3. `checkMigrationHashes` (pre-Up) / `recordMigrationHashes` (post-Up)

Side table `brain_migration_hashes(version_id, sha256)`, populated
on first apply. On every subsequent startup, for each applied
version, we recompute the sha256 of the embedded file and compare.

- **Mismatch** → refuse to start, naming the file and both hashes.
  This catches the subtle footgun of editing a migration after it
  was applied somewhere (goose would silently skip it on the
  already-migrated DB, leaving schema and code divergent).
- **Missing row** (applied version that pre-dates this feature) →
  trust on first sight, record the current hash.
- **Table doesn't exist yet** → created via `ensureHashTable` with
  `CREATE IF NOT EXISTS`. This is tooling infra, not app schema, so
  it intentionally lives outside the goose migration chain.

### 4. `backupBrainIfPending`

Before any real migration runs, if `current > 0` and `current < max`,
runs `VACUUM INTO '<path>.bak-v<current>'` to snapshot the DB. Skips
if the backup file already exists, so the **earliest** known-good
copy at each version survives across repeated runs (rather than
being overwritten by a later, possibly-broken state).

`VACUUM INTO` produces a single consistent SQLite file regardless of
WAL state, no separate `.wal`/`.shm` handling needed.

Backups accumulate and are not auto-pruned. Clean up manually with
`rm ~/.local/share/rhodium/brain.db.bak-v*` when you're confident
recent migrations are healthy.

## Inspecting state: `rhodium brain status`

```
rhodium brain status
rhodium brain status --json
```

Opens the DB **read-only** and reports path, current version, max
embedded version, pending count, ahead flag, hash mismatches, and
existing backups. Crucially, it does **not** run migrations — so it
works even when `LoadBrain` refuses to open the DB (downgrade guard
tripped, hash mismatch, etc). When something's wrong, this is the
first thing to run.

Implemented as `InspectBrain()` in `brain.go`, wired into `cmdBrain`
in `cli.go`.

## Recovery scenarios

| Symptom | Likely cause | Fix |
|---|---|---|
| `brain db at X is at schema version N, but this build only knows ... up to M` | Running an older rhodium against a DB migrated by a newer one | Upgrade rhodium, or `RHODIUM_BRAIN=<other-path>` |
| `content has since changed. Expected sha256 X, got Y` | A migration file was edited after being applied on this DB | Revert the edit, or delete the DB (restore from `*.bak-v*` if needed) |
| `migrate brain db: ...` with a SQL error | The migration itself is broken | Fix the SQL; the DB rolled back (assuming default transaction mode), so just rerun |
| Silent schema drift, no error | Probably an edit to a migration + a DB that predates the hash feature | `rhodium brain status` will flag mismatches once hashes are recorded; otherwise reset the DB |
| `bootstrap brain db: ...` | Pre-goose DB's schema diverges from what `bootstrapPreGooseDB` expects | Inspect the DB manually, or point `RHODIUM_BRAIN` at a fresh path and re-import data if necessary |

## File layout

```
migrations/
  README.md                         ← this file
  00001_initial_schema.sql          ← baseline (current end-state as of
                                       goose introduction)
  NNNNN_next_change.sql             ← add here
```

Filenames **must** start with `NNNNN_` (5-digit zero-padded integer)
followed by underscore, or `maxEmbeddedMigration()` and
`migrationFileAndHash()` won't find them.

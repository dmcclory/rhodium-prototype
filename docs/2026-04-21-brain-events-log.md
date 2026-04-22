# Append-only brain mutation log + `rhodium brain log`

Landed 2026-04-21, same day as segments+sessions but a separate pass.
First item off Tier 2 in `current_state_4_21.md`: an append-only
`brain_events` table that captures every brain-state mutation, plus a
CLI verb for reading it back. Load-bearing for future `brain replay`
and per-action undo; intentionally *not* wired into any reader yet.

Tables remain the source of truth for this pass. Events are a durable
side-record that ships alongside each state write in the same
transaction — not a projection we read back from.

---

## 1. Schema

### Migration

- `migrations/00003_brain_events.sql`

```sql
CREATE TABLE brain_events (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    ts      TEXT    NOT NULL DEFAULT (datetime('now')),
    kind    TEXT    NOT NULL,
    pr_key  TEXT    NOT NULL DEFAULT '',
    path    TEXT    NOT NULL DEFAULT '',
    payload TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_brain_events_pr   ON brain_events (pr_key, id);
CREATE INDEX idx_brain_events_kind ON brain_events (kind, id);
```

- `id` is the logical clock — strictly monotonic per brain, newest has
  the highest id.
- `ts` is for display only; never used as an ordering key.
- `pr_key` / `path` are hoisted out of the JSON payload so per-PR and
  per-file filters are indexable. Empty string for events that don't
  scope to one (e.g. `session.complete`, `scrutiny.set`).
- `payload` is a JSON blob whose shape varies per kind. Payloads are
  produced by `json.Marshal` at write time.

Append-only by contract — no `UPDATE` or `DELETE` is ever issued. If
the table grows unbounded, that's a future pruning problem, not a
correctness one.

### Deliberate non-members

`pr_cache` writes are *not* logged — that table mirrors GitHub and
isn't part of the reviewer's brain state.

---

## 2. How events are written

### `logEvent` helper

```go
type execer interface {
    Exec(query string, args ...any) (sql.Result, error)
}

func logEvent(x execer, kind, prKey, path string, payload any) error
```

The `execer` interface is satisfied by both `*sql.DB` and `*sql.Tx`.
Every mutator passes its `*sql.Tx` so the event shares the state
write's atomicity — a rollback kills the event too, so there are no
orphan rows.

Payloads are `any`; `nil` serializes to `"{}"`. No per-kind schema
enforcement at write time; shape is documented in the kind table
below and readers unmarshal at their discretion.

### Mutators that log events

Every `Brain` method that changes brain state logs at least one event.
Methods whose prior signature used `b.db.Exec` directly were converted
to a `tx.Begin` / `tx.Commit` pair to carry the event. Methods that
were already transactional got the event appended inside their
existing tx.

| Method                                 | Event(s) emitted                                 | Notes                                                                                                                                                                      |
|----------------------------------------|--------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `SetHunkMarks(repo,pr,path,marks)`     | `mark.set` per newly-on hash, `mark.clear` per newly-off hash | Snapshots the prior row set inside the tx, diffs against the incoming map. Bulk replace decomposes into per-hunk events — cheap, and makes per-hunk undo trivial later. A no-op write (same set) emits no events. |
| `SaveNote` / `SaveAgentNote`           | `note.add`                                       | Shared `insertNote` tx. Payload carries `note_id`, `line_no`, `line_hash`, `source`, `body` — the full body so replay can resurrect without the notes row. |
| `ResolveNote(id)`                      | `note.resolve`                                   | Idempotent: missing id or already-resolved → no event. pr_key/path looked up from the notes row so the event scopes correctly.                                              |
| `DeleteNote(id)`                       | `note.delete`                                    | Read-before-delete in a tx. Payload carries every field of the deleted row (`body`, `line_hash`, `source`, `resolved_at` when set) so an undo/replay can fully reconstruct the note. Missing id → no event. |
| `SetFileReviewed(repo,pr,path,h,b)`    | `file.reviewed`                                  | Payload `{head_sha, base_sha}`.                                                                                                                                             |
| `SetScrutiny(repo,pr,on)`              | `scrutiny.set`                                   | Payload `{on: bool}`. PR-scoped, empty `path`.                                                                                                                              |
| `CreateSession(...)`                   | `session.complete` (if superseding) + `session.create` | If an active session for the PR existed, it gets completed in-tx and a `session.complete` with `reason:"superseded"` is logged alongside the new `session.create`. Payload carries the full files list — replay reproduces the same snapshot. |
| `SetSessionFileDone(id,path,done)`     | `session.file.done` (+ `session.complete` on auto-transition) | Two events in one tx when the toggle auto-completes the session. The `session.complete` carries `reason:"auto"`. Checked via `RowsAffected` on the auto-complete UPDATE so the event only fires when the row actually transitioned. |
| `CompleteSession(id)`                  | `session.complete` (only on actual transition)   | Reads `completed_at` first; if already set, the UPDATE is still issued (original idempotent behaviour preserved) but no event fires. Explicit calls carry `reason:"manual"`. |

### Invariant: idempotent no-ops emit no events

Every mutator that had a "resolve-missing-id is a no-op" or
"already-completed is a no-op" contract preserved it *at the event
layer too*. Log readers see one event per *actual* state change, not
per *call*. This matters for a future replay: feeding the log back
should produce the same table state regardless of how many duplicate
calls the original session made.

---

## 3. Reader surface — `RecentEvents`

```go
type Event struct {
    ID      int64
    TS      string
    Kind    string
    PRKey   string
    Path    string
    Payload string   // raw JSON
}

type EventFilter struct {
    PRKey      string
    KindPrefix string   // SQL LIKE prefix match — "mark." grabs mark.set and mark.clear
    Limit      int      // <= 0 defaults to 100
}

func (b *Brain) RecentEvents(filter EventFilter) []Event
```

Newest-first. Payload returned as the raw stored JSON string; callers
that want a typed view unmarshal themselves. The filter is the union
of conditions — `PRKey=""` plus `KindPrefix=""` returns the most
recent N across the whole brain.

Tests in `brain_test.go` cover each mutator's event shape, supersede
semantics, idempotence (no-op calls emit no events), and the three
filter axes. Filename: `TestBrainEvents{Marks,Notes,FileReviewsAndScrutiny,Sessions,Filter}`.

---

## 4. CLI surface — `rhodium brain log`

```
rhodium brain log [--pr owner/repo#N] [--kind PREFIX] [--limit N] [--json]
```

Newest-first, columns aligned via `text/tabwriter`:

```
#3  2026-04-21 14:32:11  note.resolve  acme/web#42  src/main.go  {"note_id":1}
#2  2026-04-21 14:32:11  note.add      acme/web#42  src/main.go  {"body":"hello","line_hash":"","line_no":10,"note_id":1,"source":"human"}
#1  2026-04-21 14:32:02  mark.set      acme/web#42  src/main.go  {"hunk_hash":"abc123"}
```

`--json` emits JSONL (one event per line) with the payload unmarshalled
into a real object — that's the shape a future `rhodium brain replay
<file>` would consume. Payload field ordering is alphabetized because
the text path re-serializes through a generic `map[string]any`; the
stored bytes aren't modified.

Empty-log state prints `brain log: no events` (text) or nothing (JSON).

### Gotcha uncovered while wiring this

`splitFlags` in `cli.go` mis-handles value-taking flags: it treats the
value (anything not starting with `-`) as positional, so `--limit 20`
arrives at `flag.Parse` as `--limit` with `20` stranded in the
positional list. Every pre-existing subcommand happens to use only
bool flags so the bug was latent. `cmdBrainLog` parses `args` directly
via `fs.Parse(args)` and leaves a comment pointing at the issue. Worth
fixing `splitFlags` properly if we grow more string/int flags.

---

## 5. Deferred / future work

- **`brain replay <file>`.** The JSONL output from `brain log --json`
  is a complete description of every brain mutation; a replay command
  could consume it against a fresh DB and reproduce state. The
  `note.add` and `note.delete` payloads carry enough content for this
  to work without consulting anything else. Blocked on deciding what
  to do about `note_id` collisions (new autoincrement vs. preserving
  the original); leave for when we actually need it.
- **Undo.** "Undo the last N brain events for a PR" is a thin layer
  over `RecentEvents(PRKey=...)` + a per-kind inverse. Unblocked now;
  separate UX question (what does the TUI surface look like?).
- **Tables populated from events.** Iron's model is "events are the
  truth, tables are a projection." That flip is doable but invasive
  and yields no user-visible win today. Defer until we have a reason
  (sync, history-bisect debugging, or both).
- **`pr_cache` events.** Still deliberately out. If we ever want to
  audit "when did this PR vanish from the list," a separate
  `pr_cache_events` table is cheaper than conflating it with brain
  state.
- **Log pruning.** Not needed yet — expected volume is tens of events
  per review session. If it becomes an issue, a simple `DELETE WHERE
  id < (SELECT MAX(id) - N FROM brain_events)` sweep is enough.
- **`brain show / clear / forget`.** Still Tier 2, still open. `show`
  is natural now — "`brain log` is the audit trail; `brain show` is
  the current projection" — but not yet wired.

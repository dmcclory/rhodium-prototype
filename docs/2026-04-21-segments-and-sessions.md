# Per-segment rendering and review sessions

Landed 2026-04-21. Covers two features wired up in the same pass:

1. **Per-segment diff rendering** with alt-view cycling (`v`), so complex
   Diff4 classes (rebase, conflict, …) are displayed as one reviewable
   chunk per segment instead of one whole-file patch.
2. **Review sessions as first-class objects**, replacing the narrow
   `catch_up_sessions` counter with a stable snapshot of the diff set +
   per-file progress that survives app restarts.

Both sit on top of the Diff4 machinery from the earlier M1 work; neither
changes how `file_reviews` (the brain advance table) is written — that
stays per-file writethrough. See
`memory/project_session_gate_semantics.md` for why gate-at-completion
was deferred.

---

## 1. Per-segment rendering (phase 1)

### What's new

Previously, opening a file in catch-up mode called `Classify()` on the
four diamond corners and either:

- auto-advanced (Hidden classes),
- rendered the PR's whole-file unified patch (`ShownAsDiff2`), or
- fell back to the same whole-file patch with a warning (complex
  classes).

Now, for complex classes we run `ComputeSlow()` and render **one
synthetic segment header + per-segment diff2 hunks**. Each segment is
an independent reviewable chunk.

### Files / key entry points

- `diff2.go`
  - `diff2Hunks(from, to string) []Hunk` — unified-diff hunks (ctx=3,
    adjacent windows merged) computed off `PatienceMatches`.
  - `segmentHunks(segs []Segment, viewIdx int) []Hunk` — for each
    `Segment`, emits a synthetic header hunk (`Hash == ""`, unmarkable)
    followed by diff2 hunks between the two corners chosen by the
    segment's current View.
  - `maxSegmentViews(segs []Segment) int` — how many views the most
    complex segment exposes; drives the `v: cycle view (N/M)` footer.
- `diff2_test.go` — 8 tests covering empty, identical, insert/delete,
  replacement, close-merge, distant-split, trailing-newline cases.
- `hunks.go`
  - `Hunk.isMarkable()` — true iff `h.Hash != ""`. Synthetic headers
    are unmarkable; all mark/nav logic gates on this.
- `view_diff.go`
  - Added `segments []Segment`, `segmentViewIdx int`, `segmented bool`.
  - `redraw()` skips `renderFullFile` when `segmented` (hunks already
    contain their own bodies).
  - `onDiamondClassified` branches: Hidden → auto-advance;
    `ShownAsDiff2` + `msg.patch != ""` → whole-file patch path;
    complex → `segmentHunks`.
  - `firstUnmarked`, `allMarked`, footer count, mark-all, toggle-mark,
    `stepHunk(delta)` all filter on `isMarkable()` so synthetic headers
    are skipped.
  - Note action gates on `view.To != F2` in segmented mode — notes only
    attach when the current view is looking at the final-head corner
    (consistent with "the reviewer is annotating the landed code").
  - Footer includes `N segments` and, when applicable, `v: cycle view
    (idx/max)`.
- `messages.go`
  - `diamondClassifiedMsg` carries `result *Result` so the slow-path
    segments reach the view without a second compute.

### Phase 2 — alt-view cycling

`v` key bound to `cycle-view`: increments `segmentViewIdx` modulo the
per-segment view count and rebuilds hunks via `segmentHunks`. Each
segment independently maps its current view index onto whichever of its
Views is valid (wrapping if `segmentViewIdx` exceeds that segment's
views), so cycling one key varies each segment's lens simultaneously.

### Known behavior / gotcha

Marks are keyed on `hashHunkBody(+/-lines)`. When `v` cycles views, the
rendered +/- lines change, so the hash changes, so **marks don't
transfer across views**. Cycling back to the originating view restores
them (deterministic hashes).

Whether this is "right" depends on whether cycling is "same change,
different lens" (segment-atomic) or "tick each angle separately" (hunk
× view). Deferred until real use; see
`memory/project_segmented_mark_unit.md` for the tradeoffs and a ~40 LoC
implementation sketch if we flip to segment-level marks.

---

## 2. Review sessions as first-class objects

### What's new

`catch_up_sessions` (a bare counter: `files_total`, `files_done` for a
`(repo, pr, old_head → new_head)` tuple) is gone. In its place:
`review_sessions` + `review_session_files`, which snapshot the actual
set of paths the reviewer still owes work on.

### Migration

- `migrations/00002_review_sessions.sql`
  - Creates `review_sessions` (`pr_key`, `head_sha`/`base_sha` for the
    start corners, `goal_head`/`goal_base` for the advance target,
    timestamps).
  - Creates `review_session_files` (`session_id`, `path`, `class`,
    `done`; primary key `(session_id, path)`).
  - Drops `catch_up_sessions` on Up; Down re-creates it.

The `goal_head` / `goal_base` columns are unused by current code —
they're seeded at session creation and sit there for the future
gate-at-completion flip, when `CompleteSession` would iterate
`review_session_files` and call `SetFileReviewed(goal_head, goal_base)`
for each row. See `memory/project_session_gate_semantics.md`.

### Brain API (`brain.go`)

```go
type ReviewSession struct {
    ID, FilesTotal, FilesDone int64/int
    PRKey, HeadSHA, BaseSHA, GoalHead, GoalBase,
    StartedAt, CompletedAt string
}

type SessionFile struct { Path, Class string; Done bool }

ActiveSession(repo, pr) *ReviewSession     // most recent with completed_at IS NULL
CreateSession(repo, pr,
    headSHA, baseSHA,                      // start corners (reviewer's last-seen)
    goalHead, goalBase string,             // advance target (PR's current)
    files []SessionFile) (*ReviewSession, error)  // completes any prior active session
SetSessionFileDone(sessionID, path, done)  // auto-completes when last file done
CompleteSession(sessionID)                 // manual completion regardless of files
SessionFiles(sessionID) []SessionFile      // insertion-ordered snapshot
AllActiveSessions() []ReviewSession
```

`FilesTotal` / `FilesDone` are **denormalized** from `review_session_files`
on every read via `hydrateSessionCounts` — not stored on the session row.

### Wiring

- `commands.go:autoAdvanceCmd`
  - Partitions reviewed files into `drifted`, `forgotten`, `advanced`,
    `unresolvedPaths` (as before).
  - When `len(unresolvedPaths) > 0` and the active session either
    doesn't exist or has stale `GoalHead`/`GoalBase` → creates a new
    session with the unresolved paths as its `[]SessionFile` snapshot.
    Matching-goal sessions are resumed.
  - Silently-advanced paths that happen to already be in the active
    session (e.g. a pre-existing session covered them) get
    `SetSessionFileDone(…, true)` so the counter stays consistent with
    `file_reviews`.
- `view_diff.go:saveMarks`
  - After persisting hunk marks + `SetFileReviewed`, if `allMarked()`
    returns true, calls `app.markSessionFileDone(selectedFile)`.
- `view_diff.go:onCatchUpLoaded` / `onDiamondClassified`
  - The Hidden / auto-caught-up branches call
    `app.markSessionFileDone(selectedFile)` (replacing the old
    `advanceCatchUpSession()`).
- `app.go`
  - Field renamed `catchUpSession → reviewSession`.
  - `openPR` and `onAutoAdvance` refresh `a.reviewSession` via
    `ActiveSession`.
  - `markSessionFileDone(path string)` replaces
    `advanceCatchUpSession()` — no-op when no session, otherwise
    `SetSessionFileDone` + re-hydrate (so the session becomes `nil`
    once auto-completed).
- UI consumers (`view_prs.go`, `view_todo.go`, `cli.go`) were renamed
  in place — same fields (`FilesDone`/`FilesTotal`), same semantics.
- `brain_test.go:TestBrainReviewSessions` exercises `CreateSession`,
  `ActiveSession`, `SessionFiles` round-trip, `SetSessionFileDone`,
  auto-complete on last file, manual `CompleteSession`, and the
  "create implicitly completes prior active" invariant.

### Semantics summary

- A session exists **when and only when** there is outstanding catch-up
  work. Opening a fresh PR does not create one; cleanly reviewed PRs
  never have one.
- A session spans one (head → goal-head, base → goal-base) advance. If
  the PR moves (`pr.HeadSHA` / `pr.BaseSHA` ≠ session's `GoalHead` /
  `GoalBase`) a new autoAdvance completes the stale one and opens a
  fresh session with paths recomputed at the new corners.
- `file_reviews` is still written **per mark-save**, independent of
  session state. Mid-session progress shows up in every UI surface
  immediately. Sessions are an orthogonal layer of "here's the stable
  work list for this catch-up pass."

---

## Deferred / future work

- **Gate-at-completion.** `CompleteSession` already exists; flip
  `saveMarks` to stop calling `SetFileReviewed` and have
  `CompleteSession` do it for every `review_session_files` row. UI
  "file reviewed" checks would need to union `file_reviews` with the
  active session's `done=1` files to keep mid-session progress
  visible. Columns `goal_head`/`goal_base` already carry the target.
- **Segment-level marks.** Switch marks from diff2-hunk hash to
  segment hash (hash of the four corner contents) so they survive `v`
  cycling. ~40 LoC; gated on dogfooding the current behavior first.
- **Broader session scope.** Today sessions snapshot only the
  unresolved paths (preserving the old "catch-up session" semantic).
  A future change could snapshot the full current diff set on every
  PR open, giving stability-across-force-push for the entire review
  pass, not just catch-up.

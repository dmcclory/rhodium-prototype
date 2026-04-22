# `rhodium log` — per-commit review overlay

Landed 2026-04-21. Third Tier 2 item from `current_state_4_21.md` — a
CLI view that shows a PR's commits with per-commit review status
overlaid onto the existing content-hashed marks. No new storage; pure
projection over `hunk_marks` + `gh api`.

Context: this was chosen over the "expensive" per-commit Diff4 path
after first-principles discussion about rebase handling. The
content-hashed mark primitive is already rebase-stable; a log view
built on top is naturally rebase-friendly because it doesn't key on
commit SHAs at all. Commit-SHA-keyed state would have regressed
rebase survival, not improved it — which is the opposite of what a
rebase-heavy user wants. See section 4 for the documented caveat
that this approach does accept.

---

## 1. Command surface

```
rhodium log <owner/repo#N> [--verbose] [--json]
```

Default text output is one row per commit, newest first (reversed from
GitHub's oldest-first order so it mirrors `git log`):

```
abc1234  ✓  1/1   Fix typo in README.md                 alice  2h ago
def5678  ◐  2/3   Add logging                           alice  2h ago
9a0bcde  ✓  5/5   Initial commit                        bob    1d ago
```

Columns via `text/tabwriter`: short SHA, glyph, `marked/total` hunk
ratio, truncated title (50 chars), author login, coarse relative time.

`--verbose` indents a per-file breakdown under each commit row:

```
def5678  ◐  2/3   Add logging                           alice  2h ago
                  ◐  1/2                                logger.go
                  ✓  1/1                                main.go
```

`--json` emits a structured object with `files` nested per commit —
useful shape for piping into other tools or for eventually feeding a
per-commit TUI view.

---

## 2. Data layer additions (`github.go`)

Two new thin wrappers around `gh api`, shaped to match the existing
`listPRFiles` / `fetchCompare` style:

- `listPRCommits(repo, number) []Commit` — paginated call to
  `pulls/:n/commits`. Returns a flattened `Commit` struct (SHA, Title,
  Message, Author, Date) rather than exposing the two-level
  `{sha, commit: {...}}` shape the API returns.
- `fetchCommitFiles(repo, sha) []FileChange` — single-commit endpoint
  (`commits/:sha`), reuses the existing `ghAPIFile` DTO and
  `FileChange` return type so patches flow into `parseHunks`
  unchanged.

`Author` prefers the GitHub login when available and falls back to the
commit.author.name for commits pushed by someone without a linked
account. `Date` is stored as the raw RFC3339 timestamp and formatted
on render.

No caching. N commits × one `gh api` call each = N round-trips per
render. For review-scale PRs (dozens of commits) that's acceptable
CLI latency; a parallel fan-out is the obvious follow-up if anyone
runs it against a hundred-commit branch.

---

## 3. Overlay — pure function, where the logic lives

```go
func overlayCommitStatus(c Commit, files []FileChange,
    marksByPath map[string]map[string]bool) commitStatus
```

Factored out of the command so the interesting part is unit-testable
without mocking gh. Takes:

- the commit (for SHA/title/author/date)
- the files the commit introduced (for `Patch`)
- the PR-level marks map, built once at command start as
  `path → HunkMarks(repo, num, path)`

and returns a `commitStatus` with aggregate `Marked/Total` plus a
per-file breakdown. Hunks are parsed with the same `parseHunks` the
brain uses, so hashes are drawn from the same content-addressed space
as `hunk_marks`.

Merge commits commonly arrive with empty patches. `Total == 0`
commits render with a blank glyph — distinct from "unreviewed" (also
blank by convention but with `0/N > 0`) in the ratio column. Not
distinct in the glyph alone, which is a known rough edge.

---

## 4. The caveat, pinned by test

Marks key on `+/- `content hash only. If commit A introduces a hunk and
a later commit B rewrites that hunk mid-branch (fixup, revert, partial
undo) before the PR is rebased-and-squashed, the reviewer's mark
against the final-PR form hashes differently from A's original. A
shows as `0/N` even when the *effect* has been reviewed.

This is the documented approximation, locked in by
`TestOverlayCommitStatusRewrittenHunkCaveat` in `cli_log_test.go`:
if the test ever flips, the hash primitive has changed and the caveat
should be reassessed.

### Why we accept it

Any more precise answer requires either:

1. **Commit-SHA-keyed state** — breaks under rebase, which is
   precisely the workflow rhodium is trying to be good at.
2. **Line-lineage via `git blame`** — requires a local worktree for
   the PR, a fork-PR story, and a secondary "superseded lines count
   as reviewed?" UX decision.
3. **Full Diff4-per-commit** — the 15-class classifier applied across
   the branch history, essentially a brain-per-commit. Significant
   structural change.

In practice, heavy rebasers squash fixup commits before review, which
dissolves the caveat: the final clean history has no rewritten-within
hunks. The approximation bites mainly when reviewing an unrebased
fixup-heavy branch, which is less common in well-maintained review
flow.

### What the caveat doesn't affect

- **Context drift.** A hunk whose surrounding context shifted (insert
  earlier in the file) still matches — `hashHunkBody` is `+/-` only.
- **Straight rebase onto a new base.** `+/-` bytes don't change, so
  marks continue to match. `rhodium log` on a freshly-rebased PR
  shows the same status it showed before the rebase.
- **Merge commits.** Rendered with `0/0` and a blank glyph; never
  spuriously "unreviewed".

---

## 5. Tests

`cli_log_test.go` exercises `overlayCommitStatus` against six shapes:

- `TestOverlayCommitStatusFullyReviewed` — all hunks marked → ✓
- `TestOverlayCommitStatusPartiallyReviewed` — some hunks marked → ◐
- `TestOverlayCommitStatusUnreviewed` — no marks → blank
- `TestOverlayCommitStatusMergeCommitNoPatch` — empty patch → 0/0 blank
- `TestOverlayCommitStatusRewrittenHunkCaveat` — pins the documented
  approximation case
- `TestOverlayCommitStatusAggregatesAcrossFiles` — ratios sum across a
  commit's files, order preserved

No CLI-level tests (existing convention: CLI is a thin shell, logic
lives in functions that get unit-tested). `go vet` + the overlay
tests + `go build` cover the interesting regressions.

---

## 6. Deferred / future work

- **Parallel commit file fetches.** Straightforward fan-out with a
  bounded goroutine pool. Not needed today; ship-simple-then-measure.
- **`rhodium glog`.** Iron's graph-shaped analogue; not yet. Would
  most naturally be a TUI view rather than a CLI verb.
- **Per-hunk blame ("which commit introduced this hunk").** The
  opposite direction — given a marked hunk, find the commit that
  added those `+` lines. Useful for the "I marked this, now show me
  the commit that caused it" workflow. Needs a worktree or `gh api
  commits/:sha/patches` iteration; defer until a concrete use case
  asks for it.
- **Upgrade to blame-based line lineage.** If the documented caveat
  starts biting in practice, the middle path is `git blame HEAD`
  against the PR worktree (when available) to map commit lines to
  their surviving-at-HEAD positions, then intersect with marked hunk
  ranges. Keeps marks rebase-stable while making the per-commit
  approximation precise for non-rewrite cases. Has a fork-PR / no-
  worktree fallback story to design.
- **Per-commit note overlay.** Today we show mark ratios; we could
  also show "K notes on lines this commit introduced." Cheap add,
  same data plumbing.

# rhodium

Prototype of a code-review tool inspired by Jane Street's Iron — a TUI +
CLI with a persistent "brain" of review state (hunk-level marks that
survive rebases, per-file catch-up diffs, inline notes) that a reviewer
can live in across many PRs and many sessions.

## Install

If you just want a working `rhodium` binary on your `$PATH`, the install
script downloads the latest release, verifies its checksums, and drops
the binary in `~/.rhodium/bin` (added to your shell's path):

```
gh api -H 'Accept: application/vnd.github.v3.raw' \
  "repos/dmcclory/rhodium-prototype/contents/install.sh" | bash
```

Requires `gh` (and either `sha256sum` or `shasum` — macOS ships the
latter). Open a new shell after install so the PATH update takes effect.

## Build

```
make build          # → bin/rhodium
make test           # go test ./...
make vet            # go vet ./...
make run            # build + run the TUI
make install        # go install to $GOBIN
make clean          # remove bin/
```

Or directly, without the Makefile:

```
go build -o bin/rhodium .
```

(`go build ./...` without `-o` drops the binary in the repo root, which is
why `bin/` is the conventional target and is gitignored.)

## Configure

Config lives at `~/.config/rhodium/config.json` (override with
`$RHODIUM_CONFIG`).

Minimal example:

```json
{
  "repos": ["cli/cli", "owner/other-repo"]
}
```

For configuring agent harnesses (Claude, opencode, custom scripts) and
defining new actions, see
[docs/configuring-harnesses.md](docs/configuring-harnesses.md).

Full example with everything the nvim integration needs:

```json
{
  "repos": ["cli/cli"],
  "repo_paths": {
    "cli/cli": "/Users/dan/code/cli/cli"
  },
  "worktree": {
    "root": "~/rhodium/worktrees"
  },
  "tmux": {
    "mode": "window"
  },
  "editor": {
    "command": "nvim"
  },
  "default_pr_view": "files"
}
```

`default_pr_view` chooses which lens a PR opens on: `"files"` (default) or
`"commits"` (the glog commit-log view with per-commit review status). The `g`
key toggles between the two per-session regardless of this default.

### Paths to repos

For the `o` (open in editor) action to create a per-PR worktree, rhodium
needs to know where each repo's local clone lives. Resolution order:

1. `config.repo_paths["owner/repo"]` — explicit, recommended.
2. `$RHODIUM_REPOS_ROOT/<owner/repo>` if set.
3. `~/code/<owner/repo>` by default.

If a repo can't be found at any of these, `o` errors with a message
telling you which path it tried. Clone once with `gh repo clone`, then
either set `repo_paths` or drop the clone under the convention path.

### Worktree layout

Per-PR worktrees land under:

```
<worktree.root>/<owner>-<repo>/pr-<N>
```

First `o` on a PR runs `git worktree add --detach` against the local
clone, then `gh pr checkout <N>` inside the worktree (so fork PRs work
too). Subsequent `o`s reuse the existing worktree.

Worktrees aren't garbage-collected — clean them up with
`git worktree remove` when you're done with a PR.

### tmux modes

When `$TMUX` is set, `o` launches the editor through tmux. Config:

- `"window"` (default) — `tmux new-window`, nice tab per PR.
- `"split-h"` — horizontal split (side-by-side with the TUI).
- `"split-v"` — vertical split.
- `"off"` — suspend the TUI and run the editor inline, even inside tmux.

In every tmux mode, rhodium spawns the pane as your default interactive
shell (tmux's normal behavior) and then `send-keys` the nvim command
into it. This preserves full job control: `Ctrl-Z` suspends nvim back
to the shell, `fg` resumes it, and the pane persists until you `exit`.
When you quit nvim you're at a prompt in the worktree — handy for
`git status`, running tests, or re-launching nvim.

When `$TMUX` isn't set, rhodium uses the inline fallback
(`tea.ExecProcess`): TUI suspends, editor runs, TUI resumes on exit.

## Usage

### TUI

```
rhodium
```

- Todo dashboard → PRs list → files → diff.
- In diff view: `space`/`x` mark hunk + advance, `m` mark all, `u`
  unmark all, `c` add a note, `P` publish the note at the cursor as a
  GitHub inline comment, `d` toggle catch-up vs full diff, `o` open the
  current file in nvim at the current hunk.
- While writing a note (`c`), press `ctrl+a` to pick a contributor to
  @-mention — the list is fetched from GitHub the first time you use it
  in a session and cached after.
- In PR list view: `A` opens a review modal (approve / request-changes /
  comment-only). `tab` cycles the event type; `ctrl+s` submits.
- While a PR is selected, rhodium polls the brain every ~500ms so
  marks made in a separate nvim (tmux split/window) show up live.

### CLI

```
rhodium todo [--sync] [--json]           dashboard of PRs with pending work
rhodium notes <owner/repo#N> [--json]    all notes for a PR
rhodium state <owner/repo#N>             full review state as JSON
                                         (files, hunks with marks, notes)
rhodium mark <pr> <file> <hunk-hash>     mark a single hunk reviewed
rhodium unmark <pr> <file> <hunk-hash>   unmark
rhodium note <pr> <file> <line> <body>   add a note ("-" reads stdin)
```

The JSON-emitting commands (`state`, `mark`, `unmark`, `note`) are what
the nvim plugin shells out to — you can also use them standalone to
script against review state.

## Nvim plugin

The plugin lives at `editor/nvim/rhodium.lua`. When you press `o` in the
TUI, rhodium invokes nvim with `g:rhodium_pr` set to the PR key. The
plugin reads that, fetches state, and renders per-hunk signs
(`!` unreviewed, `✓` reviewed), virtual-text tags, and notes as
`virt_lines` above their anchor.

### Install

Either:

- **Symlink or copy** to your nvim runtimepath, e.g.:
  ```
  ln -s $PWD/editor/nvim/rhodium.lua ~/.config/nvim/plugin/rhodium.lua
  ```
- **Or** set `RHODIUM_NVIM_PLUGIN=/abs/path/to/rhodium.lua`. The rhodium
  binary will pass `-c "luafile <path>"` to nvim on launch.
- **Or** install the rhodium binary next to the `editor/` directory from
  this repo — the launcher auto-detects that case.

### Keymaps (buffer-local, only when `g:rhodium_pr` is set)

| Key          | Action |
|--------------|--------|
| `]h` / `[h`  | next / previous unreviewed hunk |
| `<leader>rm` | mark hunk at cursor reviewed |
| `<leader>ru` | unmark hunk at cursor |
| `<leader>rn` | add a note at the current line (prompts for body) |
| `<leader>rN` | jump to next unreviewed file in the PR |
| `:RhodiumRefresh` | re-fetch state from the brain and rerender |

## Data

All review state lives in a SQLite DB at
`~/.local/share/rhodium/brain.db` (override with `$RHODIUM_BRAIN`).
WAL mode is on, so the TUI and nvim can write concurrently.

Schema changes are managed with [goose][goose] migrations embedded in
the binary. See [`migrations/README.md`](migrations/README.md) for
how to add one and how the bootstrap / downgrade / hash / backup
guardrails work. `rhodium brain status` inspects the DB without
running migrations — handy when something's wrong.

[goose]: https://github.com/pressly/goose

## Environment variables

| Variable | Purpose |
|----------|---------|
| `RHODIUM_CONFIG`       | path to config.json |
| `RHODIUM_BRAIN`        | path to the SQLite brain |
| `RHODIUM_REPOS_ROOT`   | fallback root for per-repo clones |
| `RHODIUM_NVIM_PLUGIN`  | absolute path to `rhodium.lua` |
| `RHODIUM_BIN`          | used by the nvim plugin to find the CLI (defaults to `rhodium` on `$PATH`) |

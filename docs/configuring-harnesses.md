# Configuring agent harnesses

Rhodium has two configurable primitives that together make up a "harness":

- An **agent** is the binary that runs the LLM (e.g. `claude`, `opencode`,
  `aider`, or your own wrapper script). It knows how to invoke itself.
- An **action** is a keybinding + prompt-template pair that picks one of
  those agents and hands it a rendered prompt. It knows *what kind of
  conversation* to start.

This split means swapping LLMs (`default_agent`) leaves your actions alone,
and adding a new action (e.g. a security-focused first-pass review) doesn't
require touching agent config.

Everything lives under `~/.config/rhodium/config.json` (or `$RHODIUM_CONFIG`).

## Defaults

If you don't define any agents or actions, rhodium ships two built-ins so
`t` and `f` work out of the box:

- Agent `claude` → command `claude`, with `-p` for oneshot mode.
- Action `chat` on `t` — interactive tmux pane, seeded with a PR overview.
- Action `first-pass` on `f` — oneshot JSON review, results stored as notes.

User config **replaces** the defaults, it doesn't deep-merge. If you define
any agents or any actions, you own the whole list.

## Agent config

```json
{
  "agents": [
    { "name": "claude",   "command": "claude",   "oneshot_args": ["-p"] },
    { "name": "opencode", "command": "opencode", "oneshot_args": ["run"] }
  ],
  "default_agent": "opencode"
}
```

| Field          | Meaning                                                            |
|----------------|--------------------------------------------------------------------|
| `name`         | Stable id referenced by `default_agent`. Shown in the status bar. |
| `command`      | Binary on `$PATH` or absolute path.                                |
| `oneshot_args` | Flags added in `oneshot` mode so the binary reads prompt on stdin. |

In **interactive** mode (`delivery: tmux`), rhodium runs the agent as
`<command> "$(cat <prompt-file>)"` — i.e. the rendered prompt arrives as
a single positional arg. This works for agents like Claude whose interactive
mode accepts a seed prompt as argv.

In **oneshot** mode (`delivery: inline-notes`), rhodium runs
`<command> <oneshot_args...>` with the prompt piped on stdin.

`default_agent` picks which agent actions use when there are several. If
unset, the first agent wins.

## Action config

```json
{
  "actions": [
    {
      "key": "t",
      "name": "chat",
      "mode": "interactive",
      "worktree": true,
      "context": "paths",
      "delivery": "tmux",
      "prompt_template": "You're helping review PR {{.Repo}}#{{.Number}}: {{.Title}}\nAuthor: {{.Author}}\nWorktree (cwd): {{.Worktree}}\n\nChanged files:\n{{.FileList}}\n\nPR description:\n{{.Body}}\n\nRead whichever files seem relevant and discuss the change with me."
    }
  ]
}
```

| Field             | Meaning                                                                 |
|-------------------|-------------------------------------------------------------------------|
| `key`             | Single keypress that fires the action when a PR is selected.            |
| `name`            | Stable id; shows up in status messages and the help overlay.            |
| `mode`            | `interactive` or `oneshot`. Controls how the agent is invoked.          |
| `worktree`        | If true, resolve/create the per-PR git worktree and pass its path.      |
| `context`         | `paths` (just file list) or `patches` (concatenated unified diffs).     |
| `delivery`        | `tmux` for interactive panes, `inline-notes` for oneshot-to-JSON.       |
| `prompt_template` | Go `text/template` rendered against [PromptCtx](#template-variables).   |

### Mode / delivery combinations

Two combinations are supported today:

- **`interactive` + `tmux`** — spawn a new tmux pane/window in the worktree
  and send the rendered prompt as argv. Outside tmux, falls back to
  suspending the TUI and running the agent inline (`tea.ExecProcess`).
- **`oneshot` + `inline-notes`** — run the agent with the prompt on stdin,
  parse stdout as a JSON array of `{path, line, body}` entries, and store
  each as a note tagged `source=agent`.

Other combinations (e.g. `oneshot` + `tmux`) aren't rejected at config load
but won't do anything sensible.

### Template variables

The `prompt_template` is a Go `text/template`. Available fields:

| Variable        | Source                                                           |
|-----------------|------------------------------------------------------------------|
| `{{.Repo}}`     | `owner/name`                                                     |
| `{{.Number}}`   | PR number                                                        |
| `{{.Title}}`    | PR title                                                         |
| `{{.Author}}`   | PR author's GitHub login                                         |
| `{{.Body}}`     | PR description (markdown, as-is from GitHub)                     |
| `{{.HeadSHA}}`  | Head commit                                                      |
| `{{.BaseSHA}}`  | Base commit                                                      |
| `{{.Worktree}}` | Absolute worktree path — empty string if `worktree: false`       |
| `{{.FileList}}` | One line per changed file: `path  +A -D`                         |
| `{{.Patches}}`  | Concatenated unified diffs, one file after another, with headers |

Unknown fields fail template rendering loudly — we don't silently swallow
typos (`{{.Titel}}` would hide the real message and make you debug a
malformed prompt in your agent's logs).

## Worked example 1 — swap Claude for opencode

```json
{
  "repos": ["cli/cli"],
  "agents": [
    { "name": "opencode", "command": "opencode", "oneshot_args": ["run"] }
  ],
  "default_agent": "opencode"
}
```

No changes to `actions` needed — the built-ins will run through opencode
now. `t` still opens a chat; `f` still runs a first-pass review.

## Worked example 2 — custom security-review action

```json
{
  "repos": ["cli/cli"],
  "agents": [
    { "name": "claude", "command": "claude", "oneshot_args": ["-p"] }
  ],
  "actions": [
    {
      "key": "t",
      "name": "chat",
      "mode": "interactive",
      "worktree": true,
      "context": "paths",
      "delivery": "tmux",
      "prompt_template": "Review PR {{.Repo}}#{{.Number}}: {{.Title}}\nWorktree: {{.Worktree}}\n\n{{.Body}}"
    },
    {
      "key": "S",
      "name": "security-review",
      "mode": "oneshot",
      "worktree": false,
      "context": "patches",
      "delivery": "inline-notes",
      "prompt_template": "You are a security reviewer. Inspect this PR for security issues only — auth bypass, injection, secret leaks, missing input validation, unsafe deserialization, race conditions with security impact.\n\nPR {{.Repo}}#{{.Number}}: {{.Title}}\n\nDiff:\n{{.Patches}}\n\nReturn ONLY a JSON array of review notes, each {\"path\":\"...\",\"line\":N,\"body\":\"...\"}. Empty array [] if nothing stands out."
    }
  ]
}
```

Now `S` in the TUI runs a focused security pass and any findings land in
the brain as agent notes, same as `first-pass`.

## Tips

- Use `mode: oneshot` when you want results to survive into the brain —
  interactive chats don't auto-persist anything.
- If the agent's oneshot output isn't valid JSON, rhodium stashes stdout +
  stderr under `~/.cache/rhodium/last-<action>-<owner>-<repo>-<N>.*`. Read
  those when a first-pass action "succeeds silently" but no notes appear.
- The `key` field is a single key; combinations (`ctrl+x`) aren't supported
  for actions today. Pick letters that don't collide with built-in view
  bindings — uppercase letters are usually safe.
- Agents run with the TUI's environment. If your agent needs API keys,
  export them in your shell before launching `rhodium`.

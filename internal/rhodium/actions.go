package rhodium

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"rhodium/internal/brain"
	"rhodium/internal/gh"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// runAction dispatches a configured action. Resolves the worktree if the
// action asks for one, builds the prompt context, renders the template, and
// hands off to the right delivery backend.
//
// Returns a tea.Cmd (async) and a sync error. Sync errors are for things the
// user should see immediately ("no PR selected"); async failures surface via
// actionDoneMsg.
func runAction(a *app, action Action) (tea.Cmd, error) {
	if a.selectedPR == nil {
		return nil, fmt.Errorf("no PR selected")
	}
	pr := *a.selectedPR
	files := a.cache.prFiles[brain.PRKey(pr.Repo, pr.Number)]

	agent := a.cfg.DefaultAgentResolved()
	if agent.Command == "" {
		return nil, fmt.Errorf("no agent configured")
	}

	var worktree string
	if action.Worktree {
		w, err := resolveWorktree(a.cfg, pr.Repo, pr.Number)
		if err != nil {
			return nil, err
		}
		worktree = w
	}

	ctx := buildPromptCtx(pr, files, worktree)
	prompt, err := renderPrompt(action, ctx)
	if err != nil {
		return nil, err
	}

	switch action.Delivery {
	case "tmux":
		return runInteractiveAction(a, action, agent, worktree, pr, prompt)
	case "inline-notes":
		return runInlineNotesAction(a, action, agent, pr, prompt), nil
	default:
		return nil, fmt.Errorf("action %q: unknown delivery %q", action.Name, action.Delivery)
	}
}

// runInteractiveAction spawns a tmux pane in the worktree and starts the
// agent with the rendered prompt. The prompt is written to a file under
// $TMPDIR so it survives the tmux handoff and remains inspectable if the
// session is resumed. We invoke the agent as
// `<cmd> <interactive_args...> "$(cat <file>)"` so the prompt arrives as
// one argv slot — bare for claude, behind `--prompt` for opencode.
func runInteractiveAction(a *app, action Action, agent Agent, worktree string, pr gh.PR, prompt string) (tea.Cmd, error) {
	promptPath, err := writePromptFile(pr, action.Name, prompt)
	if err != nil {
		return nil, err
	}

	// Agents that won't accept the prompt as a bare positional arg (opencode
	// uses --prompt, for example) supply interactive_args; those slot in
	// between the command and the $(cat …) positional.
	head := shellQuote(agent.Command)
	if len(agent.InteractiveArgs) > 0 {
		head += " " + shellJoin(agent.InteractiveArgs)
	}
	cmdline := fmt.Sprintf("%s \"$(cat %s)\"", head, shellQuote(promptPath))

	if os.Getenv("TMUX") == "" || a.cfg.TmuxMode() == "off" {
		cmd := exec.Command("sh", "-c", cmdline)
		if worktree != "" {
			cmd.Dir = worktree
		}
		a.statusMsg = fmt.Sprintf("launching %s (%s)…", agent.Name, action.Name)
		return tea.ExecProcess(cmd, func(err error) tea.Msg {
			return actionDoneMsg{action: action.Name, err: err}
		}), nil
	}

	label := fmt.Sprintf("%s: %s", action.Name, brain.PRKey(pr.Repo, pr.Number))
	a.statusMsg = fmt.Sprintf("launching %s (%s) in tmux pane", agent.Name, action.Name)
	return func() tea.Msg {
		paneID, err := spawnTmuxPane(a.cfg.TmuxMode(), worktree, label)
		if err != nil {
			return actionDoneMsg{action: action.Name, err: err}
		}
		if err := tmuxSendKeys(paneID, cmdline); err != nil {
			return actionDoneMsg{action: action.Name, err: err}
		}
		return actionDoneMsg{action: action.Name}
	}, nil
}

// runInlineNotesAction runs the agent non-interactively with the prompt on
// stdin, parses its stdout as a JSON array of review notes, and writes each
// to the brain under source="agent". Stays inside the TUI — no tmux, no
// worktree required.
func runInlineNotesAction(a *app, action Action, agent Agent, pr gh.PR, prompt string) tea.Cmd {
	a.statusMsg = fmt.Sprintf("running %s (%s)…", agent.Name, action.Name)
	return func() tea.Msg {
		cmd := exec.Command(agent.Command, agent.OneshotArgs...)
		cmd.Stdin = strings.NewReader(prompt)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			// Stash stdout for debugging — agents sometimes emit partial output
			// even on non-zero exit.
			stashAgentOutput(pr, action.Name, stdout.Bytes(), stderr.Bytes())
			return actionDoneMsg{
				action: action.Name,
				err:    fmt.Errorf("%s %v: %w (stderr: %s)", agent.Command, agent.OneshotArgs, err, strings.TrimSpace(stderr.String())),
			}
		}

		notes, err := parseAgentNotes(stdout.Bytes())
		if err != nil {
			stashAgentOutput(pr, action.Name, stdout.Bytes(), stderr.Bytes())
			return actionDoneMsg{action: action.Name, err: fmt.Errorf("parse agent output: %w", err)}
		}
		return inlineNotesReadyMsg{action: action.Name, pr: pr, notes: notes}
	}
}

// writePromptFile persists the rendered prompt to $TMPDIR so the tmux child
// shell can read it via `$(cat …)`. Path is deterministic per (PR, action)
// so repeated presses overwrite instead of piling up.
func writePromptFile(pr gh.PR, actionName, prompt string) (string, error) {
	safeKey := strings.ReplaceAll(brain.PRKey(pr.Repo, pr.Number), "/", "-")
	name := fmt.Sprintf("rhodium-%s-%s.md", safeKey, actionName)
	path := filepath.Join(os.TempDir(), name)
	if err := os.WriteFile(path, []byte(prompt), 0o600); err != nil {
		return "", fmt.Errorf("write prompt file: %w", err)
	}
	return path, nil
}

// stashAgentOutput writes raw agent output to ~/.cache/rhodium for
// post-mortem when the oneshot path fails to parse. Best-effort — ignores
// errors since this is itself an error path.
func stashAgentOutput(pr gh.PR, actionName string, stdout, stderr []byte) {
	dir := cacheDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	safeKey := strings.ReplaceAll(brain.PRKey(pr.Repo, pr.Number), "/", "-")
	base := filepath.Join(dir, fmt.Sprintf("last-%s-%s", actionName, safeKey))
	os.WriteFile(base+".stdout", stdout, 0o600)
	os.WriteFile(base+".stderr", stderr, 0o600)
}

func cacheDir() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "rhodium")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "rhodium")
}

// agentNote is the on-wire shape we expect from an inline-notes action.
// Line is in the new-file (post-change) numbering — same as human notes.
type agentNote struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
}

// parseAgentNotes accepts either a bare JSON array or a JSON array wrapped
// in markdown fences (```json … ```), since agents frequently ignore "no
// code fences" instructions. Everything outside the first [ and last ] is
// treated as noise and stripped.
func parseAgentNotes(raw []byte) ([]agentNote, error) {
	s := strings.TrimSpace(string(raw))
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start < 0 || end < 0 || end < start {
		return nil, fmt.Errorf("no JSON array found in output (%d bytes)", len(raw))
	}
	var notes []agentNote
	if err := json.Unmarshal([]byte(s[start:end+1]), &notes); err != nil {
		return nil, err
	}
	return notes, nil
}

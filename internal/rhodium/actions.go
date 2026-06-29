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
	if a.session.selectedPR == nil {
		return nil, fmt.Errorf("no PR selected")
	}
	pr := *a.session.selectedPR
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
		// Check staleness and refresh if needed — agents must see current code.
		stale, behind := isStale(a.cfg, pr.Repo, pr.Number)
		if stale {
			a.status.msg = fmt.Sprintf("worktree %d commits behind — refreshing…", behind)
			if err := RefreshWorktree(a.cfg, pr.Repo, pr.Number); err != nil {
				return nil, fmt.Errorf("stale worktree refresh failed: %w", err)
			}
			a.status.msg = fmt.Sprintf("worktree refreshed (%d commits) — launching %s…", behind, agent.Name)
		}
		worktree = w
	}

	ctx := BuildPromptCtx(pr, files, worktree, a.cfg.BaseBranch(pr.Repo))
	prompt, err := RenderPrompt(action, ctx)
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
		a.status.msg = fmt.Sprintf("launching %s (%s)…", agent.Name, action.Name)
		return tea.ExecProcess(cmd, func(err error) tea.Msg {
			// The agent has exited — the prompt file is no longer needed.
			os.Remove(promptPath)
			return actionDoneMsg{action: action.Name, err: err}
		}), nil
	}

	// For the tmux path we can't know when the agent exits, so append a
	// shell `rm` after the command substitution: the shell evaluates
	// `$(cat …)` before running the agent, so the file can be removed
	// once the agent itself terminates inside the pane.
	cmdline = fmt.Sprintf("%s; rm -f %s", cmdline, shellQuote(promptPath))

	label := fmt.Sprintf("%s: %s", action.Name, brain.PRKey(pr.Repo, pr.Number))
	a.status.msg = fmt.Sprintf("launching %s (%s) in tmux pane", agent.Name, action.Name)
	return func() tea.Msg {
		paneID, err := spawnTmuxPane(a.cfg.TmuxMode(), worktree, label)
		if err != nil {
			os.Remove(promptPath)
			return actionDoneMsg{action: action.Name, err: err}
		}
		if err := tmuxSendKeys(paneID, cmdline); err != nil {
			os.Remove(promptPath)
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
	a.status.msg = fmt.Sprintf("running %s (%s)…", agent.Name, action.Name)
	return func() tea.Msg {
		cmd := exec.Command(agent.Command, agent.OneshotArgs...)
		cmd.Stdin = strings.NewReader(prompt)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			// Stash stdout for debugging — agents sometimes emit partial output
			// even on non-zero exit.
			StashAgentOutput(pr, action.Name, stdout.Bytes(), stderr.Bytes())
			return actionDoneMsg{
				action: action.Name,
				err:    fmt.Errorf("%s %v: %w (stderr: %s)", agent.Command, agent.OneshotArgs, err, strings.TrimSpace(stderr.String())),
			}
		}

		notes, err := ParseAgentNotes(stdout.Bytes())
		if err != nil {
			StashAgentOutput(pr, action.Name, stdout.Bytes(), stderr.Bytes())
			return actionDoneMsg{action: action.Name, err: fmt.Errorf("parse agent output: %w", err)}
		}
		return inlineNotesReadyMsg{action: action.Name, pr: pr, notes: notes}
	}
}

// writePromptFile persists the rendered prompt to $TMPDIR so the tmux child
// shell can read it via `$(cat …)`. Uses os.CreateTemp so the name is
// unpredictable and the file is created with O_CREATE|O_EXCL — defeating
// a co-located attacker who pre-creates the path as a symlink to a victim
// file (the old deterministic path was symlink-followable). Repeat presses
// no longer overwrite; the caller is responsible for cleaning up via
// os.Remove once the tmux/sh consumer is done.
func writePromptFile(pr gh.PR, actionName, prompt string) (string, error) {
	// pr/actionName are no longer baked into the filename now that
	// O_CREATE|O_EXCL is doing the security work. They're kept in the
	// signature for caller stability and potential future use (e.g.
	// embedding a sanitized label as a temp-file prefix).
	_ = pr
	_ = actionName
	// "rhodium-*.md" keeps the *.md suffix some editors / agents key on.
	f, err := os.CreateTemp("", "rhodium-*.md")
	if err != nil {
		return "", fmt.Errorf("create prompt file: %w", err)
	}
	if _, err := f.WriteString(prompt); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("write prompt file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("close prompt file: %w", err)
	}
	return f.Name(), nil
}

// StashAgentOutput writes raw agent output to ~/.cache/rhodium for
// post-mortem when the oneshot path fails to parse. Best-effort — ignores
// errors since this is itself an error path.
func StashAgentOutput(pr gh.PR, actionName string, stdout, stderr []byte) {
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

// AgentNote is the on-wire shape we expect from an inline-notes action.
// Line is in the new-file (post-change) numbering — same as human notes.
type AgentNote struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
}

// ParseAgentNotes accepts either a bare JSON array or a JSON array wrapped
// in markdown fences (```json … ```), since agents frequently ignore "no
// code fences" instructions. Everything outside the first [ and last ] is
// treated as noise and stripped.
func ParseAgentNotes(raw []byte) ([]AgentNote, error) {
	s := strings.TrimSpace(string(raw))
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start < 0 || end < 0 || end < start {
		return nil, fmt.Errorf("no JSON array found in output (%d bytes)", len(raw))
	}
	var notes []AgentNote
	if err := json.Unmarshal([]byte(s[start:end+1]), &notes); err != nil {
		return nil, err
	}
	return notes, nil
}

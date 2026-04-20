package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// launchEditor opens `file` at `line` inside `worktree`, running `rhodium`'s
// configured editor. Routes through tmux when we're inside a session (unless
// tmux.mode is "off"); otherwise suspends the TUI via tea.ExecProcess.
//
// prKey is threaded through as `g:rhodium_pr` so the nvim plugin can wire up
// overlays on startup.
func launchEditor(cfg *Config, worktree, file, prKey string, line int) tea.Cmd {
	editor := cfg.EditorCommand()
	if line < 1 {
		line = 1
	}
	nvimArgs := []string{
		"--cmd", fmt.Sprintf("let g:rhodium_pr=%q", prKey),
	}
	if plug := nvimPluginPath(); plug != "" {
		nvimArgs = append(nvimArgs, "-c", fmt.Sprintf("luafile %s", plug))
	}
	nvimArgs = append(nvimArgs, fmt.Sprintf("+%d", line), file)

	if os.Getenv("TMUX") != "" && cfg.TmuxMode() != "off" {
		// Spawn the pane as the user's default interactive shell (tmux's
		// default), capture its pane id, then `send-keys` the nvim command.
		// This keeps nvim as a foreground job of a real interactive shell so
		// ctrl-z / fg / job control all work as expected — unlike wrapping
		// in `$SHELL -c`, which is non-interactive and has no job control.
		cmdline := shellJoin(append([]string{editor}, nvimArgs...))
		return func() tea.Msg {
			paneID, err := spawnTmuxPane(cfg.TmuxMode(), worktree, prKey)
			if err != nil {
				return editorDoneMsg{err: err}
			}
			if err := tmuxSendKeys(paneID, cmdline); err != nil {
				return editorDoneMsg{err: err}
			}
			return editorDoneMsg{}
		}
	}

	// Fallback: suspend TUI, exec inline, resume on exit.
	cmd := exec.Command(editor, nvimArgs...)
	cmd.Dir = worktree
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorDoneMsg{err: err}
	})
}

// spawnTmuxPane creates a new tmux window/split per mode with cwd set to the
// worktree, and returns the pane id so callers can follow up with send-keys.
// Label is used for the window name when mode is "window".
func spawnTmuxPane(mode, cwd, label string) (string, error) {
	spawnArgs := tmuxArgs(mode, cwd, label)
	spawnArgs = append(spawnArgs, "-P", "-F", "#{pane_id}")
	out, err := exec.Command("tmux", spawnArgs...).Output()
	if err != nil {
		return "", fmt.Errorf("tmux spawn: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// tmuxSendKeys sends `cmdline` + Enter to the given pane. cmdline should
// already be shell-quoted (see shellJoin).
func tmuxSendKeys(paneID, cmdline string) error {
	if err := exec.Command("tmux", "send-keys", "-t", paneID, cmdline, "Enter").Run(); err != nil {
		return fmt.Errorf("tmux send-keys: %w", err)
	}
	return nil
}

func tmuxArgs(mode, cwd, prKey string) []string {
	switch mode {
	case "split-h":
		return []string{"split-window", "-h", "-c", cwd}
	case "split-v":
		return []string{"split-window", "-v", "-c", cwd}
	default: // "window"
		return []string{"new-window", "-c", cwd, "-n", fmt.Sprintf("rhodium: %s", prKey)}
	}
}

// shellQuote wraps s in POSIX single quotes, escaping any embedded single
// quotes. Safe for composing into a command line sent via `tmux send-keys`.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// nvimPluginPath returns the absolute path to rhodium.lua if we can find one.
// Precedence:
//   1. $RHODIUM_NVIM_PLUGIN
//   2. editor/nvim/rhodium.lua beside the running binary
//   3. editor/nvim/rhodium.lua one level up (for `bin/rhodium` layouts)
//
// Empty string means "rely on user's runtimepath."
func nvimPluginPath() string {
	if p := os.Getenv("RHODIUM_NVIM_PLUGIN"); p != "" {
		return p
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	for _, candidate := range []string{
		filepath.Join(dir, "editor", "nvim", "rhodium.lua"),
		filepath.Join(dir, "..", "editor", "nvim", "rhodium.lua"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

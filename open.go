package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

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
		args := tmuxArgs(cfg.TmuxMode(), worktree, prKey)
		args = append(args, editor)
		args = append(args, nvimArgs...)
		return func() tea.Msg {
			if err := exec.Command("tmux", args...).Run(); err != nil {
				return editorDoneMsg{err: fmt.Errorf("tmux: %w", err)}
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

// editorDoneMsg fires when the editor process exits (ExecProcess path) or
// the tmux command returns (tmux path — typically immediately after spawning
// the pane/window).
type editorDoneMsg struct {
	err error
}

// nvimPluginPath returns the absolute path to rhodium.lua if we can find one.
// Precedence: $RHODIUM_NVIM_PLUGIN, then editor/nvim/rhodium.lua beside the
// running binary. Empty string means "rely on user's runtimepath."
func nvimPluginPath() string {
	if p := os.Getenv("RHODIUM_NVIM_PLUGIN"); p != "" {
		return p
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	candidate := filepath.Join(filepath.Dir(exe), "editor", "nvim", "rhodium.lua")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

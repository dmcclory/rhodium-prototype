package rhodium

import (
	"fmt"
	"os"
	"rhodium/internal/brain"
	"rhodium/internal/gh"

	tea "github.com/charmbracelet/bubbletea"
)

// Run launches the TUI. Args dispatch (CLI vs TUI) lives in main; by the
// time we get here the caller has already decided.
func Run() error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	if cfg.GitHubUser == "" {
		if login, err := gh.FetchUser(); err == nil {
			cfg.GitHubUser = login
		} else {
			fmt.Fprintln(os.Stderr, "warning: could not detect GitHub user — set `github_user` in config to split your PRs:", err)
		}
	}
	b, err := brain.LoadBrain()
	if err != nil {
		return err
	}
	p := tea.NewProgram(newApp(cfg, b), tea.WithAltScreen())
	program = p
	if _, err := p.Run(); err != nil {
		return err
	}
	return nil
}

package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	if len(os.Args) > 1 {
		if err := runCLI(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if cfg.GitHubUser == "" {
		if login, err := fetchGitHubUser(); err == nil {
			cfg.GitHubUser = login
		} else {
			fmt.Fprintln(os.Stderr, "warning: could not detect GitHub user — set `github_user` in config to split your PRs:", err)
		}
	}
	brain, err := LoadBrain()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	p := tea.NewProgram(newApp(cfg, brain), tea.WithAltScreen())
	program = p
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

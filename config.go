package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Repos     []string          `json:"repos"`
	RepoPaths map[string]string `json:"repo_paths,omitempty"` // "owner/repo" → local clone path
	Worktree  WorktreeConfig    `json:"worktree,omitempty"`
	Tmux      TmuxConfig        `json:"tmux,omitempty"`
	Editor    EditorConfig      `json:"editor,omitempty"`
}

type WorktreeConfig struct {
	Root string `json:"root,omitempty"` // default: ~/rhodium/worktrees
}

type TmuxConfig struct {
	// Mode: "window" (new-window), "split-h", "split-v", or "off".
	// Empty defaults to "window" when $TMUX is set.
	Mode string `json:"mode,omitempty"`
}

type EditorConfig struct {
	Command string `json:"command,omitempty"` // default: nvim
}

func (c *Config) WorktreeRoot() string {
	if c.Worktree.Root != "" {
		return expandHome(c.Worktree.Root)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "rhodium", "worktrees")
}

func (c *Config) EditorCommand() string {
	if c.Editor.Command != "" {
		return c.Editor.Command
	}
	return "nvim"
}

func (c *Config) TmuxMode() string {
	if c.Tmux.Mode != "" {
		return c.Tmux.Mode
	}
	return "window"
}

// RepoPath returns the local clone path for a repo. Looks up config first;
// falls back to $RHODIUM_REPOS_ROOT/<repo-name> or ~/code/<repo-name>.
func (c *Config) RepoPath(repo string) string {
	if p, ok := c.RepoPaths[repo]; ok {
		return expandHome(p)
	}
	root := os.Getenv("RHODIUM_REPOS_ROOT")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, "code")
	}
	return filepath.Join(root, repo)
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

func loadConfig() (*Config, error) {
	path := os.Getenv("RHODIUM_CONFIG")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, ".config", "rhodium", "config.json")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no config at %s — create one with {\"repos\": [\"owner/name\"]}", path)
		}
		return nil, err
	}

	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(c.Repos) == 0 {
		return nil, fmt.Errorf("config at %s has no repos", path)
	}
	return &c, nil
}

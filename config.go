package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Repos        []string          `json:"repos"`
	RepoPaths    map[string]string `json:"repo_paths,omitempty"` // "owner/repo" → local clone path
	Worktree     WorktreeConfig    `json:"worktree,omitempty"`
	Tmux         TmuxConfig        `json:"tmux,omitempty"`
	Editor       EditorConfig      `json:"editor,omitempty"`
	Agents       []Agent           `json:"agents,omitempty"`
	DefaultAgent string            `json:"default_agent,omitempty"`
	Actions      []Action          `json:"actions,omitempty"`
}

// Agent is a coding-assistant binary (claude, opencode, etc). Actions pick
// one by name; `default_agent` picks which is used when multiple are defined.
type Agent struct {
	Name        string   `json:"name"`
	Command     string   `json:"command"`
	OneshotArgs []string `json:"oneshot_args,omitempty"` // flags for non-interactive mode (e.g. claude's -p)
}

// Action binds a keypress to an agent invocation shape. The action describes
// *what kind of conversation*; the agent knows *how to invoke itself* for
// that kind. Swapping default_agent just works without editing actions.
type Action struct {
	Key            string `json:"key"`
	Name           string `json:"name"`
	Mode           string `json:"mode"`     // "interactive" | "oneshot"
	Worktree       bool   `json:"worktree"` // true → resolve/create PR worktree before invoking
	Context        string `json:"context"`  // "paths" | "patches"
	Delivery       string `json:"delivery"` // "tmux" | "inline-notes"
	PromptTemplate string `json:"prompt_template"`
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

// defaultAgents / defaultActions ship built-in so the `t` chat and `f` first-pass
// keys work without any user config. If the user defines agents/actions at all,
// their list fully replaces ours — we don't deep-merge.
func defaultAgents() []Agent {
	return []Agent{
		{Name: "claude", Command: "claude", OneshotArgs: []string{"-p"}},
	}
}

func defaultActions() []Action {
	return []Action{
		{
			Key:      "t",
			Name:     "chat",
			Mode:     "interactive",
			Worktree: true,
			Context:  "paths",
			Delivery: "tmux",
			PromptTemplate: `You're helping review PR {{.Repo}}#{{.Number}}: {{.Title}}
Author: {{.Author}}
Worktree (cwd): {{.Worktree}}

Changed files:
{{.FileList}}

PR description:
{{.Body}}

Read whichever files seem relevant and discuss the change with me.`,
		},
		{
			Key:      "f",
			Name:     "first-pass",
			Mode:     "oneshot",
			Worktree: false,
			Context:  "patches",
			Delivery: "inline-notes",
			PromptTemplate: `Do a first-pass review of PR {{.Repo}}#{{.Number}}: {{.Title}}
Author: {{.Author}}

PR description:
{{.Body}}

Unified diff of all changed files:
{{.Patches}}

Return ONLY a JSON array (no prose, no code fence) of review notes. Each entry:
  {"path": "<file path>", "line": <new-file line number>, "body": "<your comment>"}
Focus on real issues: bugs, unclear logic, missing edge cases, inconsistencies.
Empty array [] is fine if nothing stands out.`,
		},
	}
}

// AgentsResolved returns the effective agent list (user config or defaults).
func (c *Config) AgentsResolved() []Agent {
	if len(c.Agents) > 0 {
		return c.Agents
	}
	return defaultAgents()
}

// ActionsResolved returns the effective action list (user config or defaults).
func (c *Config) ActionsResolved() []Action {
	if len(c.Actions) > 0 {
		return c.Actions
	}
	return defaultActions()
}

// DefaultAgentResolved picks the configured default, else the first agent,
// else zero value.
func (c *Config) DefaultAgentResolved() Agent {
	agents := c.AgentsResolved()
	if c.DefaultAgent != "" {
		for _, a := range agents {
			if a.Name == c.DefaultAgent {
				return a
			}
		}
	}
	if len(agents) > 0 {
		return agents[0]
	}
	return Agent{}
}

// ActionByKey finds an action by its configured keypress.
func (c *Config) ActionByKey(key string) (Action, bool) {
	for _, a := range c.ActionsResolved() {
		if a.Key == key {
			return a, true
		}
	}
	return Action{}, false
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

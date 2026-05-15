package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"rhodium/internal/brain"
	"rhodium/internal/rhodium"
)

func cmdWorktree(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: rhodium worktree <status|refresh|remove> [flags]")
	}
	switch args[0] {
	case "status":
		return cmdWorktreeStatus(args[1:])
	case "refresh":
		return cmdWorktreeRefresh(args[1:])
	case "remove":
		return cmdWorktreeRemove(args[1:])
	default:
		return fmt.Errorf("unknown worktree subcommand: %s (try status, refresh, remove)", args[0])
	}
}

// cmdWorktreeStatus prints the state of all worktrees across cached PRs.
// Usage: rhodium worktree status [--json]
func cmdWorktreeStatus(args []string) error {
	flags, positional := splitFlags(args)
	if len(positional) > 0 {
		return fmt.Errorf("usage: rhodium worktree status [--json]")
	}
	jsonOut := hasFlag(flags, "json")

	cfg, err := rhodium.LoadConfig()
	if err != nil {
		return err
	}

	b, err := brain.OpenForCLI()
	if err != nil {
		return err
	}
	defer b.Close()

	cachedPRs := b.CachedPRs()

	type row struct {
		Repo        string `json:"repo"`
		Number      int    `json:"number"`
		Title       string `json:"title"`
		Path        string `json:"path"`
		State       string `json:"state"`
		BehindCount int    `json:"behind_count,omitempty"`
		PRState     string `json:"pr_state,omitempty"`
	}

	var results []row
	for _, pr := range cachedPRs {
		status := rhodium.InspectWorktree(cfg, pr.Repo, pr.Number)
		results = append(results, row{
			Repo:        pr.Repo,
			Number:      pr.Number,
			Title:       pr.Title,
			Path:        status.Path,
			State:       status.State,
			BehindCount: status.BehindCount,
			PRState:     status.PRState,
		})
	}

	// Sort: stale first (most behind), then missing, then current
	sort.Slice(results, func(i, j int) bool {
		return worktreeSortRank(results[i].State, results[i].BehindCount) >
			worktreeSortRank(results[j].State, results[j].BehindCount)
	})

	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(results)
	}

	if len(results) == 0 {
		fmt.Println("no cached PRs — run the TUI or `rhodium todo --sync` first")
		return nil
	}

	for _, r := range results {
		icon := worktreeIcon(r.State)
		suffix := ""
		if r.State == "stale" {
			suffix = fmt.Sprintf(" (%d behind)", r.BehindCount)
		}
		if r.PRState != "" && r.PRState != "open" && r.PRState != "unknown" {
			suffix += fmt.Sprintf(" [%s]", r.PRState)
		}
		prKey := fmt.Sprintf("%s#%d", r.Repo, r.Number)
		fmt.Printf("  %s %-28s %s%s\n", icon, prKey, truncate(r.Title, 50), suffix)
	}

	// Summary
	stale, missing, current := 0, 0, 0
	for _, r := range results {
		switch r.State {
		case "stale":
			stale++
		case "missing":
			missing++
		case "current":
			current++
		}
	}
	fmt.Printf("\n  %d current, %d stale, %d missing\n", current, stale, missing)
	return nil
}

// worktreeSortRank gives sort priority: stale (most behind) first, then
// missing, then current, then error.
func worktreeSortRank(state string, behind int) int {
	switch state {
	case "stale":
		return 1000 + behind
	case "missing":
		return 500
	case "current":
		return 0
	default:
		return 1
	}
}

func worktreeIcon(state string) string {
	switch state {
	case "current":
		return "✓"
	case "stale":
		return "⟳"
	case "missing":
		return "·"
	case "error":
		return "!"
	default:
		return "?"
	}
}

// cmdWorktreeRefresh updates one or all worktrees to their PR's current HEAD.
// Usage: rhodium worktree refresh <owner/repo#N> [--all]
func cmdWorktreeRefresh(args []string) error {
	flags, positional := splitFlags(args)
	allFlag := hasFlag(flags, "all") || hasFlag(flags, "a")

	cfg, err := rhodium.LoadConfig()
	if err != nil {
		return err
	}

	type target struct {
		repo   string
		number int
	}

	var toRefresh []target

	if allFlag {
		// Refresh all stale worktrees across cached PRs
		b, err := brain.OpenForCLI()
		if err != nil {
			return err
		}
		defer b.Close()

		for _, pr := range b.CachedPRs() {
			status := rhodium.InspectWorktree(cfg, pr.Repo, pr.Number)
			if status.State == "stale" {
				toRefresh = append(toRefresh, target{repo: pr.Repo, number: pr.Number})
			}
		}
	} else {
		if len(positional) == 0 {
			return fmt.Errorf("usage: rhodium worktree refresh <owner/repo#N> [--all]")
		}
		for _, ref := range positional {
			repo, number, err := parsePRRef(ref)
			if err != nil {
				return err
			}
			toRefresh = append(toRefresh, target{repo: repo, number: number})
		}
	}

	if len(toRefresh) == 0 {
		fmt.Println("no worktrees need refreshing")
		return nil
	}

	success, failed := 0, 0
	for _, t := range toRefresh {
		prKey := fmt.Sprintf("%s#%d", t.repo, t.number)
		fmt.Printf("refreshing %s...", prKey)
		if err := rhodium.RefreshWorktree(cfg, t.repo, t.number); err != nil {
			fmt.Printf(" FAILED: %s\n", err)
			failed++
		} else {
			fmt.Println(" ok")
			success++
		}
	}

	fmt.Printf("\n%d refreshed, %d failed\n", success, failed)
	if failed > 0 {
		return fmt.Errorf("%d worktree(s) could not be refreshed", failed)
	}
	return nil
}

// cmdWorktreeRemove removes one or more worktrees from disk and git.
// Usage: rhodium worktree remove <owner/repo#N>...
func cmdWorktreeRemove(args []string) error {
	_, positional := splitFlags(args)

	if len(positional) == 0 {
		return fmt.Errorf("usage: rhodium worktree remove <owner/repo#N>...")
	}

	cfg, err := rhodium.LoadConfig()
	if err != nil {
		return err
	}

	for _, ref := range positional {
		repo, number, err := parsePRRef(ref)
		if err != nil {
			return err
		}
		safeRepo := strings.ReplaceAll(repo, "/", "-")
		target := fmt.Sprintf("%s/%s/pr-%d", cfg.WorktreeRoot(), safeRepo, number)
		prKey := fmt.Sprintf("%s#%d", repo, number)

		fmt.Printf("removing %s worktree at %s...\n", prKey, target)

		if _, err := os.Stat(target); os.IsNotExist(err) {
			fmt.Printf("  %s already gone\n", prKey)
			continue
		}

		// Try git worktree remove first
		sourcePath := cfg.RepoPath(repo)
		if _, statErr := os.Stat(sourcePath); statErr == nil {
			if gitErr := runCombined("git", "-C", sourcePath, "worktree", "remove", target); gitErr != nil {
				fmt.Printf("  git remove failed (%s), removing directory\n", gitErr)
			}
		}

		if err := os.RemoveAll(target); err != nil {
			fmt.Printf("  FAILED to remove directory: %s\n", err)
		} else {
			fmt.Printf("  removed %s\n", prKey)
		}
	}
	return nil
}

func hasFlag(flags []string, name string) bool {
	for _, f := range flags {
		trimmed := strings.TrimLeft(f, "-")
		if trimmed == name {
			return true
		}
	}
	return false
}

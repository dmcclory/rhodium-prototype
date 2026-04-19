package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// resolveWorktree returns the local path for a per-PR worktree, creating it
// if it doesn't exist. Convention: <worktree_root>/<owner>-<repo>/pr-<N>.
//
// On first use for a PR, we:
//   1. Ensure the source repo exists at the configured local path.
//   2. `git worktree add` with a placeholder branch in the source repo.
//   3. Inside the new worktree, `gh pr checkout <N>` to land the PR branch
//      (handles fork PRs cleanly).
//
// If the target path already exists and is a registered worktree, it's
// returned unchanged. If the path exists but is NOT a worktree, we refuse
// to clobber it.
func resolveWorktree(cfg *Config, repo string, number int) (string, error) {
	sourcePath := cfg.RepoPath(repo)
	if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
		return "", fmt.Errorf("source repo not found at %s — set config.repo_paths[%q] or $RHODIUM_REPOS_ROOT", sourcePath, repo)
	} else if err != nil {
		return "", err
	}

	root := cfg.WorktreeRoot()
	safeRepo := strings.ReplaceAll(repo, "/", "-")
	target := filepath.Join(root, safeRepo, fmt.Sprintf("pr-%d", number))

	existing, err := listWorktrees(sourcePath)
	if err != nil {
		return "", fmt.Errorf("list worktrees: %w", err)
	}
	for _, w := range existing {
		if w == target {
			return target, nil
		}
	}

	if _, err := os.Stat(target); err == nil {
		return "", fmt.Errorf("%s already exists and is not a worktree of %s", target, sourcePath)
	} else if !os.IsNotExist(err) {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}

	// Create a detached worktree first; gh pr checkout will set the branch.
	// Using a detached HEAD avoids branch-name collisions and fork-PR edge
	// cases that plain `git worktree add <branch>` can't handle.
	addCmd := exec.Command("git", "-C", sourcePath, "worktree", "add", "--detach", target)
	if out, err := addCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add: %w — %s", err, strings.TrimSpace(string(out)))
	}

	checkoutCmd := exec.Command("gh", "pr", "checkout", fmt.Sprintf("%d", number), "--repo", repo)
	checkoutCmd.Dir = target
	if out, err := checkoutCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("gh pr checkout %d: %w — %s", number, err, strings.TrimSpace(string(out)))
	}

	return target, nil
}

// listWorktrees returns the absolute paths of all registered worktrees for
// the repo at sourcePath.
func listWorktrees(sourcePath string) ([]string, error) {
	out, err := exec.Command("git", "-C", sourcePath, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

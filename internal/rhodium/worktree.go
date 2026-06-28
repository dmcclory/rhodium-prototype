package rhodium

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"rhodium/internal/shellout"
)

// resolveWorktree returns the local path for a per-PR worktree, creating it
// if it doesn't exist. Convention: <worktree_root>/<owner>-<repo>/pr-<N>.
//
// On first use for a PR, we:
//  1. Ensure the source repo exists at the configured local path.
//  2. `git worktree add` with a placeholder branch in the source repo.
//  3. Inside the new worktree, `gh pr checkout <N>` to land the PR branch
//     (handles fork PRs cleanly).
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
	if _, err := shellout.Combined("git", "-C", sourcePath, "worktree", "add", "--detach", target); err != nil {
		return "", fmt.Errorf("git worktree add: %w", err)
	}

	if _, err := (shellout.Spec{Dir: target}).Combined("gh", "pr", "checkout", fmt.Sprintf("%d", number), "--repo", repo); err != nil {
		return "", fmt.Errorf("gh pr checkout %d: %w", number, err)
	}

	return target, nil
}

// worktreeHEAD returns the current HEAD SHA of an existing worktree.
func worktreeHEAD(path string) (string, error) {
	out, err := shellout.Combined("git", "-C", path, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD in %s: %w", path, err)
	}
	return strings.TrimSpace(out), nil
}

// currentPRHEAD asks GitHub for the PR's current head SHA via `gh pr view`.
func currentPRHEAD(repo string, number int) (string, error) {
	out, err := shellout.Combined("gh", "pr", "view", fmt.Sprintf("%d", number),
		"--repo", repo,
		"--json", "headRefOid",
		"--jq", ".headRefOid",
	)
	if err != nil {
		return "", fmt.Errorf("gh pr view %s#%d: %w", repo, number, err)
	}
	return strings.TrimSpace(out), nil
}

// WorktreeStatus describes the state of a PR's worktree.
type WorktreeStatus struct {
	Path          string
	State         string // "missing", "current", "stale", "error"
	WorktreeHEAD  string // empty when missing/error
	PRHEAD        string // empty when missing/error/unknown
	BehindCount   int    // 0 when current/missing
	PRState       string // "open", "merged", "closed", "unknown"
}

// InspectWorktree checks the worktree for a PR and returns its status.
// It compares the worktree's HEAD SHA to the PR's current HEAD from GitHub.
func InspectWorktree(cfg *Config, repo string, number int) WorktreeStatus {
	safeRepo := strings.ReplaceAll(repo, "/", "-")
	target := filepath.Join(cfg.WorktreeRoot(), safeRepo, fmt.Sprintf("pr-%d", number))

	result := WorktreeStatus{Path: target}

	// Check if path exists
	if _, err := os.Stat(target); os.IsNotExist(err) {
		result.State = "missing"
		return result
	} else if err != nil {
		result.State = "error"
		return result
	}

	// Get worktree HEAD
	wtHead, err := worktreeHEAD(target)
	if err != nil {
		result.State = "error"
		return result
	}
	result.WorktreeHEAD = wtHead

	// Get PR's current HEAD from GitHub
	prHead, err := currentPRHEAD(repo, number)
	if err != nil {
		// GitHub API failed — can't determine freshness
		result.State = "error"
		return result
	}
	result.PRHEAD = prHead

	// Compare
	if wtHead == prHead {
		result.State = "current"
		return result
	}

	// Stale — count how many commits behind
	count, _ := countCommitsBehind(target, prHead)
	result.State = "stale"
	result.BehindCount = count

	// Also check PR state for GC context
	result.PRState = fetchPRState(repo, number)

	return result
}

// countCommitsBehind returns how many commits the worktree is behind the target ref.
// Best-effort — returns 0 on failure (we still know it's stale from the SHA mismatch).
func countCommitsBehind(worktreePath, targetSHA string) (int, error) {
	out, err := shellout.Combined("git", "-C", worktreePath, "rev-list", "--count", fmt.Sprintf("HEAD..%s", targetSHA))
	if err != nil {
		return 0, err
	}
	count := strings.TrimSpace(out)
	n, err := strconv.Atoi(count)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// fetchPRState returns the PR's state (open/merged/closed) from GitHub.
func fetchPRState(repo string, number int) string {
	out, err := shellout.Combined("gh", "pr", "view", fmt.Sprintf("%d", number),
		"--repo", repo,
		"--json", "state",
		"--jq", ".state",
	)
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(out)
}

// isStale is a lightweight check: does the worktree's HEAD differ from the
// PR's current HEAD on GitHub? Returns (stale, behindCount). If the
// worktree doesn't exist yet, it's not stale (it'll be created on resolve).
func isStale(cfg *Config, repo string, number int) (bool, int) {
	safeRepo := strings.ReplaceAll(repo, "/", "-")
	target := filepath.Join(cfg.WorktreeRoot(), safeRepo, fmt.Sprintf("pr-%d", number))

	if _, err := os.Stat(target); os.IsNotExist(err) {
		return false, 0
	}

	wtHead, err := worktreeHEAD(target)
	if err != nil {
		return false, 0
	}

	prHead, err := currentPRHEAD(repo, number)
	if err != nil {
		return false, 0
	}

	if wtHead == prHead {
		return false, 0
	}

	behind, _ := countCommitsBehind(target, prHead)
	return true, behind
}

// RefreshWorktree updates an existing worktree to match the PR's current HEAD.
// It does a hard reset to preserve a clean state. Returns an error if the
// refresh fails, in which case the caller may want to try a full recreate.
func RefreshWorktree(cfg *Config, repo string, number int) error {
	sourcePath := cfg.RepoPath(repo)
	if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
		return fmt.Errorf("source repo not found at %s", sourcePath)
	} else if err != nil {
		return err
	}

	safeRepo := strings.ReplaceAll(repo, "/", "-")
	target := filepath.Join(cfg.WorktreeRoot(), safeRepo, fmt.Sprintf("pr-%d", number))

	// Verify worktree exists
	existing, err := listWorktrees(sourcePath)
	if err != nil {
		return fmt.Errorf("list worktrees: %w", err)
	}
	found := false
	for _, w := range existing {
		if w == target {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("worktree %s is not registered in %s", target, sourcePath)
	}

	// Get the PR's current HEAD
	prHead, err := currentPRHEAD(repo, number)
	if err != nil {
		return err
	}

	// Fetch latest from origin in the source repo
	if _, err := shellout.Combined("git", "-C", sourcePath, "fetch", "origin"); err != nil {
		return fmt.Errorf("git fetch: %w", err)
	}

	// Checkout the PR's HEAD in the worktree
	if _, err := shellout.Combined("git", "-C", target, "fetch", "origin"); err != nil {
		return fmt.Errorf("worktree git fetch: %w", err)
	}

	// Hard reset to the PR's current HEAD
	if _, err := shellout.Combined("git", "-C", target, "reset", "--hard", prHead); err != nil {
		// If reset failed (e.g. detached HEAD issues), try full recreate
		return recreateWorktree(cfg, sourcePath, repo, number, target)
	}

	return nil
}

// recreateWorktree removes and recreates a worktree from scratch.
// Use this as a fallback when refresh fails.
func recreateWorktree(cfg *Config, sourcePath, repo string, number int, target string) error {
	// Remove the old worktree
	if _, err := shellout.Combined("git", "-C", sourcePath, "worktree", "remove", "--force", target); err != nil {
		return fmt.Errorf("git worktree remove: %w", err)
	}

	// Clean up the directory if it still exists
	os.RemoveAll(target)

	// Recreate using resolveWorktree
	_, err := resolveWorktree(cfg, repo, number)
	return err
}

// listWorktrees returns the absolute paths of all registered worktrees for
// the repo at sourcePath.
func listWorktrees(sourcePath string) ([]string, error) {
	out, err := shellout.Output("git", "-C", sourcePath, "worktree", "list", "--porcelain")
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

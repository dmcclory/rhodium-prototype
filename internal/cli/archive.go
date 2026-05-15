package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"rhodium/internal/brain"
	"rhodium/internal/gh"
	"rhodium/internal/rhodium"
)

func cmdArchive(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: rhodium archive <owner/repo#N> [reason]\n" +
			"       rhodium archive list [--json]")
	}
	switch args[0] {
	case "list":
		return cmdArchiveList(args[1:])
	default:
		return cmdArchiveOne(args)
	}
}

// cmdArchiveOne archives a single PR.
// Usage: rhodium archive <owner/repo#N> [merged|closed|manual]
func cmdArchiveOne(args []string) error {
	flags, positional := splitFlags(args)
	_ = flags // no flags yet

	if len(positional) == 0 {
		return fmt.Errorf("usage: rhodium archive <owner/repo#N> [merged|closed|manual]")
	}

	repo, number, err := parsePRRef(positional[0])
	if err != nil {
		return err
	}

	reason := "manual"
	if len(positional) > 1 {
		reason = positional[1]
	}

	cfg, err := rhodium.LoadConfig()
	if err != nil {
		return err
	}

	b, err := brain.OpenForCLI()
	if err != nil {
		return err
	}
	defer b.Close()

	if err := b.ArchivePR(repo, number, reason); err != nil {
		return err
	}

	// Remove the worktree on disk too.
	safeRepo := strings.ReplaceAll(repo, "/", "-")
	target := fmt.Sprintf("%s/%s/pr-%d", cfg.WorktreeRoot(), safeRepo, number)
	prKey := fmt.Sprintf("%s#%d", repo, number)

	if _, err := os.Stat(target); err == nil {
		if _, err := os.Stat(cfg.RepoPath(repo)); err == nil {
			runCombined("git", "-C", cfg.RepoPath(repo), "worktree", "remove", "--force", target)
		}
		os.RemoveAll(target)
		fmt.Printf("archived %s (%s) — worktree removed\n", prKey, reason)
	} else {
		fmt.Printf("archived %s (%s)\n", prKey, reason)
	}

	return nil
}

// cmdArchiveList lists all archived PRs.
// Usage: rhodium archive list [--json]
func cmdArchiveList(args []string) error {
	flags, positional := splitFlags(args)
	if len(positional) > 0 {
		return fmt.Errorf("usage: rhodium archive list [--json]")
	}
	jsonOut := hasFlag(flags, "json")

	b, err := brain.OpenForCLI()
	if err != nil {
		return err
	}
	defer b.Close()

	entries := b.ListArchived()
	if len(entries) == 0 {
		fmt.Println("no archived PRs")
		return nil
	}

	if jsonOut {
		return jsonPrint(entries)
	}

	for _, e := range entries {
		icon := archiveIcon(e.Reason)
		fmt.Printf("  %s %-30s archived %s (%s)\n", icon, e.PRKey, formatTime(e.ArchivedAt), e.Reason)
	}
	fmt.Printf("\n%d archived PRs\n", len(entries))
	return nil
}

func archiveIcon(reason string) string {
	switch reason {
	case "merged":
		return "✓"
	case "closed":
		return "✕"
	default:
		return "·"
	}
}

// cmdGC scans cached PRs for merged/closed ones and archives them.
// Usage: rhodium gc [--older-than 30d] [--yes] [--dry-run]
func cmdGC(args []string) error {
	flags, positional := splitFlags(args, "older-than")
	_ = positional

	yesFlag := hasFlag(flags, "yes") || hasFlag(flags, "y")
	dryRun := hasFlag(flags, "dry-run") || hasFlag(flags, "n")

	olderThan := 30 * 24 * time.Hour // default 30 days
	for i, f := range flags {
		trimmed := strings.TrimLeft(f, "-")
		if trimmed == "older-than" && i+1 < len(flags) {
			d, err := parseDuration(flags[i+1])
			if err != nil {
				return fmt.Errorf("bad --older-than: %w", err)
			}
			olderThan = d
		}
	}

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
	if len(cachedPRs) == 0 {
		fmt.Println("no cached PRs — run the TUI or `rhodium todo --sync` first")
		return nil
	}

	// Build a set of archived PR keys so we skip them.
	archivedKeys := map[string]bool{}
	for _, e := range b.ListArchived() {
		archivedKeys[e.PRKey] = true
	}

	// For each cached PR, check its current state on GitHub.
	// Group by repo for efficient fetching.
	prsByRepo := map[string][]gh.PR{}
	for _, pr := range cachedPRs {
		if archivedKeys[brain.PRKey(pr.Repo, pr.Number)] {
			continue
		}
		prsByRepo[pr.Repo] = append(prsByRepo[pr.Repo], pr)
	}

	type candidate struct {
		pr         gh.PR
		prState    string // "MERGED", "CLOSED", "OPEN"
		mergedAt   string
		shouldGC   bool
		skipReason string
		reason     string // archive reason: "merged", "closed"
	}

	var candidates []candidate
	for repo, prs := range prsByRepo {
		// Fetch fresh PR list with state.
		freshPRs, err := gh.ListPRs(repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to list PRs for %s: %s\n", repo, err)
			// Don't fail — just skip this repo
			continue
		}

		// Build lookup by PR number
		freshByNum := map[int]gh.PR{}
		for _, p := range freshPRs {
			freshByNum[p.Number] = p
		}

		for _, pr := range prs {
			c := candidate{pr: pr}
			fresh, ok := freshByNum[pr.Number]
			if !ok {
				// PR not in the open list — it's merged or closed.
				// Fetch the PR state to determine which.
				state, mergedAt := fetchPRStateAndDate(repo, pr.Number)
				c.prState = state
				c.mergedAt = mergedAt

				if state == "MERGED" || state == "CLOSED" {
					reason := "merged"
					if state == "CLOSED" {
						reason = "closed"
					}

					// Check age
					if mergedAt != "" {
						t, err := time.Parse(time.RFC3339, mergedAt)
						if err != nil {
							t = time.Now() // fallback: include it
						}
						age := time.Since(t)
						if age < olderThan {
							c.skipReason = fmt.Sprintf("too recent (%s old, threshold %s)",
								formatDuration(age), formatDuration(olderThan))
						} else {
							c.shouldGC = true
							c.reason = reason
						}
					} else {
						// No date available — include it
						c.shouldGC = true
						c.reason = reason
					}
				}
			} else {
				// Still open
				c.prState = fresh.State
			}

			candidates = append(candidates, c)
		}
	}

	// Filter to only GC candidates
	var toArchive []candidate
	for _, c := range candidates {
		if c.shouldGC {
			toArchive = append(toArchive, c)
		}
	}

	// Report
	if len(toArchive) == 0 {
		fmt.Println("nothing to archive")
		// Show skipped ones for visibility
		skipped := 0
		for _, c := range candidates {
			if c.skipReason != "" {
				skipped++
			}
		}
		if skipped > 0 {
			fmt.Printf("  %d PR(s) too recent for the current threshold\n", skipped)
		}
		return nil
	}

	if dryRun {
		fmt.Println("dry run — would archive:")
		for _, c := range toArchive {
			fmt.Printf("  %s %s#%d %s (%s)\n", archiveIcon(c.reason), c.pr.Repo, c.pr.Number, c.pr.Title, c.reason)
		}
		fmt.Printf("\n%d PRs would be archived\n", len(toArchive))
		return nil
	}

	// Confirm unless --yes
	if !yesFlag {
		fmt.Printf("Archive %d PR(s)?\n", len(toArchive))
		for _, c := range toArchive {
			fmt.Printf("  %s %s#%d %s (%s)\n", archiveIcon(c.reason), c.pr.Repo, c.pr.Number, c.pr.Title, c.reason)
		}
		fmt.Print("\nConfirm [y/N]: ")
		var response string
		fmt.Scanln(&response)
		if response != "y" && response != "Y" && response != "yes" {
			fmt.Println("aborted")
			return nil
		}
	}

	// Archive them
	archived, freed := 0, 0
	for _, c := range toArchive {
		prKey := brain.PRKey(c.pr.Repo, c.pr.Number)
		reason := c.reason
		if reason == "" {
			reason = "manual"
		}

		if err := b.ArchivePR(c.pr.Repo, c.pr.Number, reason); err != nil {
			fmt.Printf("  ✕ %s: %s\n", prKey, err)
			continue
		}

		// Remove worktree
		safeRepo := strings.ReplaceAll(c.pr.Repo, "/", "-")
		target := fmt.Sprintf("%s/%s/pr-%d", cfg.WorktreeRoot(), safeRepo, c.pr.Number)
		if _, err := os.Stat(target); err == nil {
			if _, err := os.Stat(cfg.RepoPath(c.pr.Repo)); err == nil {
				runCombined("git", "-C", cfg.RepoPath(c.pr.Repo), "worktree", "remove", "--force", target)
			}
			os.RemoveAll(target)
			freed++
		}
		archived++
	}

	fmt.Printf("\narchived %d PRs, freed %d worktrees\n", archived, freed)
	return nil
}

// fetchPRStateAndDate gets the PR state and merged/closed date from GitHub.
func fetchPRStateAndDate(repo string, number int) (state string, mergedAt string) {
	type prStateResult struct {
		State    string `json:"state"`
		MergedAt string `json:"mergedAt"`
		ClosedAt string `json:"closedAt"`
	}

	out, err := runCombinedOut("gh", "pr", "view", fmt.Sprintf("%d", number),
		"--repo", repo,
		"--json", "state,mergedAt,closedAt")
	if err != nil {
		return "unknown", ""
	}

	var result prStateResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return "unknown", ""
	}

	state = result.State
	if state == "MERGED" {
		mergedAt = result.MergedAt
	} else if state == "CLOSED" {
		mergedAt = result.ClosedAt
	}

	return state, mergedAt
}

func formatTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Format("2006-01-02")
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Hour)
	h := d / time.Hour
	if h < 24 {
		return fmt.Sprintf("%dh", h)
	}
	days := h / 24
	return fmt.Sprintf("%dd", days)
}

func parseDuration(s string) (time.Duration, error) {
	// Support: 30d, 7d, 24h, 3600s, etc.
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		days, err := parseInt(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, err
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

func parseInt(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"rhodium/internal/brain"
	"rhodium/internal/gh"
)

// repoItem is one row in `rhodium repos` output.
type repoItem struct {
	Repo    string `json:"repo"`
	PRCount int    `json:"pr_count"`
}

type reposOutput struct {
	Repos []repoItem `json:"repos"`
}

// cmdRepos lists all cached repos with their open PR counts.
func cmdRepos(args []string) error {
	flags, _ := splitFlags(args)
	fs := flag.NewFlagSet("repos", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	sync := fs.Bool("sync", false, "refresh PR cache from GitHub first")
	if err := fs.Parse(flags); err != nil {
		return err
	}

	b, err := brain.OpenForCLI()
	if err != nil {
		return err
	}
	defer b.Close()

	if *sync {
		if err := syncPRCache(b); err != nil {
			return err
		}
	}

	items := buildRepoItems(b)
	sort.Slice(items, func(i, j int) bool { return items[i].Repo < items[j].Repo })

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(reposOutput{Repos: items})
	}

	if len(items) == 0 {
		fmt.Println("repos: nothing in cache. (run with --sync to fetch from GitHub)")
		return nil
	}

	for _, it := range items {
		fmt.Printf("  %-40s  %d %s\n", it.Repo, it.PRCount, pluralize("PR", it.PRCount))
	}
	if !*sync {
		fmt.Println("\n(reading cache — use --sync to refresh from GitHub)")
	}
	return nil
}

// buildRepoItems groups cached PRs by repo.
func buildRepoItems(b *brain.Brain) []repoItem {
	cached := b.CachedPRs()
	counts := map[string]int{}
	for _, p := range cached {
		counts[p.Repo]++
	}
	items := make([]repoItem, 0, len(counts))
	for repo, count := range counts {
		items = append(items, repoItem{Repo: repo, PRCount: count})
	}
	return items
}

// cmdPRs lists PRs for a given repo, or all repos if omitted.
func cmdPRs(args []string) error {
	flags, pos := splitFlags(args)
	fs := flag.NewFlagSet("prs", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	sync := fs.Bool("sync", false, "refresh PR cache from GitHub first")
	if err := fs.Parse(flags); err != nil {
		return err
	}

	repo := ""
	if len(pos) > 0 {
		repo = pos[0]
	}

	b, err := brain.OpenForCLI()
	if err != nil {
		return err
	}
	defer b.Close()

	if *sync {
		if err := syncPRCache(b); err != nil {
			return err
		}
	}

	cached := b.CachedPRs()

	if *asJSON {
		return renderPRsJSON(cached, repo)
	}

	renderPRsText(cached, repo)
	if !*sync {
		fmt.Println("\n(reading cache — use --sync to refresh from GitHub)")
	}
	return nil
}

// renderPRsJSON emits the matching PRs as a JSON array.
func renderPRsJSON(prs []gh.PR, repo string) error {
	out := filterSorted(prs, repo)
	if repo != "" && len(out) == 0 {
		return fmt.Errorf("repo %q not in cache. (run with --sync to refresh)", repo)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// renderPRsText prints PRs in a human-readable table.
func renderPRsText(prs []gh.PR, repo string) {
	if repo == "" {
		// Group by repo, then print each group.
		grouped := groupByRepo(prs)
		if len(grouped) == 0 {
			fmt.Println("prs: nothing in cache. (run with --sync to fetch from GitHub)")
			return
		}
		for _, r := range sortedKeys(grouped) {
			items := grouped[r]
			fmt.Printf("  %s (%d %s)\n", r, len(items), pluralize("PR", len(items)))
			for _, p := range items {
				printPRRow(p)
			}
			fmt.Println()
		}
		return
	}

	items := filterSorted(prs, repo)
	if len(items) == 0 {
		fmt.Printf("prs: repo %q not in cache. (run with --sync to refresh)\n", repo)
		return
	}
	fmt.Printf("  %s (%d %s)\n", repo, len(items), pluralize("PR", len(items)))
	for _, p := range items {
		printPRRow(p)
	}
}

func printPRRow(p gh.PR) {
	title := truncate(p.Title, 50)
	fmt.Printf("  %-36s  %-55s  by %s\n", fmt.Sprintf("%s#%d", p.Repo, p.Number), title, p.Author)
}

// filterSorted returns PRs for the given repo, sorted by number descending.
// Empty repo means "all PRs".
func filterSorted(prs []gh.PR, repo string) []gh.PR {
	var out []gh.PR
	for _, p := range prs {
		if repo == "" || p.Repo == repo {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Number > out[j].Number
	})
	return out
}

func groupByRepo(prs []gh.PR) map[string][]gh.PR {
	m := map[string][]gh.PR{}
	for _, p := range prs {
		m[p.Repo] = append(m[p.Repo], p)
	}
	// Sort each group by number descending.
	for k := range m {
		sort.Slice(m[k], func(i, j int) bool { return m[k][i].Number > m[k][j].Number })
	}
	return m
}

func sortedKeys(m map[string][]gh.PR) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"rhodium/internal/brain"
	"rhodium/internal/gh"
	"rhodium/internal/rhodium"
)

// prTodoItem is one PR's row in the todo dashboard.
type prTodoItem struct {
	Key     string   `json:"key"`
	Repo    string   `json:"repo"`
	Number  int      `json:"number"`
	Title   string   `json:"title"`
	Author  string   `json:"author"`
	Status  string   `json:"status,omitempty"`
	Tags    []string `json:"tags"`
	Notes   int      `json:"notes,omitempty"`
	CatchUp *struct {
		Done       int `json:"done"`
		Total      int `json:"total"`
		LinesDone  int `json:"lines_done"`
		LinesTotal int `json:"lines_total"`
	} `json:"catch_up,omitempty"`
}

type todoOutput struct {
	PRs []prTodoItem `json:"prs"`
}

// cmdTodo prints a global dashboard of PRs with outstanding review work.
func cmdTodo(args []string) error {
	flags, _ := splitFlags(args, "status")
	fs := flag.NewFlagSet("todo", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	sync := fs.Bool("sync", false, "refresh PR cache from GitHub first")
	statusFilter := fs.String("status", "", "only show PRs with this custom status")
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

	items := buildTodoItems(b)
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })

	// Attach statuses and filter.
	if *statusFilter != "" {
		statuses := b.PRStatusByKeys(collectKeys(items))
		var filtered []prTodoItem
		for i := range items {
			if s, ok := statuses[items[i].Key]; ok && s == *statusFilter {
				items[i].Status = s
				filtered = append(filtered, items[i])
			}
		}
		items = filtered
	} else {
		// Still attach statuses for display/JSON.
		statuses := b.PRStatusByKeys(collectKeys(items))
		for i := range items {
			if s, ok := statuses[items[i].Key]; ok {
				items[i].Status = s
			}
		}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(todoOutput{PRs: items})
	}
	renderTodoText(items, *sync)
	return nil
}

// syncPRCache refreshes the brain's PR cache by listing every configured
// repo from GitHub. Per-repo errors warn and continue so a transient
// failure on one repo doesn't blank the cache for the others. If EVERY
// repo errors out (offline / gh auth gone / rate limited) we preserve the
// existing cache rather than wiping it — losing every cached PR for a
// transient network glitch is far worse than a slightly stale list.
func syncPRCache(b *brain.Brain) error {
	cfg, err := rhodium.LoadConfig()
	if err != nil {
		return err
	}
	var all []gh.PR
	var errCount int
	for _, repo := range cfg.Repos {
		prs, err := gh.ListPRs(repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: %s: %v\n", repo, err)
			errCount++
			continue
		}
		all = append(all, prs...)
	}
	if len(all) == 0 && errCount > 0 {
		fmt.Fprintln(os.Stderr, "warn: every repo failed; pr_cache left intact")
		return nil
	}
	if err := b.SetPRCache(all); err != nil {
		return fmt.Errorf("write cache: %w", err)
	}
	return nil
}

// buildTodoItems walks the union of cached PRs, active catch-up sessions,
// and PRs with notes — the three sources that can put a PR on the todo
// list — and returns one prTodoItem per PR with outstanding work.
func buildTodoItems(b *brain.Brain) []prTodoItem {
	cached := b.CachedPRs()
	byKey := map[string]gh.PR{}
	for _, p := range cached {
		byKey[brain.PRKey(p.Repo, p.Number)] = p
	}

	catchUps := map[string]*brain.ReviewSession{}
	sessions := b.AllActiveSessions()
	for i := range sessions {
		catchUps[sessions[i].PRKey] = &sessions[i]
	}

	// Union of all pr_keys with outstanding state — cached PRs plus
	// anything with notes or an active catch-up (so closed / out-of-window
	// PRs with unresolved notes still surface).
	keys := map[string]bool{}
	for k := range byKey {
		keys[k] = true
	}
	for k := range catchUps {
		keys[k] = true
	}
	for _, k := range b.PRKeysWithNotes() {
		keys[k] = true
	}

	var items []prTodoItem
	for key := range keys {
		repo, num, err := parsePRRef(key)
		if err != nil {
			continue
		}
		notes := b.NoteCountForPR(repo, num)
		cu := catchUps[key]
		_, inCache := byKey[key]
		reviewed := len(b.AllFileReviewedStates(repo, num)) > 0 || b.HasAnyMarks(repo, num)

		var tags []string
		if cu != nil {
			tags = append(tags, "catch-up")
		}
		if inCache && !reviewed && cu == nil {
			tags = append(tags, "unseen")
		}
		if notes > 0 {
			tags = append(tags, "notes")
		}
		if len(tags) == 0 {
			continue
		}
		p := byKey[key]
		item := prTodoItem{
			Key: key, Repo: repo, Number: num,
			Title: p.Title, Author: p.Author, Tags: tags, Notes: notes,
		}
		if cu != nil {
			item.CatchUp = &struct {
				Done       int `json:"done"`
				Total      int `json:"total"`
				LinesDone  int `json:"lines_done"`
				LinesTotal int `json:"lines_total"`
			}{Done: cu.FilesDone, Total: cu.FilesTotal, LinesDone: cu.LinesDone, LinesTotal: cu.LinesTotal}
		}
		items = append(items, item)
	}
	return items
}

func collectKeys(items []prTodoItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Key
	}
	return out
}

func renderTodoText(items []prTodoItem, syncedThisRun bool) {
	if len(items) == 0 {
		fmt.Println("todo: nothing pending. (run with --sync to refresh the PR cache)")
		return
	}
	fmt.Printf("%d %s need attention\n\n", len(items), pluralize("PR", len(items)))
	for _, it := range items {
		var suffix []string
		if it.Status != "" {
			suffix = append(suffix, fmt.Sprintf("status:%s", it.Status))
		}
		if it.CatchUp != nil {
			if it.CatchUp.LinesTotal > 0 {
				suffix = append(suffix, fmt.Sprintf("catch-up %d/%d files, %d/%d lines", it.CatchUp.Done, it.CatchUp.Total, it.CatchUp.LinesDone, it.CatchUp.LinesTotal))
			} else {
				suffix = append(suffix, fmt.Sprintf("catch-up %d/%d", it.CatchUp.Done, it.CatchUp.Total))
			}
		}
		if contains(it.Tags, "unseen") {
			suffix = append(suffix, "unseen")
		}
		if it.Notes > 0 {
			suffix = append(suffix, fmt.Sprintf("%d %s", it.Notes, pluralize("note", it.Notes)))
		}
		mid := truncate(it.Title, 40)
		if it.Author != "" {
			mid = fmt.Sprintf("%-40s  by %s", mid, it.Author)
		}
		fmt.Printf("  %-28s  %s  [%s]\n", it.Key, mid, strings.Join(suffix, ", "))
	}
	if !syncedThisRun {
		fmt.Println("\n(reading cache — use --sync to refresh from GitHub)")
	}
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

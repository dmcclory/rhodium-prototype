package gh

import (
	"encoding/json"
	"fmt"
	"strings"

	"rhodium/internal/shellout"
)

// Commit is a single commit on a PR branch, flattened from the gh API's
// nested { sha, commit: { author, message } } shape so callers don't have
// to navigate two levels. Title is the first line of the message;
// Message is the full body (useful for --verbose / --json).
type Commit struct {
	SHA     string
	Title   string
	Message string
	Author  string // github login when available, falling back to commit.author.name
	Date    string // ISO8601, from commit.author.date
}

type ghAPICommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name string `json:"name"`
			Date string `json:"date"`
		} `json:"author"`
	} `json:"commit"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Files []ghAPIFile `json:"files"` // populated by FetchCommitFiles, absent on the list endpoint
}

// ListPRCommits returns the commits on a PR in author/committer order
// (oldest first — the order GitHub returns them). Pagination is required
// for PRs with > 100 commits, but rare for review-scale work.
func ListPRCommits(repo string, number int) ([]Commit, error) {
	out, err := shellout.Output("gh", "api",
		"--paginate",
		fmt.Sprintf("repos/%s/pulls/%d/commits", repo, number),
	)
	if err != nil {
		return nil, fmt.Errorf("gh api pulls commits %s#%d: %w", repo, number, err)
	}
	var items []ghAPICommit
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("parse commits json: %w", err)
	}
	commits := make([]Commit, 0, len(items))
	for _, it := range items {
		commits = append(commits, Commit{
			SHA:     it.SHA,
			Title:   firstLine(it.Commit.Message),
			Message: it.Commit.Message,
			Author:  pickAuthor(it.Author.Login, it.Commit.Author.Name),
			Date:    it.Commit.Author.Date,
		})
	}
	return commits, nil
}

// FetchCommitFiles returns the per-file patches that commit introduced.
// Uses the single-commit endpoint which includes a files array with
// patches — same shape as pulls/:n/files per file. Merge commits often
// return empty patches; callers should treat missing patches as "no
// reviewable hunks from this commit".
func FetchCommitFiles(repo, sha string) ([]FileChange, error) {
	out, err := shellout.Output("gh", "api",
		fmt.Sprintf("repos/%s/commits/%s", repo, sha),
	)
	if err != nil {
		return nil, fmt.Errorf("gh api commit %s %s: %w", repo, sha, err)
	}
	var c ghAPICommit
	if err := json.Unmarshal(out, &c); err != nil {
		return nil, fmt.Errorf("parse commit json: %w", err)
	}
	files := make([]FileChange, 0, len(c.Files))
	for _, it := range c.Files {
		files = append(files, FileChange{
			Path:      it.Filename,
			Additions: it.Additions,
			Deletions: it.Deletions,
			Blob:      it.Sha,
			Patch:     it.Patch,
		})
	}
	return files, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func pickAuthor(login, name string) string {
	if login != "" {
		return login
	}
	return name
}

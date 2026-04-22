package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type PR struct {
	Repo    string
	Number  int
	Title   string
	Author  string
	HeadSHA string
	BaseSHA string
	Body    string
}

type prListItem struct {
	Number     int    `json:"number"`
	Title      string `json:"title"`
	HeadRefOid string `json:"headRefOid"`
	BaseRefOid string `json:"baseRefOid"`
	Body       string `json:"body"`
	Author     struct {
		Login string `json:"login"`
	} `json:"author"`
}

func listPRs(repo string) ([]PR, error) {
	out, err := exec.Command("gh", "pr", "list",
		"--repo", repo,
		"--json", "number,title,author,headRefOid,baseRefOid,body",
		"--limit", "50",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr list %s: %w", repo, err)
	}

	var items []prListItem
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, err
	}

	prs := make([]PR, 0, len(items))
	for _, it := range items {
		prs = append(prs, PR{
			Repo:    repo,
			Number:  it.Number,
			Title:   it.Title,
			Author:  it.Author.Login,
			HeadSHA: it.HeadRefOid,
			BaseSHA: it.BaseRefOid,
			Body:    it.Body,
		})
	}
	return prs, nil
}

type FileChange struct {
	Path      string
	Additions int
	Deletions int
	Blob      string // blob SHA at the PR's current head
	Patch     string // unified diff vs base (may be empty for binary or huge files)
}

type ghAPIFile struct {
	Sha       string `json:"sha"`
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch"`
}

// listPRFiles fetches the PR's changed files via `gh api`, returning per-file
// blob SHAs and patches in a single call.
func listPRFiles(repo string, number int) ([]FileChange, error) {
	out, err := exec.Command("gh", "api",
		"--paginate",
		fmt.Sprintf("repos/%s/pulls/%d/files", repo, number),
	).Output()
	if err != nil {
		return nil, fmt.Errorf("gh api pulls files %s#%d: %w", repo, number, err)
	}
	var items []ghAPIFile
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("parse files json: %w", err)
	}
	files := make([]FileChange, 0, len(items))
	for _, it := range items {
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

// fetchCompare returns the files that changed between two commits using the
// GitHub compare API. Files not in the result haven't changed — they're
// automatically caught up. The returned FileChanges include patches for only
// the delta between base and head.
func fetchCompare(repo, base, head string) ([]FileChange, error) {
	out, err := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/compare/%s...%s", repo, base, head),
	).Output()
	if err != nil {
		return nil, fmt.Errorf("gh api compare %s %s...%s: %w", repo, base, head, err)
	}
	var result struct {
		Files []ghAPIFile `json:"files"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("parse compare json: %w", err)
	}
	files := make([]FileChange, 0, len(result.Files))
	for _, it := range result.Files {
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

// fetchFileAtRef fetches file content at a specific git ref (commit SHA, branch).
// Returns "" if the file doesn't exist at that ref (e.g., new file).
func fetchFileAtRef(repo, path, ref string) (string, error) {
	out, err := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/contents/%s?ref=%s", repo, path, ref),
	).Output()
	if err != nil {
		// File might not exist at this ref (new file). That's OK.
		return "", nil
	}
	var content struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(out, &content); err != nil {
		return "", nil
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(content.Content, "\n", ""))
	if err != nil {
		return "", nil
	}
	return string(decoded), nil
}

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
	Files []ghAPIFile `json:"files"` // populated by fetchCommitFiles, absent on the list endpoint
}

// listPRCommits returns the commits on a PR in author/committer order
// (oldest first — the order GitHub returns them). Pagination is required
// for PRs with > 100 commits, but rare for review-scale work.
func listPRCommits(repo string, number int) ([]Commit, error) {
	out, err := exec.Command("gh", "api",
		"--paginate",
		fmt.Sprintf("repos/%s/pulls/%d/commits", repo, number),
	).Output()
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

// fetchCommitFiles returns the per-file patches that commit introduced.
// Uses the single-commit endpoint which includes a files array with
// patches — same shape as pulls/:n/files per file. Merge commits often
// return empty patches; callers should treat missing patches as "no
// reviewable hunks from this commit".
func fetchCommitFiles(repo, sha string) ([]FileChange, error) {
	out, err := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/commits/%s", repo, sha),
	).Output()
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

func fetchBlob(repo, sha string) (string, error) {
	out, err := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/git/blobs/%s", repo, sha),
	).Output()
	if err != nil {
		return "", fmt.Errorf("gh api blob %s %s: %w", repo, sha, err)
	}
	var blob struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(out, &blob); err != nil {
		return "", err
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(blob.Content, "\n", ""))
	if err != nil {
		return "", fmt.Errorf("decode blob: %w", err)
	}
	return string(decoded), nil
}


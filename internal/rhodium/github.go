package rhodium

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
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

	// Live status fields — fetched fresh from `gh pr list` each session and
	// not persisted to pr_cache. They render blank for the first ~second
	// after a cold start (until the first listPRs returns) and then fill in.
	ReviewDecision string // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, ""
	IsDraft        bool
	Mergeable      string // MERGEABLE, CONFLICTING, UNKNOWN, ""
	CIStatus       string // SUCCESS, FAILURE, PENDING, ""
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
	ReviewDecision    string                `json:"reviewDecision"`
	IsDraft           bool                  `json:"isDraft"`
	Mergeable         string                `json:"mergeable"`
	StatusCheckRollup []ghStatusCheckRollup `json:"statusCheckRollup"`
}

// ghStatusCheckRollup is one entry in `gh pr list --json statusCheckRollup`.
// GitHub mixes two shapes: legacy commit statuses populate `state`, while
// modern check runs populate `status`+`conclusion`. We coalesce them in
// rollupCIStatus.
type ghStatusCheckRollup struct {
	State      string `json:"state"`      // SUCCESS, FAILURE, PENDING, ERROR
	Status     string `json:"status"`     // QUEUED, IN_PROGRESS, COMPLETED
	Conclusion string `json:"conclusion"` // SUCCESS, FAILURE, NEUTRAL, CANCELLED, SKIPPED, TIMED_OUT, ACTION_REQUIRED, STALE
}

func listPRs(repo string) ([]PR, error) {
	out, err := exec.Command("gh", "pr", "list",
		"--repo", repo,
		"--json", "number,title,author,headRefOid,baseRefOid,body,reviewDecision,isDraft,mergeable,statusCheckRollup",
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
			Repo:           repo,
			Number:         it.Number,
			Title:          it.Title,
			Author:         it.Author.Login,
			HeadSHA:        it.HeadRefOid,
			BaseSHA:        it.BaseRefOid,
			Body:           it.Body,
			ReviewDecision: it.ReviewDecision,
			IsDraft:        it.IsDraft,
			Mergeable:      it.Mergeable,
			CIStatus:       rollupCIStatus(it.StatusCheckRollup),
		})
	}
	return prs, nil
}

// rollupCIStatus coalesces the per-check entries into a single PR-level CI
// state. Failure beats anything pending; pending beats success; an empty
// rollup yields "" so the caller can omit the badge entirely.
func rollupCIStatus(checks []ghStatusCheckRollup) string {
	if len(checks) == 0 {
		return ""
	}
	hasPending := false
	for _, c := range checks {
		// Legacy status path.
		if c.State != "" {
			switch c.State {
			case "FAILURE", "ERROR":
				return "FAILURE"
			case "PENDING":
				hasPending = true
			}
			continue
		}
		// Check-run path: only `conclusion` is meaningful once status is
		// COMPLETED. Anything else is in flight.
		if c.Status != "COMPLETED" {
			hasPending = true
			continue
		}
		switch c.Conclusion {
		case "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED":
			return "FAILURE"
		}
	}
	if hasPending {
		return "PENDING"
	}
	return "SUCCESS"
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

// Contributor is a flattened row from GET repos/:o/:r/contributors. Login
// is the @-mention handle; Contributions drives sort order in the picker.
type Contributor struct {
	Login         string
	Contributions int
}

type ghAPIContributor struct {
	Login         string `json:"login"`
	Contributions int    `json:"contributions"`
}

// listContributors pulls up to a few hundred contributors via the GitHub API
// (sorted by contribution count, descending — GitHub's default). One call
// per repo is cached on *app for the rest of the session; this function has
// no caching of its own.
func listContributors(repo string) ([]Contributor, error) {
	out, err := exec.Command("gh", "api",
		"--paginate",
		fmt.Sprintf("repos/%s/contributors?per_page=100", repo),
	).Output()
	if err != nil {
		return nil, fmt.Errorf("gh api contributors %s: %w", repo, err)
	}
	// --paginate concatenates JSON arrays by stripping the outer brackets
	// between pages, but `gh` already emits one valid array for us when the
	// response fits in a single page. For multi-page it emits a single merged
	// array, so a plain unmarshal handles both cases.
	var items []ghAPIContributor
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("parse contributors json: %w", err)
	}
	contribs := make([]Contributor, 0, len(items))
	for _, it := range items {
		if it.Login == "" {
			continue // anonymous contributors have no login
		}
		contribs = append(contribs, Contributor{Login: it.Login, Contributions: it.Contributions})
	}
	return contribs, nil
}

// InlineComment is the payload for POST pulls/:n/comments. commit_id must be
// a sha that's part of the PR (typically HeadSHA). path is the file relative
// to the repo root; line is the new-file (post-change) line number — same
// numbering human notes already use.
type InlineComment struct {
	Body     string
	Path     string
	CommitID string
	Line     int
}

type ghAPIComment struct {
	ID int64 `json:"id"`
}

// postInlineComment posts a single PR review comment tied to a specific line.
// Returns the GitHub comment id so the caller can stamp it onto the local
// note. We pass side=RIGHT because notes anchor to new-file line numbers.
//
// This is a "standalone" comment (not part of a pending review) — it becomes
// visible on the PR immediately. If you want batch semantics, switch to the
// reviews endpoint with `comments: [...]`, but the user explicitly asked for
// fire-and-forget per-comment publication.
func postInlineComment(repo string, prNum int, c InlineComment) (int64, error) {
	args := []string{
		"api",
		"--method", "POST",
		fmt.Sprintf("repos/%s/pulls/%d/comments", repo, prNum),
		"-f", "body=" + c.Body,
		"-f", "commit_id=" + c.CommitID,
		"-f", "path=" + c.Path,
		"-f", "side=RIGHT",
		"-F", fmt.Sprintf("line=%d", c.Line),
	}
	cmd := exec.Command("gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("gh api post comment %s#%d %s:%d: %w (%s)", repo, prNum, c.Path, c.Line, err, strings.TrimSpace(stderr.String()))
	}
	var got ghAPIComment
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		return 0, fmt.Errorf("parse comment response: %w", err)
	}
	if got.ID == 0 {
		return 0, fmt.Errorf("comment posted but no id in response")
	}
	return got.ID, nil
}

// ReviewEvent is the `event` field of POST pulls/:n/reviews. GitHub accepts
// APPROVE, REQUEST_CHANGES, COMMENT, or PENDING (we don't expose PENDING —
// nothing in the UI would let you come back and finish it).
type ReviewEvent string

const (
	ReviewApprove        ReviewEvent = "APPROVE"
	ReviewRequestChanges ReviewEvent = "REQUEST_CHANGES"
	ReviewComment        ReviewEvent = "COMMENT"
)

// submitReview submits a PR review with the given event. body may be empty
// for APPROVE; GitHub rejects an empty body with REQUEST_CHANGES/COMMENT so
// the caller should validate before calling.
func submitReview(repo string, prNum int, event ReviewEvent, body string) error {
	args := []string{
		"api",
		"--method", "POST",
		fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, prNum),
		"-f", "event=" + string(event),
	}
	if body != "" {
		args = append(args, "-f", "body="+body)
	}
	cmd := exec.Command("gh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh api submit review %s#%d %s: %w (%s)", repo, prNum, event, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// fetchGitHubUser returns the login of whoever `gh` is authenticated as.
// Used to auto-populate Config.GitHubUser when the user hasn't set it.
func fetchGitHubUser() (string, error) {
	out, err := exec.Command("gh", "api", "user").Output()
	if err != nil {
		return "", fmt.Errorf("gh api user: %w", err)
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(out, &u); err != nil {
		return "", fmt.Errorf("parse gh api user: %w", err)
	}
	return u.Login, nil
}

// MergeMethod is the `merge_method` field of PUT pulls/:n/merge.
type MergeMethod string

const (
	MergeSquash MergeMethod = "squash"
	MergeMerge  MergeMethod = "merge"
	MergeRebase MergeMethod = "rebase"
)

// mergePR merges a PR. message is the commit message body; empty lets GitHub
// generate the default (usually the PR body for squash, or a "Merge pull
// request #N" line for merge commits).
func mergePR(repo string, prNum int, method MergeMethod, message string) error {
	args := []string{
		"api",
		"--method", "PUT",
		fmt.Sprintf("repos/%s/pulls/%d/merge", repo, prNum),
		"-f", "merge_method=" + string(method),
	}
	if message != "" {
		args = append(args, "-f", "commit_message="+message)
	}
	cmd := exec.Command("gh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh api merge %s#%d %s: %w (%s)", repo, prNum, method, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// GHComment is a unified view over GitHub's three comment streams on a PR:
//
//   - "issue":   general PR comments (the `issues/:n/comments` endpoint).
//   - "review":  the wrapper a reviewer posts when they hit Approve / Request
//     Changes / Comment. State carries APPROVED / CHANGES_REQUESTED / COMMENTED.
//   - "inline":  per-line review comments anchored to a path:line.
//
// The diff view renders inline comments alongside local notes; the PR-level
// comments view shows everything sorted by CreatedAt.
type GHComment struct {
	Type      string // "issue", "review", or "inline"
	Author    string
	Body      string
	CreatedAt string
	State     string // review only: APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED
	Path      string // inline only
	Line      int    // inline only (new-file line, GitHub's `line` field)
	GHID      int64  // gh comment id; matches Note.GitHubCommentID for de-dupe
}

type ghIssueComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

type ghReview struct {
	ID          int64  `json:"id"`
	Body        string `json:"body"`
	State       string `json:"state"`
	SubmittedAt string `json:"submitted_at"`
	User        struct {
		Login string `json:"login"`
	} `json:"user"`
}

type ghInlineComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	CreatedAt string `json:"created_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

// fetchPRComments pulls all three comment streams for a PR. Best-effort:
// individual stream errors are swallowed so the UI gets whatever's available
// (e.g. a PR with no inline comments still returns reviews + issue comments).
func fetchPRComments(repo string, number int) ([]GHComment, error) {
	var out []GHComment

	if data, err := exec.Command("gh", "api", "--paginate",
		fmt.Sprintf("repos/%s/issues/%d/comments", repo, number),
	).Output(); err == nil {
		var items []ghIssueComment
		if json.Unmarshal(data, &items) == nil {
			for _, it := range items {
				out = append(out, GHComment{
					Type:      "issue",
					Author:    it.User.Login,
					Body:      it.Body,
					CreatedAt: it.CreatedAt,
					GHID:      it.ID,
				})
			}
		}
	}

	if data, err := exec.Command("gh", "api", "--paginate",
		fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, number),
	).Output(); err == nil {
		var items []ghReview
		if json.Unmarshal(data, &items) == nil {
			for _, it := range items {
				// Skip empty PENDING reviews (drafts the user hasn't submitted).
				if it.State == "PENDING" {
					continue
				}
				// Skip empty-body APPROVED/COMMENTED entries with no associated
				// inline comments — they'd just be noise. Inline comments still
				// surface as their own stream below.
				if it.Body == "" && it.State != "CHANGES_REQUESTED" && it.State != "DISMISSED" {
					if it.State == "APPROVED" {
						// Always show approvals — they're meaningful even with
						// no body.
					} else {
						continue
					}
				}
				out = append(out, GHComment{
					Type:      "review",
					Author:    it.User.Login,
					Body:      it.Body,
					CreatedAt: it.SubmittedAt,
					State:     it.State,
					GHID:      it.ID,
				})
			}
		}
	}

	if data, err := exec.Command("gh", "api", "--paginate",
		fmt.Sprintf("repos/%s/pulls/%d/comments", repo, number),
	).Output(); err == nil {
		var items []ghInlineComment
		if json.Unmarshal(data, &items) == nil {
			for _, it := range items {
				out = append(out, GHComment{
					Type:      "inline",
					Author:    it.User.Login,
					Body:      it.Body,
					CreatedAt: it.CreatedAt,
					Path:      it.Path,
					Line:      it.Line,
					GHID:      it.ID,
				})
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
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


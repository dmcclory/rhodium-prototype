package gh

import (
	"encoding/json"
	"fmt"
	"sort"

	"rhodium/internal/shellout"
)

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

// Comment is a unified view over GitHub's three comment streams on a PR:
//
//   - "issue":   general PR comments (the `issues/:n/comments` endpoint).
//   - "review":  the wrapper a reviewer posts when they hit Approve / Request
//     Changes / Comment. State carries APPROVED / CHANGES_REQUESTED / COMMENTED.
//   - "inline":  per-line review comments anchored to a path:line.
//
// The diff view renders inline comments alongside local notes; the PR-level
// comments view shows everything sorted by CreatedAt.
type Comment struct {
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

// PostInlineComment posts a single PR review comment tied to a specific line.
// Returns the GitHub comment id so the caller can stamp it onto the local
// note. We pass side=RIGHT because notes anchor to new-file line numbers.
//
// This is a "standalone" comment (not part of a pending review) — it becomes
// visible on the PR immediately. If you want batch semantics, switch to the
// reviews endpoint with `comments: [...]`, but the user explicitly asked for
// fire-and-forget per-comment publication.
func PostInlineComment(repo string, prNum int, c InlineComment) (int64, error) {
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
	out, err := shellout.Output("gh", args...)
	if err != nil {
		return 0, fmt.Errorf("gh api post comment %s#%d %s:%d: %w", repo, prNum, c.Path, c.Line, err)
	}
	var got ghAPIComment
	if err := json.Unmarshal(out, &got); err != nil {
		return 0, fmt.Errorf("parse comment response: %w", err)
	}
	if got.ID == 0 {
		return 0, fmt.Errorf("comment posted but no id in response")
	}
	return got.ID, nil
}

// ReplyToInlineComment posts a reply to an existing inline comment thread.
// replyToID can be any comment id in the thread — GitHub routes the new
// comment to the same root. Returns the new comment's id.
func ReplyToInlineComment(repo string, prNum int, replyToID int64, body string) (int64, error) {
	args := []string{
		"api",
		"--method", "POST",
		fmt.Sprintf("repos/%s/pulls/%d/comments/%d/replies", repo, prNum, replyToID),
		"-f", "body=" + body,
	}
	out, err := shellout.Output("gh", args...)
	if err != nil {
		return 0, fmt.Errorf("gh api reply %s#%d→%d: %w", repo, prNum, replyToID, err)
	}
	var got ghAPIComment
	if err := json.Unmarshal(out, &got); err != nil {
		return 0, fmt.Errorf("parse reply response: %w", err)
	}
	if got.ID == 0 {
		return 0, fmt.Errorf("reply posted but no id in response")
	}
	return got.ID, nil
}

// FetchPRComments pulls all three comment streams for a PR. Best-effort:
// individual stream errors are swallowed so the UI gets whatever's available
// (e.g. a PR with no inline comments still returns reviews + issue comments).
func FetchPRComments(repo string, number int) ([]Comment, error) {
	var out []Comment

	if data, err := shellout.Output("gh", "api", "--paginate",
		fmt.Sprintf("repos/%s/issues/%d/comments", repo, number),
	); err == nil {
		var items []ghIssueComment
		if json.Unmarshal(data, &items) == nil {
			for _, it := range items {
				out = append(out, Comment{
					Type:      "issue",
					Author:    it.User.Login,
					Body:      it.Body,
					CreatedAt: it.CreatedAt,
					GHID:      it.ID,
				})
			}
		}
	}

	if data, err := shellout.Output("gh", "api", "--paginate",
		fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, number),
	); err == nil {
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
				out = append(out, Comment{
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

	if data, err := shellout.Output("gh", "api", "--paginate",
		fmt.Sprintf("repos/%s/pulls/%d/comments", repo, number),
	); err == nil {
		var items []ghInlineComment
		if json.Unmarshal(data, &items) == nil {
			for _, it := range items {
				out = append(out, Comment{
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

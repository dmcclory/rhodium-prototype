package gh

import (
	"fmt"

	"rhodium/internal/shellout"
)

// ReviewEvent is the `event` field of POST pulls/:n/reviews. GitHub accepts
// APPROVE, REQUEST_CHANGES, COMMENT, or PENDING (we don't expose PENDING —
// nothing in the UI would let you come back and finish it).
type ReviewEvent string

const (
	ReviewApprove        ReviewEvent = "APPROVE"
	ReviewRequestChanges ReviewEvent = "REQUEST_CHANGES"
	ReviewComment        ReviewEvent = "COMMENT"
)

// SubmitReview submits a PR review with the given event. body may be empty
// for APPROVE; GitHub rejects an empty body with REQUEST_CHANGES/COMMENT so
// the caller should validate before calling.
func SubmitReview(repo string, prNum int, event ReviewEvent, body string) error {
	args := []string{
		"api",
		"--method", "POST",
		fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, prNum),
		"-f", "event=" + string(event),
	}
	if body != "" {
		args = append(args, "-f", "body="+body)
	}
	if err := shellout.Run("gh", args...); err != nil {
		return fmt.Errorf("gh api submit review %s#%d %s: %w", repo, prNum, event, err)
	}
	return nil
}

// MergeMethod is the `merge_method` field of PUT pulls/:n/merge.
type MergeMethod string

const (
	MergeSquash MergeMethod = "squash"
	MergeMerge  MergeMethod = "merge"
	MergeRebase MergeMethod = "rebase"
)

// MergePR merges a PR. message is the commit message body; empty lets GitHub
// generate the default (usually the PR body for squash, or a "Merge pull
// request #N" line for merge commits).
func MergePR(repo string, prNum int, method MergeMethod, message string) error {
	args := []string{
		"api",
		"--method", "PUT",
		fmt.Sprintf("repos/%s/pulls/%d/merge", repo, prNum),
		"-f", "merge_method=" + string(method),
	}
	if message != "" {
		args = append(args, "-f", "commit_message="+message)
	}
	if err := shellout.Run("gh", args...); err != nil {
		return fmt.Errorf("gh api merge %s#%d %s: %w", repo, prNum, method, err)
	}
	return nil
}

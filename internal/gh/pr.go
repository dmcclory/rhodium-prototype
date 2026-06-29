package gh

import (
	"encoding/json"
	"fmt"

	"rhodium/internal/shellout"
)

// ListPRsFn is the injectable seam for ListPRs so tests can swap in
// deterministic stubs and restore via t.Cleanup. Production code MUST call
// gh.ListPRs (which dispatches through this var).
var ListPRsFn = listPRsReal

type PR struct {
	Repo    string
	Number  int
	Title   string
	Author  string
	HeadSHA string
	BaseSHA string
	// BaseBranch is the branch this PR targets (e.g. "develop", "main"), from
	// GitHub's baseRefName. Used as the zero-config fallback for the branch a
	// review — and any agent it hands off to — compares against.
	BaseBranch string
	Body       string
	State      string // "OPEN", "MERGED", "CLOSED"

	// Live status fields — fetched fresh from `gh pr list` each session and
	// not persisted to pr_cache. They render blank for the first ~second
	// after a cold start (until the first listPRs returns) and then fill in.
	ReviewDecision string // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, ""
	IsDraft        bool
	Mergeable      string // MERGEABLE, CONFLICTING, UNKNOWN, ""
	CIStatus       string // SUCCESS, FAILURE, PENDING, ""

	// UpdatedAt is the PR's last-activity timestamp from GitHub.
	// Used by the refresh tick to gate expensive comment re-fetches.
	UpdatedAt string // ISO8601, e.g. "2026-05-23T14:30:00Z"
}

type prListItem struct {
	Number     int    `json:"number"`
	Title      string `json:"title"`
	HeadRefOid  string `json:"headRefOid"`
	BaseRefOid  string `json:"baseRefOid"`
	BaseRefName string `json:"baseRefName"`
	Body        string `json:"body"`
	State      string `json:"state"`
	Author     struct {
		Login string `json:"login"`
	} `json:"author"`
	UpdatedAt         string                `json:"updatedAt"`
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

func ListPRs(repo string) ([]PR, error) {
	return ListPRsFn(repo)
}

func listPRsReal(repo string) ([]PR, error) {
	out, err := shellout.Output("gh", "pr", "list",
		"--repo", repo,
		"--json", "number,title,author,headRefOid,baseRefOid,baseRefName,body,state,updatedAt,reviewDecision,isDraft,mergeable,statusCheckRollup",
		"--limit", "50",
	)
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
			BaseBranch:     it.BaseRefName,
			Body:           it.Body,
			State:          it.State,
			ReviewDecision: it.ReviewDecision,
			IsDraft:        it.IsDraft,
			Mergeable:      it.Mergeable,
			CIStatus:       rollupCIStatus(it.StatusCheckRollup),
			UpdatedAt:      it.UpdatedAt,
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

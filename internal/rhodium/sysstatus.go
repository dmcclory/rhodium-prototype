package rhodium

import "rhodium/internal/gh"

// SystemStatus derives an auto-computed status badge from a PR's observable
// state. Returns "" when nothing noteworthy is going on (no badge rendered).
//
// Priority order (highest → lowest):
//
//	merge conflict  → "merge conflict"
//	CI failure      → "CI failing"
//	CI pending      → "CI pending"
//	draft           → "draft"
//	changes req     → "changes requested"
//	approved        → "approved"
func SystemStatus(pr gh.PR) string {
	if pr.Mergeable == "CONFLICTING" {
		return "merge conflict"
	}
	if pr.CIStatus == "FAILURE" {
		return "CI failing"
	}
	if pr.CIStatus == "PENDING" {
		return "CI pending"
	}
	if pr.IsDraft {
		return "draft"
	}
	if pr.ReviewDecision == "CHANGES_REQUESTED" {
		return "changes requested"
	}
	if pr.ReviewDecision == "APPROVED" {
		return "approved"
	}
	return ""
}

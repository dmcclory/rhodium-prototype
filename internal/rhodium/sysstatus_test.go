package rhodium

import (
	"testing"

	"rhodium/internal/gh"
)

func TestSystemStatus_MergeConflict(t *testing.T) {
	pr := gh.PR{Mergeable: "CONFLICTING"}
	if got := SystemStatus(pr); got != "merge conflict" {
		t.Errorf("got %q, want 'merge conflict'", got)
	}
}

func TestSystemStatus_CIFailure(t *testing.T) {
	pr := gh.PR{CIStatus: "FAILURE"}
	if got := SystemStatus(pr); got != "CI failing" {
		t.Errorf("got %q, want 'CI failing'", got)
	}
}

func TestSystemStatus_CIPending(t *testing.T) {
	pr := gh.PR{CIStatus: "PENDING"}
	if got := SystemStatus(pr); got != "CI pending" {
		t.Errorf("got %q, want 'CI pending'", got)
	}
}

func TestSystemStatus_Draft(t *testing.T) {
	pr := gh.PR{IsDraft: true}
	if got := SystemStatus(pr); got != "draft" {
		t.Errorf("got %q, want 'draft'", got)
	}
}

func TestSystemStatus_ChangesRequested(t *testing.T) {
	pr := gh.PR{ReviewDecision: "CHANGES_REQUESTED"}
	if got := SystemStatus(pr); got != "changes requested" {
		t.Errorf("got %q, want 'changes requested'", got)
	}
}

func TestSystemStatus_Approved(t *testing.T) {
	pr := gh.PR{ReviewDecision: "APPROVED"}
	if got := SystemStatus(pr); got != "approved" {
		t.Errorf("got %q, want 'approved'", got)
	}
}

func TestSystemStatus_Empty(t *testing.T) {
	pr := gh.PR{}
	if got := SystemStatus(pr); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestSystemStatus_MergeConflictWinsOverCI(t *testing.T) {
	// Merge conflict is highest priority, even when CI is failing.
	pr := gh.PR{Mergeable: "CONFLICTING", CIStatus: "FAILURE"}
	if got := SystemStatus(pr); got != "merge conflict" {
		t.Errorf("got %q, want 'merge conflict'", got)
	}
}

func TestSystemStatus_CIFailureWinsOverDraft(t *testing.T) {
	pr := gh.PR{CIStatus: "FAILURE", IsDraft: true}
	if got := SystemStatus(pr); got != "CI failing" {
		t.Errorf("got %q, want 'CI failing'", got)
	}
}

func TestSystemStatus_ApprovedWinsOverEmpty(t *testing.T) {
	pr := gh.PR{ReviewDecision: "APPROVED", CIStatus: "SUCCESS"}
	if got := SystemStatus(pr); got != "approved" {
		t.Errorf("got %q, want 'approved'", got)
	}
}

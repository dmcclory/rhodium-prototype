package brain

import (
	"path/filepath"
	"rhodium/internal/gh"
	"testing"
)

func TestBrainGitHubComments(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// Empty initially.
	if b.HasGitHubComments("acme/web", 42) {
		t.Error("fresh: should have no comments")
	}
	if n := b.GitHubCommentCountForPR("acme/web", 42); n != 0 {
		t.Errorf("fresh: got %d comments, want 0", n)
	}

	// Sync three comment types.
	comments := []gh.Comment{
		{Type: "issue", Author: "alice", Body: "general comment", CreatedAt: "2025-01-01T00:00:00Z", GHID: 101},
		{Type: "review", Author: "bob", Body: "LGTM", CreatedAt: "2025-01-01T01:00:00Z", State: "APPROVED", GHID: 201},
		{Type: "inline", Author: "carol", Body: "fix this", CreatedAt: "2025-01-01T02:00:00Z", Path: "src/main.go", Line: 10, GHID: 301},
	}

	inserted, err := b.SyncGitHubCommentsFromData(comments, "acme/web", 42)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 3 {
		t.Fatalf("first sync: got %d inserted, want 3", inserted)
	}

	// Read-back: GitHubCommentsForPR returns all three.
	all := b.GitHubCommentsForPR("acme/web", 42)
	if len(all) != 3 {
		t.Fatalf("forPR: got %d, want 3", len(all))
	}
	// Ordered by created_at.
	if all[0].Type != "issue" || all[0].Author != "alice" {
		t.Errorf("first: got type=%q author=%q", all[0].Type, all[0].Author)
	}
	if all[1].Type != "review" || all[1].State != "APPROVED" {
		t.Errorf("second: got type=%q state=%q", all[1].Type, all[1].State)
	}
	if all[2].Type != "inline" || all[2].Path != "src/main.go" || all[2].Line != 10 {
		t.Errorf("third: got type=%q path=%q line=%d", all[2].Type, all[2].Path, all[2].Line)
	}

	// Per-file query only returns inline comments for that file.
	fileComments := b.GitHubCommentsForFile("acme/web", 42, "src/main.go")
	if len(fileComments) != 1 || fileComments[0].Body != "fix this" {
		t.Errorf("forFile: got %+v", fileComments)
	}

	// Different file returns empty.
	otherFile := b.GitHubCommentsForFile("acme/web", 42, "other.go")
	if len(otherFile) != 0 {
		t.Errorf("other file: got %d, want 0", len(otherFile))
	}

	// Idempotent: re-sync inserts 0.
	again, err := b.SyncGitHubCommentsFromData(comments, "acme/web", 42)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Errorf("re-sync: got %d inserted, want 0", again)
	}

	// Mixed: existing + new comments.
	newComments := []gh.Comment{
		{Type: "issue", Author: "alice", Body: "general comment", CreatedAt: "2025-01-01T00:00:00Z", GHID: 101}, // duplicate
		{Type: "inline", Author: "dave", Body: "nice", CreatedAt: "2025-01-01T03:00:00Z", Path: "src/main.go", Line: 20, GHID: 302},
	}
	mixed, err := b.SyncGitHubCommentsFromData(newComments, "acme/web", 42)
	if err != nil {
		t.Fatal(err)
	}
	if mixed != 1 {
		t.Errorf("mixed sync: got %d inserted, want 1", mixed)
	}
	if n := b.GitHubCommentCountForPR("acme/web", 42); n != 4 {
		t.Errorf("after mixed: got %d total, want 4", n)
	}

	// Different PR is independent.
	if b.HasGitHubComments("acme/web", 99) {
		t.Error("different PR: should have no comments")
	}

	// Persists across reload.
	b2, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if !b2.HasGitHubComments("acme/web", 42) {
		t.Error("after reload: should still have comments")
	}
	if n := b2.GitHubCommentCountForPR("acme/web", 42); n != 4 {
		t.Errorf("after reload: got %d, want 4", n)
	}
	all2 := b2.GitHubCommentsForPR("acme/web", 42)
	if len(all2) != 4 {
		t.Errorf("after reload: got %d comments, want 4", len(all2))
	}
}

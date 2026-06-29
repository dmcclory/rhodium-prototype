package glog

import (
	"regexp"
	"strings"
	"testing"

	"rhodium/internal/gh"
	coreglog "rhodium/internal/glog"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// allExpanded builds the fully-expanded node list + expansion maps for a set
// of commits, mirroring SetCommits' default.
func allExpanded(commits []coreglog.CommitRollup) ([]node, map[int]bool, map[fileKey]bool) {
	ec := map[int]bool{}
	ef := map[fileKey]bool{}
	for ci, c := range commits {
		ec[ci] = true
		for fi := range c.Files {
			ef[fileKey{ci, fi}] = true
		}
	}
	return visibleNodes(commits, ec, ef), ec, ef
}

func TestRenderTreeBadgesAndSummary(t *testing.T) {
	pr := &gh.PR{Repo: "octo/web", Number: 42, Title: "Refactor auth"}
	commits := []coreglog.CommitRollup{
		{Commit: gh.Commit{SHA: "a1b9f2cdeadbeef", Title: "extract parser", Author: "tj"}, Marked: 2, Total: 2, Status: coreglog.StatusAll},
		{Commit: gh.Commit{SHA: "9ad3e05beefcafe", Title: "wire it up", Author: "dan"}, Marked: 1, Total: 2, Status: coreglog.StatusPartial},
		{Commit: gh.Commit{SHA: "f02bb71facefeed", Title: "fix race", Author: "dan"}, Marked: 0, Total: 1, Status: coreglog.StatusNone},
	}

	nodes, ec, ef := allExpanded(commits)
	out := stripANSI(renderTree(pr, commits, nodes, 0, ec, ef))

	if !strings.Contains(out, "octo/web#42") || !strings.Contains(out, "3 commits") {
		t.Errorf("missing header:\n%s", out)
	}
	for _, want := range []string{"a1b9f2c", "extract parser", "9ad3e05", "wire it up", "f02bb71", "fix race"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, want := range []string{"[✓]", "✔ reviewed", "[~]", "◐ 1/2 hunks", "[ ]", "○ unreviewed"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "reviewed 1/3 commits · 3/5 hunks") {
		t.Errorf("missing/incorrect summary:\n%s", out)
	}
}

func TestTreeExpandCollapse(t *testing.T) {
	commits := []coreglog.CommitRollup{
		{
			Commit: gh.Commit{SHA: "a1b9f2cdeadbeef", Title: "extract parser"},
			Files: []coreglog.FileRollup{
				{
					Path: "auth/middleware.go", Additions: 9, Deletions: 22, Marked: 1, Total: 2,
					Hunks: []coreglog.HunkStatus{
						{Header: "@@ -1,3 +1,4 @@ func wireRotation() {", Hash: "h1", Marked: true},
						{Header: "@@ -10,2 +11,3 @@ func handleExpiry() {", Hash: "h2", Marked: false},
					},
				},
			},
			Marked: 1, Total: 2, Status: coreglog.StatusPartial,
		},
	}

	// Fully expanded shows file + hunks.
	nodes, ec, ef := allExpanded(commits)
	out := stripANSI(renderTree(nil, commits, nodes, 0, ec, ef))
	for _, want := range []string{"auth/middleware.go", "+9 −22", "wireRotation", "handleExpiry", "◐ 1/2"} {
		if !strings.Contains(out, want) {
			t.Errorf("expanded view missing %q in:\n%s", want, out)
		}
	}

	// Collapse the commit → files/hunks hidden.
	ec[0] = false
	nodes = visibleNodes(commits, ec, ef)
	if len(nodes) != 1 {
		t.Fatalf("collapsed commit should leave 1 node, got %d", len(nodes))
	}
	out = stripANSI(renderTree(nil, commits, nodes, 0, ec, ef))
	if strings.Contains(out, "wireRotation") || strings.Contains(out, "auth/middleware.go") {
		t.Errorf("collapsed commit should hide files/hunks:\n%s", out)
	}

	// Expand commit but collapse the file → file shown, hunks hidden.
	ec[0] = true
	ef[fileKey{0, 0}] = false
	nodes = visibleNodes(commits, ec, ef)
	out = stripANSI(renderTree(nil, commits, nodes, 0, ec, ef))
	if !strings.Contains(out, "auth/middleware.go") {
		t.Errorf("expanded commit should show the file:\n%s", out)
	}
	if strings.Contains(out, "wireRotation") {
		t.Errorf("collapsed file should hide hunks:\n%s", out)
	}
}

func TestEnterOnHunkEmitsOpenHunkMsg(t *testing.T) {
	commits := []coreglog.CommitRollup{
		{
			Commit: gh.Commit{SHA: "a1b9f2cdeadbeef"},
			Files: []coreglog.FileRollup{
				{Path: "auth/cache.go", Hunks: []coreglog.HunkStatus{{Header: "@@ x", Hash: "deadbeef", Marked: false}}, Total: 1},
			},
			Total: 1, Status: coreglog.StatusNone,
		},
	}
	m := New()
	m.SetCommits(nil, commits)
	// nodes: [commit, file, hunk] — move cursor to the hunk.
	m.cursor = 2
	if m.nodes[m.cursor].kind != kindHunk {
		t.Fatalf("expected cursor on a hunk, got kind %d", m.nodes[m.cursor].kind)
	}

	cmd := m.onEnter()
	if cmd == nil {
		t.Fatal("expected a command from enter-on-hunk")
	}
	msg, ok := cmd().(OpenHunkMsg)
	if !ok {
		t.Fatalf("expected OpenHunkMsg, got %T", cmd())
	}
	if msg.Path != "auth/cache.go" || msg.HunkHash != "deadbeef" || msg.CommitSHA != "a1b9f2cdeadbeef" {
		t.Errorf("unexpected OpenHunkMsg: %+v", msg)
	}
}

func TestSetCommitsDefaultsToExpanded(t *testing.T) {
	m := New()
	commits := []coreglog.CommitRollup{
		{Commit: gh.Commit{SHA: "c1"}, Files: []coreglog.FileRollup{{Path: "a.go", Hunks: []coreglog.HunkStatus{{Hash: "x"}}, Total: 1}}},
		{Commit: gh.Commit{SHA: "c2"}},
	}
	m.SetCommits(nil, commits)
	// Commit c1 expanded + its file expanded → 3 nodes (commit, file, hunk);
	// c2 has no files → 1 node. Total 4.
	if len(m.nodes) != 4 {
		t.Errorf("expected 4 visible nodes when fully expanded, got %d", len(m.nodes))
	}
	if !m.expandedCommit[0] || !m.expandedFile[fileKey{0, 0}] {
		t.Error("commit and file should default to expanded")
	}
}

func TestRefreshRollupsPreservesCursorAndExpansion(t *testing.T) {
	commits := []coreglog.CommitRollup{
		{Commit: gh.Commit{SHA: "c1"}, Files: []coreglog.FileRollup{{Path: "a.go", Hunks: []coreglog.HunkStatus{{Hash: "x"}}, Total: 1}}, Total: 1, Status: coreglog.StatusNone},
		{Commit: gh.Commit{SHA: "c2"}, Files: []coreglog.FileRollup{{Path: "b.go", Hunks: []coreglog.HunkStatus{{Hash: "y"}}, Total: 1}}, Total: 1, Status: coreglog.StatusNone},
	}
	m := New()
	m.SetCommits(nil, commits)
	// Collapse commit 0's file, then put the cursor on the second commit.
	m.expandedFile[fileKey{0, 0}] = false
	m.rebuild()
	m.cursor = 2
	wantNode := m.nodes[m.cursor]

	// Simulate backing out and returning: same structure, a commit now marked.
	updated := []coreglog.CommitRollup{
		{Commit: gh.Commit{SHA: "c1"}, Files: commits[0].Files, Marked: 1, Total: 1, Status: coreglog.StatusAll},
		commits[1],
	}
	m.RefreshRollups(updated)

	if m.cursor != 2 {
		t.Errorf("cursor moved: got %d, want 2", m.cursor)
	}
	if m.nodes[m.cursor] != wantNode {
		t.Errorf("focused node changed: got %+v, want %+v", m.nodes[m.cursor], wantNode)
	}
	if m.expandedFile[fileKey{0, 0}] {
		t.Error("collapsed file should stay collapsed after refresh")
	}
}

func TestStatusTailEmptyForNoHunks(t *testing.T) {
	c := coreglog.CommitRollup{Commit: gh.Commit{SHA: "merge12"}, Total: 0, Status: coreglog.StatusNone}
	if tail := statusTail(c); tail != "" {
		t.Errorf("expected empty tail for a no-hunk commit, got %q", tail)
	}
}

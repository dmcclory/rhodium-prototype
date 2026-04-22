package main

import (
	"testing"
)

// overlayCommitStatus is the only non-network piece of rhodium log, so
// these tests exercise it against hand-crafted commits + marks rather
// than mocking gh. Each case is one of the shapes a reviewer will hit
// in practice.

// makeMarksFromPatch parses `patch` the way the brain does at mark time
// and returns a "path → {hash → true}" map, optionally filtering to a
// subset of hunk indices. Lets tests express "reviewer marked the first
// hunk but not the second" without copy-pasting hash strings.
func makeMarksFromPatch(path, patch string, markedIdx ...int) map[string]map[string]bool {
	hunks := parseHunks(patch)
	want := map[int]bool{}
	if len(markedIdx) == 0 {
		for i := range hunks {
			want[i] = true
		}
	} else {
		for _, i := range markedIdx {
			want[i] = true
		}
	}
	m := map[string]bool{}
	for i, h := range hunks {
		if want[i] {
			m[h.Hash] = true
		}
	}
	return map[string]map[string]bool{path: m}
}

func TestOverlayCommitStatusFullyReviewed(t *testing.T) {
	patch := `@@ -1,1 +1,2 @@
 context
+added line
`
	files := []FileChange{{Path: "a.go", Patch: patch}}
	marks := makeMarksFromPatch("a.go", patch)

	got := overlayCommitStatus(Commit{SHA: "abc"}, files, marks)
	if got.Total != 1 || got.Marked != 1 {
		t.Errorf("fully-reviewed: got %d/%d, want 1/1", got.Marked, got.Total)
	}
	if commitStatusGlyph(got) != "✓" {
		t.Errorf("glyph: got %q, want ✓", commitStatusGlyph(got))
	}
}

func TestOverlayCommitStatusPartiallyReviewed(t *testing.T) {
	// Two hunks, reviewer marked only the first.
	patch := `@@ -1,1 +1,2 @@
 alpha
+beta
@@ -10,1 +11,2 @@
 gamma
+delta
`
	files := []FileChange{{Path: "a.go", Patch: patch}}
	marks := makeMarksFromPatch("a.go", patch, 0)

	got := overlayCommitStatus(Commit{SHA: "abc"}, files, marks)
	if got.Marked != 1 || got.Total != 2 {
		t.Errorf("partial: got %d/%d, want 1/2", got.Marked, got.Total)
	}
	if commitStatusGlyph(got) != "◐" {
		t.Errorf("glyph: got %q, want ◐", commitStatusGlyph(got))
	}
}

func TestOverlayCommitStatusUnreviewed(t *testing.T) {
	patch := `@@ -1,1 +1,2 @@
 context
+added
`
	files := []FileChange{{Path: "a.go", Patch: patch}}
	marks := map[string]map[string]bool{} // no marks at all

	got := overlayCommitStatus(Commit{SHA: "abc"}, files, marks)
	if got.Marked != 0 || got.Total != 1 {
		t.Errorf("unreviewed: got %d/%d, want 0/1", got.Marked, got.Total)
	}
	if commitStatusGlyph(got) != " " {
		t.Errorf("glyph: got %q, want blank", commitStatusGlyph(got))
	}
}

func TestOverlayCommitStatusMergeCommitNoPatch(t *testing.T) {
	// Merge commits commonly arrive with an empty patch — no reviewable
	// hunks. Should report 0/0 and render as blank rather than "reviewed".
	got := overlayCommitStatus(Commit{SHA: "abc"}, []FileChange{{Path: "a.go", Patch: ""}}, nil)
	if got.Total != 0 || len(got.Files) != 0 {
		t.Errorf("merge commit: got total=%d files=%d, want 0/0", got.Total, len(got.Files))
	}
	if commitStatusGlyph(got) != " " {
		t.Errorf("glyph on empty: got %q, want blank", commitStatusGlyph(got))
	}
}

func TestOverlayCommitStatusRewrittenHunkCaveat(t *testing.T) {
	// This is the documented approximation case, pinned as a regression
	// guard: commit A introduced a hunk that a later commit then rewrote.
	// The final-PR diff — which is what `marks` is hashed against — has
	// the rewritten form. A's original patch hashes differently, so A
	// shows as unreviewed even though the net effect was reviewed.
	originalPatch := `@@ -1,1 +1,2 @@
 ctx
+alpha
`
	// Reviewer's marks are against the final-PR hunk, which (say) has
	// been rewritten to `+alpha-revised`. A's `+alpha` has a different
	// hash — no intersection.
	finalPatch := `@@ -1,1 +1,2 @@
 ctx
+alpha-revised
`
	marks := makeMarksFromPatch("a.go", finalPatch) // marks the rewritten hunk

	got := overlayCommitStatus(
		Commit{SHA: "abc"},
		[]FileChange{{Path: "a.go", Patch: originalPatch}},
		marks,
	)
	if got.Marked != 0 || got.Total != 1 {
		t.Errorf("rewrite caveat: got %d/%d, want 0/1 — if this flipped, "+
			"the hash primitive changed and the documented caveat may be obsolete",
			got.Marked, got.Total)
	}
}

func TestOverlayCommitStatusAggregatesAcrossFiles(t *testing.T) {
	patchA := `@@ -1,1 +1,2 @@
 ctx
+a1
`
	patchB := `@@ -1,1 +1,2 @@
 ctx
+b1
@@ -10,1 +11,2 @@
 ctx
+b2
`
	files := []FileChange{
		{Path: "a.go", Patch: patchA},
		{Path: "b.go", Patch: patchB},
	}
	// Mark all of a.go, only second hunk of b.go.
	marks := makeMarksFromPatch("a.go", patchA)
	for h, v := range makeMarksFromPatch("b.go", patchB, 1)["b.go"] {
		if marks["b.go"] == nil {
			marks["b.go"] = map[string]bool{}
		}
		marks["b.go"][h] = v
	}

	got := overlayCommitStatus(Commit{SHA: "abc"}, files, marks)
	if got.Marked != 2 || got.Total != 3 {
		t.Errorf("aggregate: got %d/%d, want 2/3", got.Marked, got.Total)
	}
	if len(got.Files) != 2 {
		t.Fatalf("files len: got %d, want 2", len(got.Files))
	}
	// Files preserve input order.
	if got.Files[0].Path != "a.go" || got.Files[0].Marked != 1 || got.Files[0].Total != 1 {
		t.Errorf("files[0]: %+v", got.Files[0])
	}
	if got.Files[1].Path != "b.go" || got.Files[1].Marked != 1 || got.Files[1].Total != 2 {
		t.Errorf("files[1]: %+v", got.Files[1])
	}
}

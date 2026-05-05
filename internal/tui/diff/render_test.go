package diff

import (
	"strings"
	"testing"

	"rhodium/internal/brain"

	corediff "rhodium/internal/diff"
)

// --- renderSegment tests ---

func TestRenderSegmentSimpleB1B2F1(t *testing.T) {
	// ClassB1B2F1: b1=b2=f1; only f2 is new → show b2→f2
	seg := corediff.Segment{
		Class: corediff.ClassB1B2F1,
		B1:    "line1\nline2\nline3",
		F1:    "line1\nline2\nline3",
		B2:    "line1\nline2\nline3",
		F2:    "line1\nline2\nline3\nnew line",
	}
	views := seg.Class.Views()
	if len(views) == 0 {
		t.Fatal("expected at least one view")
	}

	body, hunkLines, lineMap := renderSegment(seg, views[0], 0, 0, nil, 0, nil, nil, nil, 0, false)

	if !strings.Contains(body, "line1") {
		t.Error("expected context line 'line1' in output")
	}
	if !strings.Contains(body, "+new line") {
		t.Errorf("expected '+new line' in output:\n%s", body)
	}
	// Header should be the first line of hunkLines
	if len(hunkLines) == 0 {
		t.Fatal("expected at least one hunk line offset")
	}
	// lineMap should have entries
	if len(lineMap) == 0 {
		t.Fatal("expected non-empty lineMap")
	}
}

func TestRenderSegmentPureDeletion(t *testing.T) {
	// b2 has content, f2 removed it
	seg := corediff.Segment{
		Class: corediff.ClassB1F1,
		B1:    "keep\ndelete1\ndelete2\nkeep2",
		F1:    "keep\ndelete1\ndelete2\nkeep2",
		B2:    "keep\ndelete1\ndelete2\nkeep2",
		F2:    "keep\nkeep2",
	}
	views := seg.Class.Views()
	if len(views) == 0 {
		t.Fatal("expected at least one view")
	}

	body, _, _ := renderSegment(seg, views[0], 0, 0, nil, 0, nil, nil, nil, 0, false)

	if !strings.Contains(body, "-delete1") {
		t.Errorf("expected '-delete1' in output:\n%s", body)
	}
	if !strings.Contains(body, "-delete2") {
		t.Errorf("expected '-delete2' in output:\n%s", body)
	}
}

func TestRenderSegmentConflict(t *testing.T) {
	// ClassConflict: all four different
	seg := corediff.Segment{
		Class: corediff.ClassConflict,
		B1:    "header\noriginal\nfooter",
		F1:    "header\nfeature-v1\nfooter",
		B2:    "header\nbase-changed\nfooter",
		F2:    "header\nfeature-v2\nfooter",
	}
	views := seg.Class.Views()
	if len(views) == 0 {
		t.Fatal("expected at least one view")
	}

	body, _, _ := renderSegment(seg, views[0], 0, 0, nil, 0, nil, nil, nil, 0, false)

	// views[0] for ClassConflict is B2→F2
	if views[0].From != corediff.B2 || views[0].To != corediff.F2 {
		t.Errorf("expected primary view B2→F2, got %s→%s", views[0].From, views[0].To)
	}
	if !strings.Contains(body, "-base-changed") {
		t.Errorf("expected '-base-changed' in B2→F2 diff:\n%s", body)
	}
	if !strings.Contains(body, "+feature-v2") {
		t.Errorf("expected '+feature-v2' in B2→F2 diff:\n%s", body)
	}
}

func TestRenderSegmentEmptyHunks(t *testing.T) {
	// Segment where from == to → no hunks
	seg := corediff.Segment{
		Class: corediff.ClassB1F1, // b1=f1; b2≠f2
		B1:    "same",
		F1:    "same",
		B2:    "same",
		F2:    "same",
	}
	views := seg.Class.Views()
	if len(views) == 0 {
		t.Fatal("expected at least one view")
	}

	body, hunkLines, lineMap := renderSegment(seg, views[0], 0, 0, nil, 0, nil, nil, nil, 0, false)

	// No diff hunks → no hunkLines
	if len(hunkLines) != 0 {
		t.Errorf("expected 0 hunkLines for identical from/to, got %d", len(hunkLines))
	}
	if len(lineMap) != 0 {
		t.Errorf("expected 0 lineMap entries for identical from/to, got %d", len(lineMap))
	}
	// Body should be empty (no header rendered when no hunks)
	if body != "" {
		t.Errorf("expected empty body, got:\n%s", body)
	}
}

func TestRenderSegmentMarkedHunk(t *testing.T) {
	seg := corediff.Segment{
		Class: corediff.ClassB1B2F1,
		B1:    "a\nb",
		F1:    "a\nb",
		B2:    "a\nb",
		F2:    "a\nb\nc",
	}
	views := seg.Class.Views()

	// Generate hunks to get the hash
	hunks := corediff.Diff2Hunks(seg.B2, seg.F2)
	if len(hunks) == 0 {
		t.Fatal("expected hunks")
	}
	// renderSegment expects prefixed keys (segIdx:hash)
	marks := map[string]bool{fmt.Sprintf("0:%s", hunks[0].Hash): true}

	body, _, _ := renderSegment(seg, views[0], 0, 0, marks, 0, nil, nil, nil, 0, false)

	if !strings.Contains(body, "[✓]") {
		t.Errorf("expected marked hunk '[✓]' in output:\n%s", body)
	}
}

func TestRenderSegmentFocusedHunk(t *testing.T) {
	seg := corediff.Segment{
		Class: corediff.ClassB1B2F1,
		B1:    "a\nb\nc",
		F1:    "a\nb\nc",
		B2:    "a\nb\nc",
		F2:    "a\nb\nc\nd",
	}
	views := seg.Class.Views()

	// Hunk 0 in this segment is focused
	body, _, _ := renderSegment(seg, views[0], 0, 0, nil, 0, nil, nil, nil, 0, false)

	// Focused hunks get reverse-video rendering (the style wraps the header)
	// We can't easily test the ANSI codes, but we can verify the hunk is rendered
	if !strings.Contains(body, "@@") {
		t.Errorf("expected hunk header in output:\n%s", body)
	}

	// Hunk 1 (doesn't exist — only one hunk) so no focus
	body2, _, _ := renderSegment(seg, views[0], 0, 0, nil, 1, nil, nil, nil, 0, false)
	// Both should contain the hunk, just different styling
	if !strings.Contains(body2, "@@") {
		t.Errorf("expected hunk header in output:\n%s", body2)
	}
}

// --- renderSegmented tests ---

func TestRenderSegmentedSingleSegment(t *testing.T) {
	segs := []corediff.Segment{
		{
			Class: corediff.ClassB1B2,
			B1:    "old",
			F1:    "old",
			B2:    "old",
			F2:    "old\nnew line",
		},
	}

	body, hunkLines, lineMap := renderSegmented(segs, 0, nil, 0, nil, nil, nil, 0, false)

	if !strings.Contains(body, "== segment 1/1") {
		t.Errorf("expected segment header in output:\n%s", body)
	}
	if !strings.Contains(body, "b1_b2") {
		t.Errorf("expected class label in output:\n%s", body)
	}
	if !strings.Contains(body, "+new line") {
		t.Errorf("expected diff content in output:\n%s", body)
	}
	if len(hunkLines) == 0 {
		t.Error("expected hunkLines for the segment header")
	}
	if len(lineMap) == 0 {
		t.Error("expected lineMap entries")
	}
}

func TestRenderSegmentedMultipleSegments(t *testing.T) {
	segs := []corediff.Segment{
		{
			Class: corediff.ClassB1B2F1,
			B1:    "top\nold1",
			F1:    "top\nold1",
			B2:    "top\nold1",
			F2:    "top\nnew1",
		},
		{
			Class: corediff.ClassB1B2F1,
			B1:    "mid\nold2",
			F1:    "mid\nold2",
			B2:    "mid\nold2",
			F2:    "mid\nnew2",
		},
	}

	body, _, _ := renderSegmented(segs, 0, nil, 0, nil, nil, nil, 0, false)

	// Should have two segment headers
	count := strings.Count(body, "== segment")
	if count != 2 {
		t.Errorf("expected 2 segment headers, found %d in:\n%s", count, body)
	}
	// Should contain both changes
	if !strings.Contains(body, "+new1") {
		t.Errorf("expected '+new1' in output:\n%s", body)
	}
	if !strings.Contains(body, "+new2") {
		t.Errorf("expected '+new2' in output:\n%s", body)
	}
}

func TestRenderSegmentedLineOffset(t *testing.T) {
	// Two segments: the second segment's line numbers should start
	// after the first segment's To content.
	segs := []corediff.Segment{
		{
			Class: corediff.ClassB1B2F1,
			B1:    "l1\nl2\nl3",
			F1:    "l1\nl2\nl3",
			B2:    "l1\nl2\nl3",
			F2:    "l1\nl2\nl3\nl4",
		},
		{
			Class: corediff.ClassB1B2F1,
			B1:    "m1\nm2",
			F1:    "m1\nm2",
			B2:    "m1\nm2",
			F2:    "m1\nm2", // no change in this segment
		},
	}

	// Get hunks for first segment to understand line numbers
	hunks1 := corediff.Diff2Hunks(segs[0].B2, segs[0].F2)
	if len(hunks1) == 0 {
		t.Fatal("expected hunks for segment 0")
	}
	// Segment 0: from "l1\nl2\nl3" to "l1\nl2\nl3\nl4"
	// The diff should start at +4,1 (new line 4)
	body, _, _ := renderSegmented(segs, 0, nil, 0, nil, nil, nil, 0, false)

	// Segment 1 has no changes, so no hunks. But we should still see the header.
	if !strings.Contains(body, "== segment 2/2") {
		t.Errorf("expected segment 2 header:\n%s", body)
	}
}

func TestRenderSegmentedViewCycling(t *testing.T) {
	// ClassConflict has 3 views. Cycling viewIdx should change the displayed diff.
	segs := []corediff.Segment{
		{
			Class: corediff.ClassConflict,
			B1:    "header\noriginal\nfooter",
			F1:    "header\nfeature-v1\nfooter",
			B2:    "header\nbase-changed\nfooter",
			F2:    "header\nfeature-v2\nfooter",
		},
	}

	body0, _, _ := renderSegmented(segs, 0, nil, 0, nil, nil, nil, 0, false)
	body1, _, _ := renderSegmented(segs, 1, nil, 0, nil, nil, nil, 0, false)

	// View 0: b2→f2 ("new diff")
	if !strings.Contains(body0, "b2→f2") {
		t.Errorf("expected b2→f2 in view 0 header:\n%s", body0)
	}
	// View 1: f1→f2 ("old tip to new tip")
	if !strings.Contains(body1, "f1→f2") {
		t.Errorf("expected f1→f2 in view 1 header:\n%s", body1)
	}

	// The diffs should be different (B2→F2 diff vs F1→F2 diff)
	if body0 == body1 {
		t.Error("expected different output for different views")
	}
}

func TestRenderSegmentedHiddenSegmentsSkipped(t *testing.T) {
	// Hidden class segments should be skipped.
	segs := []corediff.Segment{
		{
			Class: corediff.ClassB1B2F1F2, // Hidden: all equal
			B1:    "same",
			F1:    "same",
			B2:    "same",
			F2:    "same",
		},
		{
			Class: corediff.ClassB1B2F1,
			B1:    "old",
			F1:    "old",
			B2:    "old",
			F2:    "old\nnew",
		},
	}

	body, _, _ := renderSegmented(segs, 0, nil, 0, nil, nil, nil, 0, false)

	// Only segment 2 should appear (segment 1 is hidden)
	if strings.Contains(body, "segment 1") {
		t.Errorf("hidden segment should not appear:\n%s", body)
	}
	// The visible segment should be labeled 2/2 (counting the hidden one)
	// Actually, looking at SegmentHunks, it uses the original index.
	// Let me check: the segment index in the header uses segIdx+1 and len(segments).
	// Since hidden segments are skipped (continue), they don't get rendered.
	// But the index still uses the original segIdx.
	if !strings.Contains(body, "segment 2/2") {
		t.Errorf("expected 'segment 2/2' for the visible segment:\n%s", body)
	}
}

func TestRenderSegmentedEmptySegments(t *testing.T) {
	segs := []corediff.Segment{}

	body, hunkLines, lineMap := renderSegmented(segs, 0, nil, 0, nil, nil, nil, 0, false)

	if body != "" {
		t.Errorf("expected empty body for no segments, got:\n%s", body)
	}
	if len(hunkLines) != 0 {
		t.Errorf("expected 0 hunkLines, got %d", len(hunkLines))
	}
	if len(lineMap) != 0 {
		t.Errorf("expected 0 lineMap, got %d", len(lineMap))
	}
}

func TestRenderSegmentedWithNotes(t *testing.T) {
	segs := []corediff.Segment{
		{
			Class: corediff.ClassB1B2F1,
			B1:    "a\nb",
			F1:    "a\nb",
			B2:    "a\nb",
			F2:    "a\nb\nnew line",
		},
	}

	notes := []brain.Note{
		{LineNo: 3, Body: "test note"},
	}

	body, _, _ := renderSegmented(segs, 0, nil, 0, notes, nil, nil, 0, false)

	if !strings.Contains(body, "test note") {
		t.Errorf("expected note body in output:\n%s", body)
	}
	if !strings.Contains(body, "RH: ") {
		t.Errorf("expected note prefix 'RH: ' in output:\n%s", body)
	}
}

func TestRenderSegmentedFocusedSegmentHeader(t *testing.T) {
	segs := []corediff.Segment{
		{
			Class: corediff.ClassB1B2F1,
			B1:    "a",
			F1:    "a",
			B2:    "a",
			F2:    "a\nx",
		},
		{
			Class: corediff.ClassB1B2F1,
			B1:    "b",
			F1:    "b",
			B2:    "b",
			F2:    "b\ny",
		},
	}

	// Focused hunk is at index 0 (the first segment header)
	body0, _, _ := renderSegmented(segs, 0, nil, 0, nil, nil, nil, 0, false)
	// Focused hunk is at index 2 (second segment header: 0=header, 1=hunk, 2=header)
	body2, _, _ := renderSegmented(segs, 0, nil, 2, nil, nil, nil, 0, false)

	// Both should render both segments
	if !strings.Contains(body0, "== segment 1/2") {
		t.Errorf("expected segment 1 header:\n%s", body0)
	}
	if !strings.Contains(body2, "== segment 2/2") {
		t.Errorf("expected segment 2 header:\n%s", body2)
	}
}

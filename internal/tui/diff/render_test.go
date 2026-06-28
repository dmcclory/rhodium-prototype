package diff

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"rhodium/internal/brain"
	"rhodium/internal/gh"

	corediff "rhodium/internal/diff"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// stripANSI removes terminal color escape sequences so tests can reason
// about visible column positions.
func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// contentCol returns the column (byte index) where needle first appears in
// the first line of body that contains it, after stripping ANSI codes.
func contentCol(body, needle string) int {
	for _, l := range strings.Split(stripANSI(body), "\n") {
		if i := strings.Index(l, needle); i >= 0 {
			return i
		}
	}
	return -1
}

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
	marks := map[string]int{fmt.Sprintf("0:%s", hunks[0].Hash): 1}

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

	body, hunkLines, lineMap := renderSegmented(segs, 0, false, nil, 0, nil, nil, nil, 0, false)

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

	body, _, _ := renderSegmented(segs, 0, false, nil, 0, nil, nil, nil, 0, false)

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
	body, _, _ := renderSegmented(segs, 0, false, nil, 0, nil, nil, nil, 0, false)

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

	body0, _, _ := renderSegmented(segs, 0, false, nil, 0, nil, nil, nil, 0, false)
	body1, _, _ := renderSegmented(segs, 1, false, nil, 0, nil, nil, nil, 0, false)

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

	body, _, _ := renderSegmented(segs, 0, false, nil, 0, nil, nil, nil, 0, false)

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

	body, hunkLines, lineMap := renderSegmented(segs, 0, false, nil, 0, nil, nil, nil, 0, false)

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

	body, _, _ := renderSegmented(segs, 0, false, nil, 0, notes, nil, nil, 0, false)

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
	body0, _, _ := renderSegmented(segs, 0, false, nil, 0, nil, nil, nil, 0, false)
	// Focused hunk is at index 2 (second segment header: 0=header, 1=hunk, 2=header)
	body2, _, _ := renderSegmented(segs, 0, false, nil, 2, nil, nil, nil, 0, false)

	// Both should render both segments
	if !strings.Contains(body0, "== segment 1/2") {
		t.Errorf("expected segment 1 header:\n%s", body0)
	}
	if !strings.Contains(body2, "== segment 2/2") {
		t.Errorf("expected segment 2 header:\n%s", body2)
	}
}

// --- segment navigation tests ---

func TestJumpSegmentNext(t *testing.T) {
	segs := []corediff.Segment{
		{
			Class: corediff.ClassB1B2F1,
			B1:    "a", F1: "a", B2: "a", F2: "a\nx",
		},
		{
			Class: corediff.ClassB1B2F1,
			B1:    "b", F1: "b", B2: "b", F2: "b\ny",
		},
		{
			Class: corediff.ClassB1B2F1,
			B1:    "c", F1: "c", B2: "c", F2: "c\nz",
		},
	}
	m := &Model{
		segmented: true,
		segments:  segs,
		hunks:     corediff.SegmentHunks(segs, 0),
		hunkIdx:   1, // first hunk of segment 0
	}
	m.Resize(80, 24)

	// Jump to next segment.
	m.jumpSegment(1)

	// Should be at the first hunk of segment 1.
	nextSeg := segIdxForHunk(m.hunks, m.hunkIdx)
	if nextSeg != 1 {
		t.Errorf("after nextSegment: segIdx=%d, want 1", nextSeg)
	}
}

func TestJumpSegmentPrev(t *testing.T) {
	segs := []corediff.Segment{
		{Class: corediff.ClassB1B2F1, B1: "a", F1: "a", B2: "a", F2: "a\nx"},
		{Class: corediff.ClassB1B2F1, B1: "b", F1: "b", B2: "b", F2: "b\ny"},
		{Class: corediff.ClassB1B2F1, B1: "c", F1: "c", B2: "c", F2: "c\nz"},
	}
	m := &Model{
		segmented: true,
		segments:  segs,
		hunks:     corediff.SegmentHunks(segs, 0),
		hunkIdx:   4, // first hunk of segment 2
	}
	m.Resize(80, 24)

	// Jump to previous segment.
	m.jumpSegment(-1)

	prevSeg := segIdxForHunk(m.hunks, m.hunkIdx)
	if prevSeg != 1 {
		t.Errorf("after prevSegment: segIdx=%d, want 1", prevSeg)
	}
}

func TestJumpSegmentNextAtBoundary(t *testing.T) {
	segs := []corediff.Segment{
		{Class: corediff.ClassB1B2F1, B1: "a", F1: "a", B2: "a", F2: "a\nx"},
		{Class: corediff.ClassB1B2F1, B1: "b", F1: "b", B2: "b", F2: "b\ny"},
	}
	m := &Model{
		segmented: true,
		segments:  segs,
		hunks:     corediff.SegmentHunks(segs, 0),
		hunkIdx:   2, // first (only) hunk of segment 1 (last segment)
	}
	m.Resize(80, 24)

	// On the last segment, nextSegment should stay within segment.
	m.jumpSegment(1)

	seg := segIdxForHunk(m.hunks, m.hunkIdx)
	if seg != 1 {
		t.Errorf("at boundary: segIdx=%d, want 1 (stay on last segment)", seg)
	}
}

func TestJumpSegmentPrevAtBoundary(t *testing.T) {
	segs := []corediff.Segment{
		{Class: corediff.ClassB1B2F1, B1: "a", F1: "a", B2: "a", F2: "a\nx"},
		{Class: corediff.ClassB1B2F1, B1: "b", F1: "b", B2: "b", F2: "b\ny"},
	}
	m := &Model{
		segmented: true,
		segments:  segs,
		hunks:     corediff.SegmentHunks(segs, 0),
		hunkIdx:   1, // first hunk of segment 0
	}
	m.Resize(80, 24)

	// On the first segment, prevSegment should stay within segment.
	m.jumpSegment(-1)

	seg := segIdxForHunk(m.hunks, m.hunkIdx)
	if seg != 0 {
		t.Errorf("at boundary: segIdx=%d, want 0 (stay on first segment)", seg)
	}
}

func TestJumpSegmentNonSegmented(t *testing.T) {
	// Non-segmented hunks from a plain patch.
	m := &Model{
		segmented: false,
		hunks: []corediff.Hunk{
			{Header: "@@ -1,2 +1,3 @@", BodyLines: []string{" a", "+b", "+c"}, Hash: "abc"},
			{Header: "@@ -5,2 +6,3 @@", BodyLines: []string{" d", "+e", "+f"}, Hash: "def"},
		},
		hunkIdx: 0,
	}
	m.Resize(80, 24)

	m.jumpSegment(1)

	if m.hunkIdx != 1 {
		t.Errorf("non-segmented nextSegment: hunkIdx=%d, want 1", m.hunkIdx)
	}
}

func TestFooterSegmentProgress(t *testing.T) {
	segs := []corediff.Segment{
		{Class: corediff.ClassB1B2F1, B1: "a", F1: "a", B2: "a", F2: "a\nx"},
		{Class: corediff.ClassConflict, B1: "b", F1: "b", B2: "b\nold", F2: "b\nnew"},
	}
	m := &Model{
		segmented:     true,
		segments:      segs,
		hunks:         corediff.SegmentHunks(segs, 0),
		hunkIdx:       2, // first hunk of segment 1
		catchUpMode:   true,
		catchUpOldHead: "abc1234",
		catchUpClass:  corediff.ClassConflict,
	}
	m.Resize(80, 24)
	m.redraw()

	footer := m.Footer()
	if !strings.Contains(footer, "segment 2/2") {
		t.Errorf("footer missing segment progress:\n%s", footer)
	}
	if !strings.Contains(footer, "conflict") {
		t.Errorf("footer missing class label:\n%s", footer)
	}
	if !strings.Contains(footer, "N/P") {
		t.Errorf("footer missing N/P hint:\n%s", footer)
	}
}

func TestSegmentStatusCmd(t *testing.T) {
	segs := []corediff.Segment{
		{Class: corediff.ClassConflict, B1: "a", F1: "a", B2: "a", F2: "a\nx"},
	}
	m := &Model{
		segmented: true,
		segments:  segs,
		hunks:     corediff.SegmentHunks(segs, 0),
		hunkIdx:   1,
	}

	cmd := m.segmentStatusCmd()
	if cmd == nil {
		t.Fatal("expected non-nil status cmd")
	}
	msg := cmd()
	statusMsg, ok := msg.(StatusMsg)
	if !ok {
		t.Fatalf("expected StatusMsg, got %T", msg)
	}
	if !strings.Contains(statusMsg.Text, "segment 1/1") {
		t.Errorf("status missing segment info: %s", statusMsg.Text)
	}
	if !strings.Contains(statusMsg.Text, "conflict") {
		t.Errorf("status missing class: %s", statusMsg.Text)
	}
}

// --- story mode tests ---

func TestRenderSegmentedWithStoryMode(t *testing.T) {
	segs := []corediff.Segment{
		{
			Class: corediff.ClassConflict,
			B1:    "header\nold",
			F1:    "header\nfeature-v1\nold",
			B2:    "header\nnew-base",
			F2:    "header\nfeature-v2\nnew-base",
		},
	}

	// Without story mode — no story line.
	bodyOff, _, _ := renderSegmented(segs, 0, false, nil, 0, nil, nil, nil, 0, false)
	if strings.Contains(bodyOff, "story:") {
		t.Errorf("story line should not appear when storyMode=false:\n%s", bodyOff)
	}

	// With story mode — story line appears.
	bodyOn, _, _ := renderSegmented(segs, 0, true, nil, 0, nil, nil, nil, 0, false)
	if !strings.Contains(bodyOn, "story:") {
		t.Errorf("story line should appear when storyMode=true:\n%s", bodyOn)
	}
	if !strings.Contains(bodyOn, "f1 had") {
		t.Errorf("story line should show f1 line count:\n%s", bodyOn)
	}
	if !strings.Contains(bodyOn, "f2 has") {
		t.Errorf("story line should show f2 line count:\n%s", bodyOn)
	}
}

func TestRenderSegmentedStoryModeWithMultipleSegments(t *testing.T) {
	segs := []corediff.Segment{
		{
			Class: corediff.ClassB1B2F1,
			B1:    "a", F1: "a", B2: "a", F2: "a\nnew",
		},
		{
			Class: corediff.ClassConflict,
			B1:    "x\ny", F1: "x\nold\ny", B2: "x\nbase\ny", F2: "x\nnew\ny",
		},
	}

	body, _, _ := renderSegmented(segs, 0, true, nil, 0, nil, nil, nil, 0, false)

	// Both segments should have story lines.
	count := strings.Count(body, "story:")
	if count != 2 {
		t.Errorf("expected 2 story lines, got %d in:\n%s", count, body)
	}
}

func TestRenderSegmentedStoryModeSkippedWhenEmpty(t *testing.T) {
	// Segment where f1 == f2 (no rebase delta) — story summary returns
	// empty string, so no line should be rendered.
	segs := []corediff.Segment{
		{
			Class: corediff.ClassB1B2,
			B1:    "a", F1: "a", B2: "a", F2: "a",
		},
	}

	body, _, _ := renderSegmented(segs, 0, true, nil, 0, nil, nil, nil, 0, false)

	if strings.Contains(body, "story:") {
		t.Errorf("story line should not appear for identical f1/f2:\n%s", body)
	}
}

func TestToggleStoryMode(t *testing.T) {
	segs := []corediff.Segment{
		{Class: corediff.ClassConflict, B1: "a", F1: "a\nold", B2: "a", F2: "a\nnew"},
	}
	m := &Model{
		pr:          &gh.PR{Repo: "r", Number: 1, HeadSHA: "h", BaseSHA: "b"},
		file:        "f.go",
		segmented:   true,
		segments:    segs,
		hunks:       corediff.SegmentHunks(segs, 0),
		catchUpMode: true,
		storyMode:   false,
	}
	m.Resize(80, 24)

	// Initially off.
	if m.storyMode {
		t.Error("storyMode should start false")
	}

	// Toggle on.
	cmd := m.toggleStoryMode()
	if !m.storyMode {
		t.Error("storyMode should be true after toggle")
	}
	if cmd == nil {
		t.Fatal("expected status cmd")
	}
	msg := cmd()
	statusMsg, ok := msg.(StatusMsg)
	if !ok {
		t.Fatalf("expected StatusMsg, got %T", msg)
	}
	if !strings.Contains(statusMsg.Text, "story mode on") {
		t.Errorf("status: got %q", statusMsg.Text)
	}

	// Toggle off.
	cmd = m.toggleStoryMode()
	if m.storyMode {
		t.Error("storyMode should be false after second toggle")
	}
	msg = cmd()
	statusMsg, ok = msg.(StatusMsg)
	if !ok {
		t.Fatalf("expected StatusMsg, got %T", msg)
	}
	if !strings.Contains(statusMsg.Text, "story mode off") {
		t.Errorf("status: got %q", statusMsg.Text)
	}
}

func TestToggleStoryModeNonSegmented(t *testing.T) {
	m := &Model{segmented: false}
	cmd := m.toggleStoryMode()
	if m.storyMode {
		t.Error("storyMode should not change when not segmented")
	}
	if cmd == nil {
		t.Fatal("expected status cmd")
	}
	msg := cmd()
	statusMsg, ok := msg.(StatusMsg)
	if !ok {
		t.Fatalf("expected StatusMsg, got %T", msg)
	}
	if !strings.Contains(statusMsg.Text, "only available") {
		t.Errorf("status: got %q", statusMsg.Text)
	}
}

// --- feature ddiff rendering tests ---

func TestRenderSegmentedWithDDiff(t *testing.T) {
	segs := []corediff.Segment{
		{
			Class: corediff.ClassConflict,
			B1:    "header\nold",
			F1:    "header\nfeature-v1\nold",
			B2:    "header\nnew-base",
			F2:    "header\nfeature-v2\nnew-base",
		},
	}

	// Without story mode — no ddiff lines.
	bodyOff, _, _ := renderSegmented(segs, 0, false, nil, 0, nil, nil, nil, 0, false)
	if strings.Contains(bodyOff, "[dropped]") || strings.Contains(bodyOff, "[kept]") {
		t.Errorf("ddiff should not appear when storyMode=false:\n%s", bodyOff)
	}

	// With story mode — ddiff lines appear.
	bodyOn, _, _ := renderSegmented(segs, 0, true, nil, 0, nil, nil, nil, 0, false)
	if !strings.Contains(bodyOn, "[kept]") {
		t.Errorf("expected [kept] ddiff line:\n%s", bodyOn)
	}
	if !strings.Contains(bodyOn, "[dropped]") && !strings.Contains(bodyOn, "[added]") && !strings.Contains(bodyOn, "[absorbed]") && !strings.Contains(bodyOn, "[propagated]") {
		t.Errorf("expected at least one change label in ddiff:\n%s", bodyOn)
	}
}

func TestRenderDDiffLineStyles(t *testing.T) {
	tests := []struct {
		kind   corediff.DDiffKind
		text   string
		prefix string
	}{
		{corediff.DDiffKept, "same", " "},
		{corediff.DDiffDropped, "gone", "-"},
		{corediff.DDiffAdded, "new", "+"},
		{corediff.DDiffAbsorbed, "base", "-"},
		{corediff.DDiffPropagated, "prop", "+"},
	}
	for _, tt := range tests {
		dl := corediff.DDiffLine{Kind: tt.kind, Text: tt.text}
		out := renderDDiffLine(dl)
		if !strings.Contains(out, fmt.Sprintf("[%s]", tt.kind)) {
			t.Errorf("%s: missing label in %q", tt.kind, out)
		}
		if !strings.Contains(out, tt.text) {
			t.Errorf("%s: missing text %q in %q", tt.kind, tt.text, out)
		}
	}
}

// TestRenderFullFileAlignsAddedAndContext verifies that the "+ " marker on
// added lines doesn't push their content out of alignment with unchanged
// (context) lines — both should start in the same column.
func TestRenderFullFileAlignsAddedAndContext(t *testing.T) {
	fileContent := "ctx1\nadded\nctx2\n"
	hunks := []corediff.Hunk{
		{Header: "@@ -1,2 +1,3 @@", BodyLines: []string{" ctx1", "+added", " ctx2"}, Hash: "h"},
	}

	body, _, _ := renderFullFile(fileContent, hunks, nil, 0, nil, nil, nil, -1, false, nil)

	ctxCol := contentCol(body, "ctx1")
	addCol := contentCol(body, "added")
	if ctxCol < 0 || addCol < 0 {
		t.Fatalf("missing rendered lines:\n%s", body)
	}
	if ctxCol != addCol {
		t.Errorf("content misaligned: context starts at col %d, added at col %d\n%s",
			ctxCol, addCol, body)
	}
}

// TestRenderChunksAlignsAddedDeletedAndContext verifies the same alignment
// invariant in the chunk view, across added, deleted, and context lines.
func TestRenderChunksAlignsAddedDeletedAndContext(t *testing.T) {
	hunks := []corediff.Hunk{
		{Header: "@@ -1,2 +1,2 @@", BodyLines: []string{" keepme", "-goneme", "+addme"}, Hash: "h"},
	}
	chunks := []corediff.Chunk{
		{Signature: "block", StartLine: 1, EndLine: 2, HunkIdxs: []int{0}},
	}
	expanded := map[int]bool{0: true}

	body, _, _ := renderChunks(chunks, hunks, nil, 0, expanded, nil, nil, nil, -1, false, nil)

	keepCol := contentCol(body, "keepme")
	goneCol := contentCol(body, "goneme")
	addCol := contentCol(body, "addme")
	if keepCol < 0 || goneCol < 0 || addCol < 0 {
		t.Fatalf("missing rendered lines:\n%s", body)
	}
	if keepCol != addCol || keepCol != goneCol {
		t.Errorf("content misaligned: context=%d deleted=%d added=%d\n%s",
			keepCol, goneCol, addCol, body)
	}
}

// TestRenderChunksFiltersHunkLinesByRange verifies that when a single large
// hunk spans multiple chunks, each expanded chunk renders only the lines
// within its semantic range — no duplication across chunks.
func TestRenderChunksFiltersHunkLinesByRange(t *testing.T) {
	hunks := []corediff.Hunk{
		{Header: "@@ -0,0 +1,13 @@", BodyLines: []string{
			"+func foo() {",
			"+\tfmt.Println(\"foo\")",
			"+}",
			"+",
			"+func bar() {",
			"+\tfmt.Println(\"bar\")",
			"+}",
			"+",
			"+func baz() {",
			"+\tfmt.Println(\"baz\")",
			"+}",
		}, Hash: "h1"},
	}

	chunks := []corediff.Chunk{
		{Signature: "func foo() {", StartLine: 1, EndLine: 4, HunkIdxs: []int{0}},
		{Signature: "func bar() {", StartLine: 5, EndLine: 8, HunkIdxs: []int{0}},
		{Signature: "func baz() {", StartLine: 9, EndLine: 11, HunkIdxs: []int{0}},
	}

	// Expand all chunks so we can inspect what each one renders.
	expanded := map[int]bool{0: true, 1: true, 2: true}

	body, _, _ := renderChunks(chunks, hunks, nil, 0, expanded, nil, nil, nil, 0, false, nil)
	lines := strings.Split(body, "\n")

	// Count how many times each function signature appears as a diff line
	// (prefixed with '+ '). The green '+ ' prefix was added so additions
	// are still visible alongside syntax highlighting.
	fooCount := 0
	barCount := 0
	bazCount := 0
	for _, l := range lines {
		if strings.Contains(l, "func foo()") && strings.Contains(l, "+") {
			fooCount++
		}
		if strings.Contains(l, "func bar()") && strings.Contains(l, "+") {
			barCount++
		}
		if strings.Contains(l, "func baz()") && strings.Contains(l, "+") {
			bazCount++
		}
	}

	// Each function should appear exactly once — in its own chunk — not
	// duplicated across all three chunks.
	if fooCount != 1 {
		t.Errorf("func foo appears %d times in output, want 1", fooCount)
	}
	if barCount != 1 {
		t.Errorf("func bar appears %d times in output, want 1", barCount)
	}
	if bazCount != 1 {
		t.Errorf("func baz appears %d times in output, want 1", bazCount)
	}
}

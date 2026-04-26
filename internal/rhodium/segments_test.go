package rhodium

import (
	"testing"
)

func TestComputeSegmentsIdentical(t *testing.T) {
	lines := []string{"one", "two", "three"}
	segs := computeSegments(lines, lines, lines, lines)
	if len(segs) != 0 {
		t.Errorf("identical corners: got %d segments, want 0 (everything is anchors)", len(segs))
	}
}

func TestComputeSegmentsPureInsertion(t *testing.T) {
	// Classic new-commits-no-rebase case: b1==b2, f1==f2 prefix, f2 has a
	// new line appended. Expect segmentation to find the anchor region and
	// isolate the new line.
	b1 := []string{"one", "two"}
	b2 := []string{"one", "two"}
	f1 := []string{"one", "two"}
	f2 := []string{"one", "two", "NEW"}

	segs := computeSegments(b1, f1, b2, f2)
	// One segment: trailing region containing just "NEW".
	// b1[2:]="", f1[2:]="", b2[2:]="", f2[2:3]="NEW"
	// Class: b1=b2=f1="" (equal), f2 differs → ClassB1B2F1 ("new diff")
	if len(segs) != 1 {
		t.Fatalf("got %d segments, want 1; segs=%+v", len(segs), segs)
	}
	if segs[0].Class != ClassB1B2F1 {
		t.Errorf("class = %s, want ClassB1B2F1 (new diff)", segs[0].Class)
	}
	if segs[0].F2 != "NEW" {
		t.Errorf("F2 = %q, want %q", segs[0].F2, "NEW")
	}
}

// TestComputeSegmentsCleanRebase is the motivating case from diff4-plan.md:
// base drift + feature change applied to both arms, content identical after
// the rebase.
//
// Layout:
//   b1: "x"
//   b2: "x, y"               (base added y)
//   f1: "x, z"               (feature added z on old arm)
//   f2: "x, y, z"            (rebased: y from new base + z from feature)
//
// Super-anchor: "x" (only line present in all 4 corners).
// Two inter-anchor regions:
//   Leading: empty on all corners (skipped).
//   Trailing: b1="", f1="z", b2="y", f2="y\nz"
//     → Classify: all four segments distinct → ClassConflict at segment level.
//
// This test documents current behavior — NOT a claim that ClassConflict is
// the "right" classification here. Iron's full algorithm further resolves
// this via ddiff views (M5) and hunk merging (M4). For M2 we faithfully
// report what segmentation produces; higher layers decide what to do with
// it.
func TestComputeSegmentsCleanRebase(t *testing.T) {
	b1 := []string{"x"}
	b2 := []string{"x", "y"}
	f1 := []string{"x", "z"}
	f2 := []string{"x", "y", "z"}

	segs := computeSegments(b1, f1, b2, f2)
	if len(segs) != 1 {
		t.Fatalf("got %d segments, want 1; segs=%+v", len(segs), segs)
	}
	// The segment reported for the trailing region.
	if segs[0].F1 != "z" {
		t.Errorf("F1 = %q, want %q", segs[0].F1, "z")
	}
	if segs[0].B2 != "y" {
		t.Errorf("B2 = %q, want %q", segs[0].B2, "y")
	}
	// Per the Classify rules on a diamond with four distinct non-empty
	// strings and no pair equal, this lands at ClassConflict. Documenting
	// the status quo — see the comment above for the interpretation.
	if segs[0].Class != ClassConflict {
		t.Errorf("class = %s, want ClassConflict (current M2 behavior)", segs[0].Class)
	}
}

func TestComputeSegmentsEmptyCorners(t *testing.T) {
	// Deleted file case — f2 becomes empty. No super-anchors possible.
	b1 := []string{"a", "b"}
	f1 := []string{"a", "b"}
	b2 := []string{"a", "b"}
	var f2 []string

	segs := computeSegments(b1, f1, b2, f2)
	if len(segs) != 1 {
		t.Fatalf("got %d segments, want 1", len(segs))
	}
	// Whole file is the segment. b1=f1=b2="a\nb", f2="" → ClassB1F1F2... no
	// wait, Classify checks b1=b2 first. b1==b2==f1 (all "a\nb"), f2="" — so
	// the three-equal case with f2 as the outlier: ClassB1B2F1.
	if segs[0].Class != ClassB1B2F1 {
		t.Errorf("class = %s, want ClassB1B2F1", segs[0].Class)
	}
}

func TestComputeSlowFastPathPassesThrough(t *testing.T) {
	// Hidden class: ComputeSlow returns empty, matching Compute's behavior.
	d := Diamond{B1: "x", F1: "x", B2: "x", F2: "x"}
	if r := ComputeSlow(d); len(r.Segments) != 0 {
		t.Errorf("hidden: got %d segments, want 0", len(r.Segments))
	}

	// Shown-as-diff2 class: ComputeSlow falls back to single whole-file
	// segment — no segmentation needed.
	d2 := Diamond{B1: "old", F1: "old", B2: "old", F2: "new"} // ClassB1B2F1
	r := ComputeSlow(d2)
	if len(r.Segments) != 1 {
		t.Fatalf("shown-as-diff2: got %d segments, want 1", len(r.Segments))
	}
	if r.Segments[0].Class != ClassB1B2F1 {
		t.Errorf("class = %s, want ClassB1B2F1", r.Segments[0].Class)
	}
}

// TestComputeSlowComplex drives ComputeSlow through the complex-class path
// using a diamond whose corners do not share any pairwise equality but where
// the file contents have common anchor lines for segmentation to find.
func TestComputeSlowComplex(t *testing.T) {
	d := Diamond{
		B1: "header\nbody1\nfooter",
		F1: "header\nbody2\nfooter",
		B2: "header\nbody3\nfooter",
		F2: "header\nbody4\nfooter",
	}
	// Top-level: all four corners distinct → ClassConflict.
	// Segmentation: "header" anchors, "footer" anchors. Middle segment:
	// b1="body1", f1="body2", b2="body3", f2="body4". Classify → all four
	// distinct → ClassConflict.
	r := ComputeSlow(d)
	if len(r.Segments) != 1 {
		t.Fatalf("got %d segments, want 1 (only middle is non-empty)", len(r.Segments))
	}
	seg := r.Segments[0]
	if seg.B1 != "body1" || seg.F1 != "body2" || seg.B2 != "body3" || seg.F2 != "body4" {
		t.Errorf("segment corners wrong: %+v", seg)
	}
	if seg.Class != ClassConflict {
		t.Errorf("class = %s, want ClassConflict", seg.Class)
	}
}

func TestSplitLinesForSeg(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"one", []string{"one"}},
		{"one\ntwo", []string{"one", "two"}},
		{"one\ntwo\n", []string{"one", "two"}}, // trailing newline dropped
		{"one\n\nthree", []string{"one", "", "three"}},
	}
	for _, tt := range tests {
		got := splitLinesForSeg(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("splitLinesForSeg(%q) = %v, want %v", tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitLinesForSeg(%q)[%d] = %q, want %q", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}

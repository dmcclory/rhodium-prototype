package rhodium

import "strings"

// computeSegments runs the slow-path diff4 algorithm on four corner line
// slices and returns classified segments in file order.
//
// Algorithm (from Iron's patdiff4/lib/segments.ml):
//
//  1. Patience-diff each arm of the diamond:
//       pcs1 = patience(b1, f1)   — the old-arm matches
//       pcs2 = patience(b2, f2)   — the new-arm matches
//
//  2. Build content sequences from each PCS (pcs1[i]'s matched content is
//     b1[pcs1[i].AIdx], which by construction equals f1[pcs1[i].BIdx]).
//     Patience-diff those content sequences to find *super-anchors* — lines
//     whose content appears at matched indices in all four corners.
//
//  3. Walk super-anchors in order. Each inter-anchor gap becomes a segment;
//     classify its four-corner content with the existing 15-class Classify.
//     Empty-everywhere segments (gaps between adjacent anchors) are dropped.
//
// What this does NOT do (deferred to M4, per diff4-plan.md):
//   - Context-based hunk merging: adjacent segments of compatible class do
//     not get merged, and no pre/post-context lines are added. Each segment
//     is reported as-is, even if it's a single line surrounded by anchors.
//
// Iron uses Myers for cross-alignment (O(N·D)); we use patience for both.
// Both are valid LineDifferents; the distinction is perf-on-large-diffs,
// which is M3.
func computeSegments(b1, f1, b2, f2 []string) []Segment {
	pcs1 := PatienceMatches(b1, f1)
	pcs2 := PatienceMatches(b2, f2)

	// Project each PCS entry's matched content. Used only for cross-alignment.
	content1 := make([]string, len(pcs1))
	for i, p := range pcs1 {
		content1[i] = b1[p.AIdx]
	}
	content2 := make([]string, len(pcs2))
	for i, p := range pcs2 {
		content2[i] = b2[p.AIdx]
	}

	cross := PatienceMatches(content1, content2)

	// Each cross-match gives a super-anchor: a 4-tuple of corner indices.
	type anchor struct{ b1, f1, b2, f2 int }
	anchors := make([]anchor, len(cross))
	for i, cm := range cross {
		anchors[i] = anchor{
			b1: pcs1[cm.AIdx].AIdx,
			f1: pcs1[cm.AIdx].BIdx,
			b2: pcs2[cm.BIdx].AIdx,
			f2: pcs2[cm.BIdx].BIdx,
		}
	}

	var segments []Segment
	prev := anchor{b1: -1, f1: -1, b2: -1, f2: -1}

	emit := func(next anchor) {
		sb1 := joinRange(b1, prev.b1+1, next.b1)
		sf1 := joinRange(f1, prev.f1+1, next.f1)
		sb2 := joinRange(b2, prev.b2+1, next.b2)
		sf2 := joinRange(f2, prev.f2+1, next.f2)
		if sb1 == "" && sf1 == "" && sb2 == "" && sf2 == "" {
			return // empty gap between adjacent anchors
		}
		d := Diamond{B1: sb1, F1: sf1, B2: sb2, F2: sf2}
		segments = append(segments, Segment{
			Class: Classify(d, nil),
			B1:    sb1,
			F1:    sf1,
			B2:    sb2,
			F2:    sf2,
		})
	}

	for _, a := range anchors {
		emit(a)
		prev = a
	}
	// Trailing region past the last anchor.
	tail := anchor{b1: len(b1), f1: len(f1), b2: len(b2), f2: len(f2)}
	emit(tail)

	return segments
}

// joinRange joins lines[start:end] with newlines, returning "" for empty
// ranges. Matches the convention in Classify that empty corners compare
// equal to each other.
func joinRange(lines []string, start, end int) string {
	if start >= end || start < 0 || end > len(lines) {
		return ""
	}
	return strings.Join(lines[start:end], "\n")
}

// ComputeSlow runs the slow-path segmentation on a diamond. For hidden and
// shown-as-diff2 classes it still produces the top-level whole-file segment
// (fast path remains authoritative there); for complex classes it returns
// the per-segment decomposition.
//
// This is separate from Compute() so M1 callers keep working unchanged —
// Compute() is the stable whole-file interface; ComputeSlow() opts into the
// finer-grained output.
func ComputeSlow(d Diamond) *Result {
	top := Classify(d, nil)
	if top.Hidden() {
		return &Result{}
	}
	if top.ShownAsDiff2() {
		return &Result{Segments: []Segment{{Class: top, B1: d.B1, F1: d.F1, B2: d.B2, F2: d.F2}}}
	}
	segs := computeSegments(
		splitLinesForSeg(d.B1),
		splitLinesForSeg(d.F1),
		splitLinesForSeg(d.B2),
		splitLinesForSeg(d.F2),
	)
	if len(segs) == 0 {
		// Segmentation found nothing meaningful — fall back to the whole-file
		// segment so callers always get *something* to act on.
		return &Result{Segments: []Segment{{Class: top, B1: d.B1, F1: d.F1, B2: d.B2, F2: d.F2}}}
	}
	return &Result{Segments: segs}
}

func splitLinesForSeg(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	// Strings with a trailing newline produce a phantom empty final element;
	// drop it so anchor indices are in 1:1 correspondence with logical lines.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

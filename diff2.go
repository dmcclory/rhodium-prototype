package main

import (
	"fmt"
)

// diff2Hunks produces unified-diff-style hunks between two content strings,
// using PatienceMatches for the line alignment. Output is a slice of Hunk
// values with the same shape as parseHunks: an `@@ -a,b +c,d @@` header,
// BodyLines with leading ' ' / '-' / '+', and a Hash keyed off the +/- lines.
//
// This is the slow-path's per-segment renderer. It is NOT byte-identical to
// `git diff` output — patience can pick different alignments, context window
// merging is naive, and there's no \ No newline at end of file handling — but
// it's deterministic, self-contained, and good enough for in-TUI display.
func diff2Hunks(from, to string) []Hunk {
	const ctx = 3
	fromLines := splitLinesForSeg(from)
	toLines := splitLinesForSeg(to)
	matches := PatienceMatches(fromLines, toLines)

	ops := buildDiffOps(fromLines, toLines, matches)
	if len(ops) == 0 {
		return nil
	}
	windows := groupChangeWindows(ops, ctx)
	if len(windows) == 0 {
		return nil
	}
	return opsToHunks(ops, windows)
}

// diffOp is one line of the edit script: equal (' '), remove ('-'), add ('+').
type diffOp struct {
	kind byte
	text string
}

func buildDiffOps(fromLines, toLines []string, matches []MatchPair) []diffOp {
	var ops []diffOp
	ai, bi := 0, 0
	for _, m := range matches {
		for ai < m.AIdx {
			ops = append(ops, diffOp{'-', fromLines[ai]})
			ai++
		}
		for bi < m.BIdx {
			ops = append(ops, diffOp{'+', toLines[bi]})
			bi++
		}
		ops = append(ops, diffOp{' ', fromLines[m.AIdx]})
		ai++
		bi++
	}
	for ai < len(fromLines) {
		ops = append(ops, diffOp{'-', fromLines[ai]})
		ai++
	}
	for bi < len(toLines) {
		ops = append(ops, diffOp{'+', toLines[bi]})
		bi++
	}
	return ops
}

// changeWindow is a half-open [start, end) slice of ops indices covering one
// emitted hunk — a run of non-equal ops plus up to `ctx` context lines on
// either side, with adjacent windows merged when their context overlaps.
type changeWindow struct{ start, end int }

func groupChangeWindows(ops []diffOp, ctx int) []changeWindow {
	var windows []changeWindow
	i := 0
	for i < len(ops) {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		j := i
		for j < len(ops) && ops[j].kind != ' ' {
			j++
		}
		ws := i - ctx
		if ws < 0 {
			ws = 0
		}
		we := j + ctx
		if we > len(ops) {
			we = len(ops)
		}
		if n := len(windows); n > 0 && windows[n-1].end >= ws {
			windows[n-1].end = we
		} else {
			windows = append(windows, changeWindow{ws, we})
		}
		i = j
	}
	return windows
}

// maxSegmentViews is the largest Views() count across the given segments.
// Used to decide whether to advertise `v: cycle view` in the footer — there's
// nothing to cycle if every segment has a single view.
func maxSegmentViews(segments []Segment) int {
	max := 0
	for _, seg := range segments {
		if n := len(seg.Class.Views()); n > max {
			max = n
		}
	}
	return max
}

// segmentHunks renders each segment under the given per-segment view choice
// into a flat hunk list: a synthetic header hunk (Hash=="", unmarkable) that
// labels the segment, followed by the diff2Hunks of that segment's corners.
// viewIdx is the global alt-view cycle (phase 2 cycles it); each segment
// uses Views()[viewIdx % len(Views())].
func segmentHunks(segments []Segment, viewIdx int) []Hunk {
	var hunks []Hunk
	n := len(segments)
	for i, seg := range segments {
		views := seg.Class.Views()
		if len(views) == 0 {
			continue
		}
		view := views[viewIdx%len(views)]
		d := Diamond{B1: seg.B1, F1: seg.F1, B2: seg.B2, F2: seg.F2}
		from := d.Get(view.From)
		to := d.Get(view.To)
		header := fmt.Sprintf("== segment %d/%d · %s · %s→%s (%s) ==",
			i+1, n, seg.Class, view.From, view.To, view.Kind)
		hunks = append(hunks, Hunk{Header: header})
		hunks = append(hunks, diff2Hunks(from, to)...)
	}
	return hunks
}

func opsToHunks(ops []diffOp, windows []changeWindow) []Hunk {
	// Prefix sums: aAt[i] = number of from-lines consumed by ops[:i].
	aAt := make([]int, len(ops)+1)
	bAt := make([]int, len(ops)+1)
	for i, o := range ops {
		aAt[i+1] = aAt[i]
		bAt[i+1] = bAt[i]
		if o.kind == ' ' || o.kind == '-' {
			aAt[i+1]++
		}
		if o.kind == ' ' || o.kind == '+' {
			bAt[i+1]++
		}
	}

	hunks := make([]Hunk, 0, len(windows))
	for _, w := range windows {
		aCount := aAt[w.end] - aAt[w.start]
		bCount := bAt[w.end] - bAt[w.start]
		aStart := aAt[w.start] + 1
		bStart := bAt[w.start] + 1
		if aCount == 0 {
			aStart = aAt[w.start]
		}
		if bCount == 0 {
			bStart = bAt[w.start]
		}
		header := fmt.Sprintf("@@ -%d,%d +%d,%d @@", aStart, aCount, bStart, bCount)
		body := make([]string, 0, w.end-w.start)
		for k := w.start; k < w.end; k++ {
			body = append(body, string(ops[k].kind)+ops[k].text)
		}
		hunks = append(hunks, Hunk{
			Header:    header,
			BodyLines: body,
			Hash:      hashHunkBody(body),
		})
	}
	return hunks
}

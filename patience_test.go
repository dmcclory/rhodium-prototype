package main

import (
	"reflect"
	"strings"
	"testing"
)

// helper: asserts that matches are in strictly increasing order of both
// AIdx and BIdx, and that every pair points to equal lines in a/b.
func assertValidMatches(t *testing.T, a, b []string, matches []MatchPair) {
	t.Helper()
	for i, m := range matches {
		if m.AIdx < 0 || m.AIdx >= len(a) {
			t.Fatalf("match %d: AIdx %d out of range [0,%d)", i, m.AIdx, len(a))
		}
		if m.BIdx < 0 || m.BIdx >= len(b) {
			t.Fatalf("match %d: BIdx %d out of range [0,%d)", i, m.BIdx, len(b))
		}
		if a[m.AIdx] != b[m.BIdx] {
			t.Fatalf("match %d points to unequal lines: a[%d]=%q vs b[%d]=%q", i, m.AIdx, a[m.AIdx], m.BIdx, b[m.BIdx])
		}
		if i > 0 {
			prev := matches[i-1]
			if m.AIdx <= prev.AIdx {
				t.Fatalf("match %d: AIdx not increasing (%d after %d)", i, m.AIdx, prev.AIdx)
			}
			if m.BIdx <= prev.BIdx {
				t.Fatalf("match %d: BIdx not increasing (%d after %d)", i, m.BIdx, prev.BIdx)
			}
		}
	}
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func TestPatienceEmpty(t *testing.T) {
	if got := PatienceMatches(nil, nil); len(got) != 0 {
		t.Errorf("nil,nil: got %v, want empty", got)
	}
	if got := PatienceMatches(nil, []string{"x"}); len(got) != 0 {
		t.Errorf("nil,[x]: got %v, want empty", got)
	}
	if got := PatienceMatches([]string{"x"}, nil); len(got) != 0 {
		t.Errorf("[x],nil: got %v, want empty", got)
	}
}

func TestPatienceIdentical(t *testing.T) {
	a := lines("one\ntwo\nthree\nfour")
	got := PatienceMatches(a, a)
	want := []MatchPair{
		{0, 0}, {1, 1}, {2, 2}, {3, 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	assertValidMatches(t, a, a, got)
}

func TestPatiencePureInsertion(t *testing.T) {
	a := lines("one\ntwo\nthree")
	b := lines("one\ntwo\nNEW\nthree")
	got := PatienceMatches(a, b)
	// Prefix "one", "two" match. Suffix "three" matches. Middle is empty on a.
	want := []MatchPair{
		{0, 0}, {1, 1}, {2, 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	assertValidMatches(t, a, b, got)
}

func TestPatiencePureDeletion(t *testing.T) {
	a := lines("one\ntwo\nGONE\nthree")
	b := lines("one\ntwo\nthree")
	got := PatienceMatches(a, b)
	want := []MatchPair{
		{0, 0}, {1, 1}, {3, 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	assertValidMatches(t, a, b, got)
}

func TestPatienceNoCommonLines(t *testing.T) {
	a := lines("alpha\nbeta\ngamma")
	b := lines("delta\nepsilon\nzeta")
	got := PatienceMatches(a, b)
	if len(got) != 0 {
		t.Errorf("completely disjoint: got %v, want empty", got)
	}
}

// TestPatienceReorderedBlocks is the canonical case where patience beats
// naive longest-common-subsequence: two reorderable blocks each containing
// a unique marker line. Patience should anchor on the unique markers and
// match whole blocks together, giving a "structural" result.
func TestPatienceReorderedBlocks(t *testing.T) {
	a := lines("funcA_sig\nfuncA_body1\nfuncA_body2\nfuncB_sig\nfuncB_body1\nfuncB_body2")
	b := lines("funcB_sig\nfuncB_body1\nfuncB_body2\nfuncA_sig\nfuncA_body1\nfuncA_body2")

	got := PatienceMatches(a, b)
	assertValidMatches(t, a, b, got)

	// Patience must anchor on at least one of the unique function sigs (they
	// each appear once in a and once in b). A naive LCS might match the
	// `_body` lines — but those aren't unique. We just assert structural
	// invariants plus the existence of at least one signature anchor.
	foundSigAnchor := false
	for _, m := range got {
		if strings.Contains(a[m.AIdx], "_sig") {
			foundSigAnchor = true
			break
		}
	}
	if !foundSigAnchor {
		t.Errorf("expected a _sig anchor match; got: %v", got)
	}
}

// TestPatienceDuplicateLines verifies the "find unique" step: lines that
// appear multiple times in either side can't be patience anchors.
func TestPatienceDuplicateLines(t *testing.T) {
	// "x" appears twice in a, so it's not unique — can't be an anchor.
	// "unique" appears once in each, so it anchors.
	a := lines("x\nunique\nx")
	b := lines("x\nunique\nx")

	got := PatienceMatches(a, b)
	// Common prefix strips "x" and "unique"; common suffix strips "x".
	// All three lines match by prefix/suffix stripping alone.
	assertValidMatches(t, a, b, got)
	if len(got) != 3 {
		t.Errorf("expected 3 matches (all via prefix/suffix), got %d: %v", len(got), got)
	}
}

// TestPatienceDuplicateOnlyInOneSide: duplicates on only one side still can't
// anchor. Combined with content changes, verifies recursion into the middle.
func TestPatienceDuplicateOnlyInOneSide(t *testing.T) {
	a := lines("head\nx\nmiddle\nx\ntail")
	b := lines("head\nx\nCHANGED\nx\ntail")

	got := PatienceMatches(a, b)
	assertValidMatches(t, a, b, got)
	// Prefix matches head (0,0). Suffix matches tail (4,4). The middle
	// region is "x\nmiddle\nx" vs "x\nCHANGED\nx" — in the middle, "x"
	// appears twice on each side (not unique), and "middle"/"CHANGED"
	// are unique but don't match. Patience should still pick up the
	// head/tail and leave the "x" lines unmatched (since they're not
	// anchors).
	//
	// Actually the outer "x" lines might be swept up by prefix/suffix
	// stripping. Prefix stripping: a[0]=head, b[0]=head → match. a[1]=x,
	// b[1]=x → match. a[2]=middle vs b[2]=CHANGED → stop. So prefix gives
	// us (0,0), (1,1). Suffix: a[4]=tail, b[4]=tail → match. a[3]=x,
	// b[3]=x → match. So suffix gives (3,3), (4,4). Middle is empty on
	// both sides. Total: 4 matches.
	if len(got) != 4 {
		t.Errorf("expected 4 matches, got %d: %v", len(got), got)
	}
}

// TestPatienceLISTieBreaking: when multiple unique matches form crossings,
// the LIS resolves them. Verify by construction.
func TestPatienceLISCrossings(t *testing.T) {
	// "A" at a[0]→b[2], "B" at a[1]→b[0], "C" at a[2]→b[1].
	// Unique matches sorted by AIdx: (0,2), (1,0), (2,1).
	// LIS by BIdx: longest increasing subseq of [2,0,1] is [0,1] = length 2.
	// So patience anchors on (1,0) and (2,1), dropping (0,2).
	a := []string{"A", "B", "C"}
	b := []string{"B", "C", "A"}
	got := PatienceMatches(a, b)
	assertValidMatches(t, a, b, got)
	// We should have exactly those two LIS anchors, no extras (middle gaps
	// have no unique common lines remaining).
	want := []MatchPair{{1, 0}, {2, 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestLISByB(t *testing.T) {
	tests := []struct {
		name  string
		input []MatchPair
		want  []MatchPair
	}{
		{"empty", nil, nil},
		{"single", []MatchPair{{0, 5}}, []MatchPair{{0, 5}}},
		{"already increasing",
			[]MatchPair{{0, 1}, {1, 2}, {2, 3}},
			[]MatchPair{{0, 1}, {1, 2}, {2, 3}},
		},
		{"reversed picks one",
			[]MatchPair{{0, 3}, {1, 2}, {2, 1}},
			[]MatchPair{{2, 1}}, // LIS length 1; tie-breaker: the last tails[0] wins
		},
		{"classic [2,0,1] picks [0,1]",
			[]MatchPair{{0, 2}, {1, 0}, {2, 1}},
			[]MatchPair{{1, 0}, {2, 1}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lisByB(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("lisByB(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestFindUniqueMatches(t *testing.T) {
	a := []string{"once_a", "dup", "once_b", "dup"}
	b := []string{"dup", "once_b", "once_a", "dup"}
	got := findUniqueMatches(a, b)
	// "once_a" and "once_b" are unique in both; "dup" is not.
	want := []MatchPair{
		{0, 2}, // once_a
		{2, 1}, // once_b
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

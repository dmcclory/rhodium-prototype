package main

import "sort"

// MatchPair is a pairing of (aIdx, bIdx) where a[aIdx] equals b[bIdx]. Paired
// matches are always in strictly increasing order of both indices — they
// represent a common subsequence of the two line slices.
type MatchPair struct {
	AIdx, BIdx int
}

// PatienceMatches returns the matched line pairs between two line slices
// using the patience diff algorithm. The algorithm is:
//
//  1. Strip common prefix and suffix — all those lines pair trivially.
//  2. In the middle region, find lines that appear exactly once in a and
//     exactly once in b (and are equal) — these are candidate anchors.
//  3. Take the longest increasing subsequence of anchors by b-index.
//     Dropped anchors represent crossings the algorithm commits to ignoring.
//  4. Recurse into the gaps between consecutive anchors.
//
// Lines are compared with Go string equality. If you need looser equality
// (e.g., whitespace-insensitive), normalize lines upstream before calling.
//
// Output is in increasing order of both AIdx and BIdx.
func PatienceMatches(a, b []string) []MatchPair {
	var out []MatchPair
	patienceRec(a, b, 0, 0, &out)
	return out
}

func patienceRec(a, b []string, aOff, bOff int, out *[]MatchPair) {
	// Strip common prefix — trivially matched, no recursion needed.
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		*out = append(*out, MatchPair{AIdx: aOff + prefix, BIdx: bOff + prefix})
		prefix++
	}

	// Strip common suffix. Measured from the end of the post-prefix slices.
	aRem := a[prefix:]
	bRem := b[prefix:]
	suffix := 0
	for suffix < len(aRem) && suffix < len(bRem) && aRem[len(aRem)-1-suffix] == bRem[len(bRem)-1-suffix] {
		suffix++
	}

	// Suffix matches are emitted after the middle's recursive output so the
	// overall MatchPair list stays in increasing AIdx order.
	aMid := aRem[:len(aRem)-suffix]
	bMid := bRem[:len(bRem)-suffix]
	midOffA := aOff + prefix
	midOffB := bOff + prefix

	if len(aMid) == 0 || len(bMid) == 0 {
		// One side is empty — nothing in the middle could match. Skip to
		// suffix.
		appendSuffix(out, a, b, aOff, bOff, suffix)
		return
	}

	unique := findUniqueMatches(aMid, bMid)
	if len(unique) == 0 {
		// No unique anchors — patience gives up on the middle. Iron falls
		// back to a plain diff here; for M2 we also stop, meaning the entire
		// middle is reported as unmatched (no MatchPairs emitted for it).
		appendSuffix(out, a, b, aOff, bOff, suffix)
		return
	}

	anchors := lisByB(unique)

	prevA, prevB := -1, -1
	for _, m := range anchors {
		// Recurse into the gap preceding this anchor.
		subA := aMid[prevA+1 : m.AIdx]
		subB := bMid[prevB+1 : m.BIdx]
		patienceRec(subA, subB, midOffA+prevA+1, midOffB+prevB+1, out)
		// Emit the anchor itself (translating local → global indices).
		*out = append(*out, MatchPair{AIdx: midOffA + m.AIdx, BIdx: midOffB + m.BIdx})
		prevA = m.AIdx
		prevB = m.BIdx
	}
	// Gap after the final anchor.
	subA := aMid[prevA+1:]
	subB := bMid[prevB+1:]
	patienceRec(subA, subB, midOffA+prevA+1, midOffB+prevB+1, out)

	appendSuffix(out, a, b, aOff, bOff, suffix)
}

func appendSuffix(out *[]MatchPair, a, b []string, aOff, bOff, suffix int) {
	for i := 0; i < suffix; i++ {
		aIdx := aOff + len(a) - suffix + i
		bIdx := bOff + len(b) - suffix + i
		*out = append(*out, MatchPair{AIdx: aIdx, BIdx: bIdx})
	}
}

// findUniqueMatches returns (aIdx, bIdx) pairs for every line that appears
// exactly once in a, exactly once in b, and is equal across the two —
// patience's "structural anchor" candidates. Returned slice is sorted by AIdx.
func findUniqueMatches(a, b []string) []MatchPair {
	aCount := make(map[string]int, len(a))
	aIndex := make(map[string]int, len(a))
	for i, line := range a {
		aCount[line]++
		if aCount[line] == 1 {
			aIndex[line] = i
		}
	}
	bCount := make(map[string]int, len(b))
	bIndex := make(map[string]int, len(b))
	for i, line := range b {
		bCount[line]++
		if bCount[line] == 1 {
			bIndex[line] = i
		}
	}

	var out []MatchPair
	for line, ac := range aCount {
		if ac != 1 {
			continue
		}
		if bCount[line] != 1 {
			continue
		}
		out = append(out, MatchPair{AIdx: aIndex[line], BIdx: bIndex[line]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AIdx < out[j].AIdx })
	return out
}

// lisByB returns the longest increasing subsequence of pairs by BIdx. Input
// is assumed to be sorted by AIdx. O(n log n) via the patience-sort trick.
func lisByB(pairs []MatchPair) []MatchPair {
	if len(pairs) == 0 {
		return nil
	}
	// tails[k] = index into pairs whose BIdx is the smallest tail of any
	// increasing subsequence of length k+1 discovered so far.
	tails := make([]int, 0, len(pairs))
	prev := make([]int, len(pairs))
	for i := range prev {
		prev[i] = -1
	}

	for i, p := range pairs {
		lo, hi := 0, len(tails)
		for lo < hi {
			mid := (lo + hi) / 2
			if pairs[tails[mid]].BIdx < p.BIdx {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		if lo > 0 {
			prev[i] = tails[lo-1]
		}
		if lo == len(tails) {
			tails = append(tails, i)
		} else {
			tails[lo] = i
		}
	}

	// Reconstruct by walking `prev` from the end of the longest chain.
	result := make([]MatchPair, 0, len(tails))
	k := tails[len(tails)-1]
	for k >= 0 {
		result = append(result, pairs[k])
		k = prev[k]
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

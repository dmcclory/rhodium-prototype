package diff

import (
	"strings"
	"testing"
)

func TestDiff2HunksEmpty(t *testing.T) {
	if got := Diff2Hunks("", ""); got != nil {
		t.Errorf("Diff2Hunks(\"\", \"\") = %+v, want nil", got)
	}
}

func TestDiff2HunksIdentical(t *testing.T) {
	s := "one\ntwo\nthree\n"
	if got := Diff2Hunks(s, s); got != nil {
		t.Errorf("identical input: got %+v, want nil", got)
	}
}

func TestDiff2HunksPureInsertion(t *testing.T) {
	from := ""
	to := "a\nb\nc"
	hs := Diff2Hunks(from, to)
	if len(hs) != 1 {
		t.Fatalf("got %d hunks, want 1: %+v", len(hs), hs)
	}
	if want := "@@ -0,0 +1,3 @@"; hs[0].Header != want {
		t.Errorf("header = %q, want %q", hs[0].Header, want)
	}
	wantBody := []string{"+a", "+b", "+c"}
	if !equalLines(hs[0].BodyLines, wantBody) {
		t.Errorf("body = %v, want %v", hs[0].BodyLines, wantBody)
	}
	if hs[0].Hash == "" {
		t.Error("hash should be non-empty for markable hunks")
	}
}

func TestDiff2HunksPureDeletion(t *testing.T) {
	from := "a\nb\nc"
	to := ""
	hs := Diff2Hunks(from, to)
	if len(hs) != 1 {
		t.Fatalf("got %d hunks, want 1: %+v", len(hs), hs)
	}
	if want := "@@ -1,3 +0,0 @@"; hs[0].Header != want {
		t.Errorf("header = %q, want %q", hs[0].Header, want)
	}
	wantBody := []string{"-a", "-b", "-c"}
	if !equalLines(hs[0].BodyLines, wantBody) {
		t.Errorf("body = %v, want %v", hs[0].BodyLines, wantBody)
	}
}

func TestDiff2HunksReplacement(t *testing.T) {
	from := "header\nold line\nfooter"
	to := "header\nnew line\nfooter"
	hs := Diff2Hunks(from, to)
	if len(hs) != 1 {
		t.Fatalf("got %d hunks, want 1: %+v", len(hs), hs)
	}
	h := hs[0]
	// With ctx=3, the change window expands to include both context
	// lines, giving a single 4-line hunk.
	wantBody := []string{" header", "-old line", "+new line", " footer"}
	if !equalLines(h.BodyLines, wantBody) {
		t.Errorf("body = %v, want %v", h.BodyLines, wantBody)
	}
	if want := "@@ -1,3 +1,3 @@"; h.Header != want {
		t.Errorf("header = %q, want %q", h.Header, want)
	}
}

func TestDiff2HunksTwoCloseChangesMerge(t *testing.T) {
	// Two changes separated by fewer than 2*ctx (=6) equal lines — they
	// should merge into a single hunk so the shared context isn't emitted
	// twice.
	from := "a\nb\nc\nd\ne\nf\ng"
	to := "a\nB\nc\nd\ne\nF\ng"
	hs := Diff2Hunks(from, to)
	if len(hs) != 1 {
		t.Fatalf("got %d hunks, want 1 (merged): %+v", len(hs), hs)
	}
	// Confirm both edits are represented.
	body := strings.Join(hs[0].BodyLines, "\n")
	if !strings.Contains(body, "-b") || !strings.Contains(body, "+B") {
		t.Errorf("first edit missing from merged hunk: %q", body)
	}
	if !strings.Contains(body, "-f") || !strings.Contains(body, "+F") {
		t.Errorf("second edit missing from merged hunk: %q", body)
	}
}

func TestDiff2HunksTwoDistantChangesSplit(t *testing.T) {
	// Two changes separated by > 2*ctx (=6) equal lines — produce two
	// distinct hunks.
	from := "a\nx\nc\nd\ne\nf\ng\nh\ni\ny\nk"
	to := "a\nX\nc\nd\ne\nf\ng\nh\ni\nY\nk"
	hs := Diff2Hunks(from, to)
	if len(hs) != 2 {
		t.Fatalf("got %d hunks, want 2: %+v", len(hs), hs)
	}
	if !containsLine(hs[0].BodyLines, "-x") || !containsLine(hs[0].BodyLines, "+X") {
		t.Errorf("first hunk missing the expected edit: %+v", hs[0].BodyLines)
	}
	if !containsLine(hs[1].BodyLines, "-y") || !containsLine(hs[1].BodyLines, "+Y") {
		t.Errorf("second hunk missing the expected edit: %+v", hs[1].BodyLines)
	}
	// Hashes should differ — otherwise mark state leaks between hunks.
	if hs[0].Hash == hs[1].Hash {
		t.Error("expected distinct hashes for distinct hunks")
	}
}

func TestDiff2HunksTrailingNewline(t *testing.T) {
	// Trailing-newline variations should be canonicalized by splitLinesForSeg
	// so they don't produce spurious "" diff lines.
	h1 := Diff2Hunks("a\nb", "a\nc")
	h2 := Diff2Hunks("a\nb\n", "a\nc\n")
	if len(h1) != len(h2) {
		t.Fatalf("hunk count differs: %d vs %d", len(h1), len(h2))
	}
	for i := range h1 {
		if h1[i].Hash != h2[i].Hash {
			t.Errorf("hunk %d hash changes with trailing newline", i)
		}
	}
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsLine(lines []string, s string) bool {
	for _, l := range lines {
		if l == s {
			return true
		}
	}
	return false
}

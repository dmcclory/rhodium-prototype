package main

import (
	"path/filepath"
	"testing"
)

func TestDecideAdvance(t *testing.T) {
	// Two hunks with deterministic hashes from hashHunkBody.
	h1 := Hunk{Header: "@@ -1,2 +1,3 @@", BodyLines: []string{"+alpha"}, Hash: hashHunkBody([]string{"+alpha"})}
	h2 := Hunk{Header: "@@ -10,1 +11,2 @@", BodyLines: []string{"+beta"}, Hash: hashHunkBody([]string{"+beta"})}
	hunks := []Hunk{h1, h2}

	t.Run("no hunks", func(t *testing.T) {
		if got := decideAdvance(nil, nil, "x", "y"); got != advanceNoHunks {
			t.Errorf("got %v, want advanceNoHunks", got)
		}
	})

	t.Run("all hunks marked wins over rev-update", func(t *testing.T) {
		marks := map[string]bool{h1.Hash: true, h2.Hash: true}
		// Even if content differs, all-marked is the strongest advance reason.
		if got := decideAdvance(hunks, marks, "old", "new"); got != advanceAllMarked {
			t.Errorf("got %v, want advanceAllMarked", got)
		}
	})

	t.Run("rev-update when content identical", func(t *testing.T) {
		marks := map[string]bool{h1.Hash: true} // only one of two marked
		content := "package main\n\nfunc main() {}\n"
		if got := decideAdvance(hunks, marks, content, content); got != advanceRevUpdate {
			t.Errorf("got %v, want advanceRevUpdate", got)
		}
	})

	t.Run("no advance when content differs and not all marked", func(t *testing.T) {
		marks := map[string]bool{h1.Hash: true}
		if got := decideAdvance(hunks, marks, "old", "new"); got != advanceNone {
			t.Errorf("got %v, want advanceNone", got)
		}
	})

	t.Run("missing oldContent cannot trigger rev-update", func(t *testing.T) {
		marks := map[string]bool{h1.Hash: true}
		if got := decideAdvance(hunks, marks, "", "new"); got != advanceNone {
			t.Errorf("got %v, want advanceNone (oldContent unavailable)", got)
		}
	})

	t.Run("both empty never counts as rev-update", func(t *testing.T) {
		// Guard: "" == "" must not trick us into auto-advancing a file we
		// actually know nothing about. Delete/forget is the caller's job.
		marks := map[string]bool{}
		if got := decideAdvance(hunks, marks, "", ""); got != advanceNone {
			t.Errorf("got %v, want advanceNone", got)
		}
	})

	t.Run("zero hunks short-circuits before mark check", func(t *testing.T) {
		// No hunks but non-empty marks (stale state) — still noHunks.
		marks := map[string]bool{"stale-hash": true}
		if got := decideAdvance(nil, marks, "same", "same"); got != advanceNoHunks {
			t.Errorf("got %v, want advanceNoHunks", got)
		}
	})
}

// TestProbeAdvanceLocalOnly verifies that files resolvable from brain state
// alone (all-marked, no-hunks) are returned without the worker pool ever
// needing to run — important because each worker would otherwise spawn a gh
// subprocess, slowing down PRs with many already-reviewed files.
func TestProbeAdvanceLocalOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RHODIUM_BRAIN", filepath.Join(dir, "brain.db"))

	b, err := LoadBrain()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	pr := PR{Repo: "acme/web", Number: 42, HeadSHA: "newhead", BaseSHA: "newbase"}

	// fcEmpty: patch has no hunks (pure rename / binary). decideAdvance
	// should short-circuit with advanceNoHunks.
	fcEmpty := FileChange{Path: "renamed.go", Patch: ""}

	// fcMarked: two hunks, both ticked off in the brain.
	fcMarked := FileChange{Path: "src/main.go", Patch: samplePatch}
	hunks := parseHunks(samplePatch)
	marks := map[string]bool{hunks[0].Hash: true, hunks[1].Hash: true}
	if err := b.SetHunkMarks(pr.Repo, pr.Number, fcMarked.Path, marks); err != nil {
		t.Fatal(err)
	}

	states := map[string]FileReviewState{
		fcEmpty.Path:  {HeadSHA: "oldhead", BaseSHA: "oldbase"},
		fcMarked.Path: {HeadSHA: "oldhead", BaseSHA: "oldbase"},
	}

	// workers=0 means the fetch pool never spawns — anything not resolved
	// in the first pass simply doesn't appear in the output map.
	got := probeAdvance(b, pr, []FileChange{fcEmpty, fcMarked}, states, 0)

	if got[fcEmpty.Path] != advanceNoHunks {
		t.Errorf("empty patch: got %v, want advanceNoHunks", got[fcEmpty.Path])
	}
	if got[fcMarked.Path] != advanceAllMarked {
		t.Errorf("all-marked: got %v, want advanceAllMarked", got[fcMarked.Path])
	}
}

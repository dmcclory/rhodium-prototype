package main

// advanceReason is why a file qualifies (or doesn't) for implicit auto-advance
// when the reviewer re-enters a PR whose head or base has moved. Pure logic
// lives in decideAdvance so the rev-update probe can be unit-tested without
// hitting GitHub.
type advanceReason int

const (
	advanceNone      advanceReason = iota // reviewer still needs to see this file
	advanceNoHunks                        // patch had no hunks (binary, huge, pure rename)
	advanceAllMarked                      // every current hunk is already ticked off in the brain
	advanceRevUpdate                      // f1 == f2: the file's bytes haven't changed through a rebase / force-push
)

func (r advanceReason) advances() bool {
	return r != advanceNone
}

// decideAdvance returns the reason this file can be auto-advanced in the
// brain, or advanceNone if it can't. The caller is responsible for fetching
// oldContent (f1) and newContent (f2) when applicable.
//
// oldContent == "" means we don't have it (never reviewed, or fetch failed);
// in that case rev-update can't fire and we fall back to the mark-based
// shortcut alone. An empty newContent is treated the same way — we refuse to
// call two empty strings a match, because "file missing at both refs" is a
// delete/forget case the caller should handle explicitly, not us.
func decideAdvance(hunks []Hunk, marks map[string]bool, oldContent, newContent string) advanceReason {
	if len(hunks) == 0 {
		return advanceNoHunks
	}
	allMarked := true
	for _, h := range hunks {
		if !marks[h.Hash] {
			allMarked = false
			break
		}
	}
	if allMarked {
		return advanceAllMarked
	}
	if oldContent != "" && newContent != "" && oldContent == newContent {
		return advanceRevUpdate
	}
	return advanceNone
}

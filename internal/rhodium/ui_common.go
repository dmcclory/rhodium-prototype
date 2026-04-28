package rhodium

import (
	"rhodium/internal/diff"
)

// view identifies which sub-model is currently focused.
type view int

const (
	viewTodo view = iota
	viewPRs
	viewFiles
	viewDiff
	viewComments
)

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// hashLine wraps a single string as a single-line "+"-prefixed hunk
// body and runs it through the hunk hasher. Used by the cli surface for
// note line anchoring; the diff view has its own copy for the same
// purpose.
func hashLine(s string) string {
	return diff.HashHunkBody([]string{"+" + s})
}

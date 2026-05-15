package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"

	"rhodium/internal/brain"
	"rhodium/internal/diff"
	"rhodium/internal/gh"
)

type stateHunk struct {
	Hash    string `json:"hash"`
	Header  string `json:"header"`
	OldLine int    `json:"old_line"`
	NewLine int    `json:"new_line"`
	Marked  bool   `json:"marked"`
}

type stateFile struct {
	Path      string       `json:"path"`
	Status    string       `json:"status"` // unseen | partial | seen
	Additions int          `json:"additions"`
	Deletions int          `json:"deletions"`
	Patch     string       `json:"patch"`
	Hunks     []stateHunk  `json:"hunks"`
	Notes     []brain.Note `json:"notes"`
}

type stateOutput struct {
	Key     string      `json:"key"`
	Repo    string      `json:"repo"`
	Number  int         `json:"number"`
	Title   string      `json:"title"`
	Author  string      `json:"author"`
	HeadSHA string      `json:"head_sha"`
	BaseSHA string      `json:"base_sha"`
	Files   []stateFile `json:"files"`
}

func statusName(s brain.FileStatus) string {
	switch s {
	case brain.StatusSeen:
		return "seen"
	case brain.StatusPartial:
		return "partial"
	default:
		return "unseen"
	}
}

var hunkHeaderLineRE = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

func hunkLines(header string) (oldLine, newLine int) {
	m := hunkHeaderLineRE.FindStringSubmatch(header)
	if m == nil {
		return 0, 0
	}
	oldLine, _ = strconv.Atoi(m[1])
	newLine, _ = strconv.Atoi(m[2])
	return
}

// cmdState prints the full review state for a PR as JSON — the nvim plugin's
// primary source of truth. Fetches file data from gh on demand.
func cmdState(args []string) error {
	flags, pos := splitFlags(args)
	fs := flag.NewFlagSet("state", flag.ContinueOnError)
	asJSON := fs.Bool("json", true, "emit JSON (default)")
	if err := fs.Parse(flags); err != nil {
		return err
	}
	_ = asJSON // --json is accepted for symmetry; output is always JSON here
	if len(pos) != 1 {
		return fmt.Errorf("usage: rhodium state <owner/repo#N>")
	}
	repo, num, err := parsePRRef(pos[0])
	if err != nil {
		return err
	}

	b, err := brain.OpenForCLI()
	if err != nil {
		return err
	}
	defer b.Close()

	files, err := gh.ListPRFiles(repo, num)
	if err != nil {
		return err
	}

	out := stateOutput{
		Key:    brain.PRKey(repo, num),
		Repo:   repo,
		Number: num,
	}
	for _, p := range b.CachedPRs() {
		if p.Repo == repo && p.Number == num {
			out.Title = p.Title
			out.Author = p.Author
			out.HeadSHA = p.HeadSHA
			out.BaseSHA = p.BaseSHA
			break
		}
	}

	for _, fc := range files {
		marks := b.HunkMarks(repo, num, fc.Path)
		hunks := diff.ParseHunks(fc.Patch)
		sh := make([]stateHunk, 0, len(hunks))
		for _, h := range hunks {
			oldL, newL := hunkLines(h.Header)
			sh = append(sh, stateHunk{
				Hash:    h.Hash,
				Header:  h.Header,
				OldLine: oldL,
				NewLine: newL,
				Marked:  marks[h.Hash] > 0,
			})
		}
		out.Files = append(out.Files, stateFile{
			Path:      fc.Path,
			Status:    statusName(b.Status(repo, num, fc)),
			Additions: fc.Additions,
			Deletions: fc.Deletions,
			Patch:     fc.Patch,
			Hunks:     sh,
			Notes:     b.NotesForFile(repo, num, fc.Path),
		})
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

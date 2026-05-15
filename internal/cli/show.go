package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"text/tabwriter"

	"rhodium/internal/brain"
	"rhodium/internal/diff"
	"rhodium/internal/gh"
)

// cmdBrainShow prints a human-readable summary of review state for a PR.
// Unlike `rhodium state` (full JSON), this is a glanceable dashboard showing
// file status, notes, session progress, and scrutiny mode.
func cmdBrainShow(args []string) error {
	flags, pos := splitFlags(args)
	fs := flag.NewFlagSet("brain show", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: rhodium brain show <owner/repo#N>")
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

	prInfo, err := lookupCachedPR(b, repo, num)
	if err != nil {
		return err
	}

	files, err := gh.ListPRFiles(repo, num)
	if err != nil {
		return fmt.Errorf("fetching PR files: %w", err)
	}

	data := gatherShowData(b, repo, num, prInfo, files)

	if *asJSON {
		_, err = fmt.Println(renderShowJSON(data))
		return err
	}
	_, err = fmt.Println(renderShowText(data))
	return err
}

// showData holds everything the show command needs to render.
type showData struct {
	Key         string
	Title       string
	Author      string
	HeadSHA     string
	BaseSHA     string
	Scrutinized bool
	Session     *brain.ReviewSession
	Notes       []brain.Note
	Files       []fileSummary
	FilesDone   map[string]brain.FileReviewState
}

type fileSummary struct {
	Path      string
	Status    string
	Glyph     string
	Hunks     int
	Marked    int
	Additions int
	Deletions int
}

func gatherShowData(b *brain.Brain, repo string, num int, prInfo gh.PR, files []gh.FileChange) showData {
	key := brain.PRKey(repo, num)
	data := showData{
		Key:         key,
		Title:       prInfo.Title,
		Author:      prInfo.Author,
		HeadSHA:     prInfo.HeadSHA,
		BaseSHA:     prInfo.BaseSHA,
		Scrutinized: b.IsScrutinized(repo, num),
		Session:     b.ActiveSession(repo, num),
		Notes:       b.NotesForPR(repo, num, brain.NotesActive),
		FilesDone:   b.AllFileReviewedStates(repo, num),
	}
	for _, f := range files {
		data.Files = append(data.Files, buildFileSummary(b, repo, num, f))
	}
	return data
}

func buildFileSummary(b *brain.Brain, repo string, num int, f gh.FileChange) fileSummary {
	hunks := diff.ParseHunks(f.Patch)
	marks := b.HunkMarks(repo, num, f.Path)
	marked := 0
	for _, h := range hunks {
		if marks[h.Hash] > 0 {
			marked++
		}
	}
	st := b.Status(repo, num, f)
	return fileSummary{
		Path:      f.Path,
		Status:    statusName(st),
		Glyph:     st.Glyph(),
		Hunks:     len(hunks),
		Marked:    marked,
		Additions: f.Additions,
		Deletions: f.Deletions,
	}
}

func lookupCachedPR(b *brain.Brain, repo string, num int) (gh.PR, error) {
	for _, p := range b.CachedPRs() {
		if p.Repo == repo && p.Number == num {
			return p, nil
		}
	}
	return gh.PR{}, fmt.Errorf("PR %s not found in brain cache — run `rhodium todo --sync` first", brain.PRKey(repo, num))
}

// renderShowJSON emits the full show data as indented JSON.
func renderShowJSON(d showData) string {
	totalHunks, totalMarked, unseenCount := aggregateHunks(d.Files)
	out := map[string]any{
		"pr":             d.Key,
		"title":          d.Title,
		"author":         d.Author,
		"head_sha":       d.HeadSHA,
		"base_sha":       d.BaseSHA,
		"scrutiny":       d.Scrutinized,
		"files_total":    len(d.Files),
		"files_unseen":   unseenCount,
		"hunks_total":    totalHunks,
		"hunks_marked":   totalMarked,
		"notes_active":   len(d.Notes),
		"files_reviewed": len(d.FilesDone),
		"files":          d.Files,
	}
	if d.Session != nil {
		out["session"] = map[string]any{
			"files_total": d.Session.FilesTotal,
			"files_done":  d.Session.FilesDone,
			"lines_total": d.Session.LinesTotal,
			"lines_done":  d.Session.LinesDone,
		}
	}
	raw, _ := json.MarshalIndent(out, "", "  ")
	return string(raw)
}

// renderShowText renders the human-readable dashboard.
func renderShowText(d showData) string {
	var b strings.Builder
	b.WriteString(renderShowHeader(d))
	b.WriteString(renderShowHunkProgress(d.Files))
	b.WriteString(renderShowFiles(d.Files))
	b.WriteString(renderShowNotes(d.Notes))
	return b.String()
}

func renderShowHeader(d showData) string {
	scrutinyStr := "off"
	if d.Scrutinized {
		scrutinyStr = "on"
	}
	var sessionLine string
	if d.Session != nil {
		sessionLine = fmt.Sprintf("Session: %d/%d files, %d/%d lines  |  Scrutiny: %s", d.Session.FilesDone, d.Session.FilesTotal, d.Session.LinesDone, d.Session.LinesTotal, scrutinyStr)
	} else {
		sessionLine = fmt.Sprintf("Scrutiny: %s", scrutinyStr)
	}
	return fmt.Sprintf("%s — %s\nAuthor: %-20s  Head: %-7s  Base: %-7s\n\n%s\n\n",
		d.Key, d.Title,
		d.Author, shortSHA(d.HeadSHA), shortSHA(d.BaseSHA),
		sessionLine,
	)
}

func renderShowHunkProgress(files []fileSummary) string {
	totalHunks, totalMarked, _ := aggregateHunks(files)
	if totalHunks > 0 {
		pct := totalMarked * 100 / totalHunks
		bar := progressBar(totalMarked, totalHunks, 30)
		return fmt.Sprintf("Hunks: %s %d/%d (%d%%)\n\n", bar, totalMarked, totalHunks, pct)
	}
	return "Hunks: no reviewable hunks\n\n"
}

func renderShowFiles(files []fileSummary) string {
	var b strings.Builder
	b.WriteString("Files:\n")
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, f := range files {
		hunkStr := ""
		if f.Hunks > 0 {
			hunkStr = fmt.Sprintf("  %d/%d hunks", f.Marked, f.Hunks)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t+%d -%d\n", f.Glyph, f.Path, hunkStr, f.Additions, f.Deletions)
	}
	tw.Flush()
	b.WriteString("\n")
	return b.String()
}

func renderShowNotes(notes []brain.Note) string {
	if len(notes) == 0 {
		return "Notes: none\n"
	}
	published, local := 0, 0
	for _, n := range notes {
		if n.GitHubCommentID != 0 {
			published++
		} else {
			local++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Notes: %d active (%d published, %d local)\n", len(notes), published, local)
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, n := range notes {
		source := n.Source
		if source == "" {
			source = "human"
		}
		publishedMark := ""
		if n.GitHubCommentID != 0 {
			publishedMark = " [published]"
		}
		fmt.Fprintf(tw, "  #%d\t%s:%d\t(%s)%s\n", n.ID, n.Path, n.LineNo, source, publishedMark)
		for _, bl := range splitLines(n.Body) {
			fmt.Fprintf(tw, "    \t\t%s\n", truncate(bl, 80))
		}
	}
	tw.Flush()
	return b.String()
}

// aggregateHunks sums up hunk counts across all file summaries.
func aggregateHunks(files []fileSummary) (total, marked, unseen int) {
	for _, f := range files {
		total += f.Hunks
		marked += f.Marked
		if f.Glyph == " " {
			unseen++
		}
	}
	return
}

func progressBar(done, total, width int) string {
	if total == 0 {
		return "[" + strings.Repeat("-", width) + "]"
	}
	filled := done * width / total
	bar := strings.Repeat("=", filled) + strings.Repeat("-", width-filled)
	return "[" + bar + "]"
}

func splitLines(s string) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

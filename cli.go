package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

func runCLI(args []string) error {
	switch args[0] {
	case "notes":
		return cmdNotes(args[1:])
	case "todo":
		return cmdTodo(args[1:])
	case "state":
		return cmdState(args[1:])
	case "mark":
		return cmdMark(args[1:], true)
	case "unmark":
		return cmdMark(args[1:], false)
	case "note":
		return cmdNote(args[1:])
	case "resolve":
		return cmdResolve(args[1:])
	case "brain":
		return cmdBrain(args[1:])
	case "log":
		return cmdLog(args[1:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

// splitFlags partitions args into flags (anything starting with -) and positional.
// This lets users pass flags before OR after positional args, which Go's flag
// package doesn't do by default.
func splitFlags(args []string) (flags, positional []string) {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
		} else {
			positional = append(positional, a)
		}
	}
	return
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `rhodium — code review TUI (run with no args) and CLI

Usage:
  rhodium                                           launch the TUI
  rhodium notes <owner/repo#N>                      print notes for a PR
  rhodium todo                                      global dashboard (catch-up, unseen, notes)
  rhodium state <owner/repo#N>                      print full review state (files, hunks, marks, notes)
  rhodium mark <owner/repo#N> <file> <hunk-hash>    mark a hunk as reviewed
  rhodium unmark <owner/repo#N> <file> <hunk-hash>  unmark a hunk
  rhodium note <owner/repo#N> <file> <line> <body>  add a note (body "-" reads from stdin)
  rhodium resolve <note-id>...                      mark one or more notes resolved
  rhodium brain status                              inspect the brain db (path, schema version, pending migrations)
  rhodium brain log [--pr ref] [--kind p] [--limit N]  print the brain mutation log, newest first
  rhodium log <owner/repo#N> [--verbose]            per-commit review overlay for a PR

Flags:
  --json     emit JSON (notes, todo, state, brain log, log)
  --sync     (todo only) refresh the PR cache from GitHub before printing
  --all      (notes only) include resolved notes
  --pr       (brain log) filter to one PR (owner/repo#N)
  --kind     (brain log) filter by kind prefix (mark., note., session., ...)
  --limit    (brain log) max events to return (default 100)
  --verbose  (log) show per-file breakdown under each commit`)
}

var prRefRE = regexp.MustCompile(`^([^/]+/[^/#]+)[#/](\d+)$`)

func parsePRRef(s string) (repo string, number int, err error) {
	m := prRefRE.FindStringSubmatch(s)
	if m == nil {
		return "", 0, fmt.Errorf("bad PR ref %q — expected owner/repo#123 or owner/repo/123", s)
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return "", 0, err
	}
	return m[1], n, nil
}

func cmdBrain(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: rhodium brain {status|log}")
	}
	switch args[0] {
	case "status":
		return cmdBrainStatus(args[1:])
	case "log":
		return cmdBrainLog(args[1:])
	default:
		return fmt.Errorf("unknown brain subcommand: %s (try 'status' or 'log')", args[0])
	}
}

func cmdBrainStatus(args []string) error {
	flags, _ := splitFlags(args)
	fs := flag.NewFlagSet("brain status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(flags); err != nil {
		return err
	}
	status, err := InspectBrain()
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(status)
	}
	fmt.Printf("path:      %s\n", status.Path)
	if !status.Exists {
		fmt.Println("status:    (no database file — will be created on first use)")
		fmt.Printf("embedded:  %d migrations (latest v%d)\n", status.EmbeddedCount, status.MaxEmbedded)
		return nil
	}
	fmt.Printf("version:   %d\n", status.CurrentVersion)
	fmt.Printf("embedded:  %d migrations (latest v%d)\n", status.EmbeddedCount, status.MaxEmbedded)
	fmt.Printf("pending:   %d\n", status.Pending)
	if status.Ahead {
		fmt.Println("WARNING:   database is AHEAD of this binary — upgrade rhodium")
	}
	if len(status.HashMismatches) > 0 {
		fmt.Println("WARNING:   migration file content changed since apply:")
		for _, m := range status.HashMismatches {
			fmt.Printf("             v%d %s\n", m.Version, m.File)
		}
	}
	if len(status.Migrations) > 0 {
		fmt.Println("migrations:")
		for _, m := range status.Migrations {
			marker := "applied"
			if m.Pending {
				marker = "pending"
			}
			file := m.File
			if file == "" {
				file = "(no file)"
			}
			fmt.Printf("  v%-4d  %-10s  %s\n", m.Version, marker, file)
		}
	}
	if len(status.Backups) > 0 {
		fmt.Println("backups:")
		for _, b := range status.Backups {
			fmt.Printf("  %s\n", b)
		}
	}
	return nil
}

// logJSONEvent is the on-wire shape for `brain log --json`: the stored
// payload is unmarshalled into json.RawMessage so downstream consumers
// (a future `brain replay`) get a real JSON object, not a string.
type logJSONEvent struct {
	ID      int64           `json:"id"`
	TS      string          `json:"ts"`
	Kind    string          `json:"kind"`
	PRKey   string          `json:"pr_key,omitempty"`
	Path    string          `json:"path,omitempty"`
	Payload json.RawMessage `json:"payload"`
}

// cmdBrainLog prints the append-only brain_events log, newest first.
// Filters (--pr, --kind) narrow the result server-side; --limit caps the
// page size (RecentEvents default is 100). --json emits JSONL suitable
// for piping into a future `brain replay`.
func cmdBrainLog(args []string) error {
	// Parse args directly — splitFlags mis-handles value-taking flags
	// like --limit 20, and this subcommand has no positional args.
	fs := flag.NewFlagSet("brain log", flag.ContinueOnError)
	prRef := fs.String("pr", "", "filter to one PR (owner/repo#N)")
	kind := fs.String("kind", "", "filter by kind prefix (e.g. mark., note., session.)")
	limit := fs.Int("limit", 100, "max events to return")
	asJSON := fs.Bool("json", false, "emit JSONL")
	if err := fs.Parse(args); err != nil {
		return err
	}

	filter := EventFilter{KindPrefix: *kind, Limit: *limit}
	if *prRef != "" {
		repo, num, err := parsePRRef(*prRef)
		if err != nil {
			return err
		}
		filter.PRKey = prKey(repo, num)
	}

	brain, err := LoadBrain()
	if err != nil {
		return err
	}
	defer brain.Close()

	events := brain.RecentEvents(filter)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, e := range events {
			raw := json.RawMessage(e.Payload)
			if len(raw) == 0 {
				raw = json.RawMessage("{}")
			}
			if err := enc.Encode(logJSONEvent{
				ID: e.ID, TS: e.TS, Kind: e.Kind,
				PRKey: e.PRKey, Path: e.Path, Payload: raw,
			}); err != nil {
				return err
			}
		}
		return nil
	}

	if len(events) == 0 {
		fmt.Println("brain log: no events")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, e := range events {
		payload := compactJSON(e.Payload)
		fmt.Fprintf(tw, "#%d\t%s\t%s\t%s\t%s\t%s\n",
			e.ID, e.TS, e.Kind, e.PRKey, e.Path, payload)
	}
	return tw.Flush()
}

// compactJSON re-serializes a stored payload without whitespace. Stored
// payloads are already produced by json.Marshal and therefore compact,
// but a manual re-marshal keeps us robust to future hand-written rows
// and normalizes field ordering for readable log output.
func compactJSON(raw string) string {
	if raw == "" {
		return "{}"
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	buf, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return string(buf)
}

// cmdNotes prints notes for a single PR.
func cmdNotes(args []string) error {
	flags, pos := splitFlags(args)
	fs := flag.NewFlagSet("notes", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	all := fs.Bool("all", false, "include resolved notes")
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: rhodium notes <owner/repo#N>")
	}
	repo, num, err := parsePRRef(pos[0])
	if err != nil {
		return err
	}
	brain, err := LoadBrain()
	if err != nil {
		return err
	}
	defer brain.Close()

	filter := NotesActive
	if *all {
		filter = NotesAll
	}
	notes := brain.NotesForPR(repo, num, filter)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(notes)
	}

	if len(notes) == 0 {
		fmt.Printf("%s — no notes\n", prKey(repo, num))
		return nil
	}
	fmt.Printf("%s — %d %s\n\n", prKey(repo, num), len(notes), pluralize("note", len(notes)))
	var curPath string
	for _, n := range notes {
		if n.Path != curPath {
			if curPath != "" {
				fmt.Println()
			}
			fmt.Println(n.Path)
			curPath = n.Path
		}
		marker := ""
		if n.ResolvedAt != "" {
			marker = " ✓ resolved " + n.ResolvedAt
		}
		fmt.Printf("  [#%d] line %d  (%s)%s\n", n.ID, n.LineNo, n.CreatedAt, marker)
		for _, bl := range strings.Split(strings.TrimRight(n.Body, "\n"), "\n") {
			fmt.Printf("    %s\n", bl)
		}
	}
	return nil
}

// cmdResolve marks one or more notes as resolved by ID.
func cmdResolve(args []string) error {
	_, pos := splitFlags(args)
	if len(pos) == 0 {
		return fmt.Errorf("usage: rhodium resolve <note-id>...")
	}
	brain, err := LoadBrain()
	if err != nil {
		return err
	}
	defer brain.Close()

	for _, s := range pos {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("note id must be an integer: %q", s)
		}
		if err := brain.ResolveNote(id); err != nil {
			return fmt.Errorf("resolve #%d: %w", id, err)
		}
		fmt.Printf("resolved #%d\n", id)
	}
	return nil
}

// prTodoItem is one PR's row in the todo dashboard.
type prTodoItem struct {
	Key     string   `json:"key"`
	Repo    string   `json:"repo"`
	Number  int      `json:"number"`
	Title   string   `json:"title"`
	Author  string   `json:"author"`
	Tags    []string `json:"tags"`
	Notes   int      `json:"notes,omitempty"`
	CatchUp *struct {
		Done  int `json:"done"`
		Total int `json:"total"`
	} `json:"catch_up,omitempty"`
}

type todoOutput struct {
	PRs []prTodoItem `json:"prs"`
}

// cmdTodo prints a global dashboard of PRs with outstanding review work.
func cmdTodo(args []string) error {
	flags, _ := splitFlags(args)
	fs := flag.NewFlagSet("todo", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	sync := fs.Bool("sync", false, "refresh PR cache from GitHub first")
	if err := fs.Parse(flags); err != nil {
		return err
	}

	brain, err := LoadBrain()
	if err != nil {
		return err
	}
	defer brain.Close()

	if *sync {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		var all []PR
		for _, repo := range cfg.Repos {
			prs, err := listPRs(repo)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warn: %s: %v\n", repo, err)
				continue
			}
			all = append(all, prs...)
		}
		if err := brain.SetPRCache(all); err != nil {
			return fmt.Errorf("write cache: %w", err)
		}
	}

	cached := brain.CachedPRs()
	byKey := map[string]PR{}
	for _, p := range cached {
		byKey[prKey(p.Repo, p.Number)] = p
	}

	catchUps := map[string]*ReviewSession{}
	sessions := brain.AllActiveSessions()
	for i := range sessions {
		catchUps[sessions[i].PRKey] = &sessions[i]
	}

	// Union of all pr_keys with outstanding state — cached PRs plus anything
	// that has notes or an active catch-up (so closed / out-of-window PRs
	// with unresolved notes still surface).
	keys := map[string]bool{}
	for k := range byKey {
		keys[k] = true
	}
	for k := range catchUps {
		keys[k] = true
	}
	for _, k := range brain.PRKeysWithNotes() {
		keys[k] = true
	}

	var items []prTodoItem
	for key := range keys {
		repo, num, err := parsePRRef(key)
		if err != nil {
			continue
		}
		notes := brain.NoteCountForPR(repo, num)
		cu := catchUps[key]
		_, inCache := byKey[key]
		reviewed := len(brain.AllFileReviewedStates(repo, num)) > 0 || brain.HasAnyMarks(repo, num)

		var tags []string
		if cu != nil {
			tags = append(tags, "catch-up")
		}
		if inCache && !reviewed && cu == nil {
			tags = append(tags, "unseen")
		}
		if notes > 0 {
			tags = append(tags, "notes")
		}
		if len(tags) == 0 {
			continue
		}
		p := byKey[key]
		item := prTodoItem{
			Key: key, Repo: repo, Number: num,
			Title: p.Title, Author: p.Author, Tags: tags, Notes: notes,
		}
		if cu != nil {
			item.CatchUp = &struct {
				Done  int `json:"done"`
				Total int `json:"total"`
			}{Done: cu.FilesDone, Total: cu.FilesTotal}
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(todoOutput{PRs: items})
	}

	if len(items) == 0 {
		fmt.Println("todo: nothing pending. (run with --sync to refresh the PR cache)")
		return nil
	}

	fmt.Printf("%d %s need attention\n\n", len(items), pluralize("PR", len(items)))
	for _, it := range items {
		var suffix []string
		if it.CatchUp != nil {
			suffix = append(suffix, fmt.Sprintf("catch-up %d/%d", it.CatchUp.Done, it.CatchUp.Total))
		}
		if contains(it.Tags, "unseen") {
			suffix = append(suffix, "unseen")
		}
		if it.Notes > 0 {
			suffix = append(suffix, fmt.Sprintf("%d %s", it.Notes, pluralize("note", it.Notes)))
		}
		mid := truncate(it.Title, 40)
		if it.Author != "" {
			mid = fmt.Sprintf("%-40s  by %s", mid, it.Author)
		}
		fmt.Printf("  %-28s  %s  [%s]\n", it.Key, mid, strings.Join(suffix, ", "))
	}
	if !*sync {
		fmt.Println("\n(reading cache — use --sync to refresh from GitHub)")
	}
	return nil
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func pluralize(word string, n int) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// --- state / mark / note — CLI surface consumed by the nvim plugin ---

type stateHunk struct {
	Hash    string `json:"hash"`
	Header  string `json:"header"`
	OldLine int    `json:"old_line"`
	NewLine int    `json:"new_line"`
	Marked  bool   `json:"marked"`
}

type stateFile struct {
	Path      string      `json:"path"`
	Status    string      `json:"status"` // unseen | partial | seen
	Additions int         `json:"additions"`
	Deletions int         `json:"deletions"`
	Patch     string      `json:"patch"`
	Hunks     []stateHunk `json:"hunks"`
	Notes     []Note      `json:"notes"`
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

func statusName(s FileStatus) string {
	switch s {
	case StatusSeen:
		return "seen"
	case StatusPartial:
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

	brain, err := LoadBrain()
	if err != nil {
		return err
	}
	defer brain.Close()

	files, err := listPRFiles(repo, num)
	if err != nil {
		return err
	}

	out := stateOutput{
		Key:    prKey(repo, num),
		Repo:   repo,
		Number: num,
	}
	for _, p := range brain.CachedPRs() {
		if p.Repo == repo && p.Number == num {
			out.Title = p.Title
			out.Author = p.Author
			out.HeadSHA = p.HeadSHA
			out.BaseSHA = p.BaseSHA
			break
		}
	}

	for _, fc := range files {
		marks := brain.HunkMarks(repo, num, fc.Path)
		hunks := parseHunks(fc.Patch)
		sh := make([]stateHunk, 0, len(hunks))
		for _, h := range hunks {
			oldL, newL := hunkLines(h.Header)
			sh = append(sh, stateHunk{
				Hash:    h.Hash,
				Header:  h.Header,
				OldLine: oldL,
				NewLine: newL,
				Marked:  marks[h.Hash],
			})
		}
		out.Files = append(out.Files, stateFile{
			Path:      fc.Path,
			Status:    statusName(brain.Status(repo, num, fc)),
			Additions: fc.Additions,
			Deletions: fc.Deletions,
			Patch:     fc.Patch,
			Hunks:     sh,
			Notes:     brain.NotesForFile(repo, num, fc.Path),
		})
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// cmdMark flips a single hunk mark on (on=true) or off (on=false).
func cmdMark(args []string, on bool) error {
	verb := "mark"
	if !on {
		verb = "unmark"
	}
	_, pos := splitFlags(args)
	if len(pos) != 3 {
		return fmt.Errorf("usage: rhodium %s <owner/repo#N> <file> <hunk-hash>", verb)
	}
	repo, num, err := parsePRRef(pos[0])
	if err != nil {
		return err
	}
	path, hash := pos[1], pos[2]

	brain, err := LoadBrain()
	if err != nil {
		return err
	}
	defer brain.Close()

	marks := brain.HunkMarks(repo, num, path)
	if on {
		marks[hash] = true
	} else {
		delete(marks, hash)
	}
	if err := brain.SetHunkMarks(repo, num, path, marks); err != nil {
		return err
	}

	// Record the head/base SHAs the reviewer is looking at, so catch-up works
	// consistently whether the mark came from the TUI or nvim.
	for _, p := range brain.CachedPRs() {
		if p.Repo == repo && p.Number == num {
			_ = brain.SetFileReviewed(repo, num, path, p.HeadSHA, p.BaseSHA)
			break
		}
	}
	return nil
}

// cmdNote saves a note for a specific line. Body read from the positional arg,
// or from stdin when body == "-". Line hash is computed here from the file
// content at that line, so nvim doesn't need to duplicate the hashing.
func cmdNote(args []string) error {
	_, pos := splitFlags(args)
	if len(pos) != 4 {
		return fmt.Errorf("usage: rhodium note <owner/repo#N> <file> <line> <body|->")
	}
	repo, num, err := parsePRRef(pos[0])
	if err != nil {
		return err
	}
	path := pos[1]
	lineNo, err := strconv.Atoi(pos[2])
	if err != nil {
		return fmt.Errorf("line must be an integer: %w", err)
	}
	body := pos[3]
	if body == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		body = strings.TrimRight(string(data), "\n")
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("empty note body")
	}

	brain, err := LoadBrain()
	if err != nil {
		return err
	}
	defer brain.Close()

	// Compute line hash from the file at head. If we can't fetch (e.g. offline,
	// new file), fall back to an empty hash — note is still anchored by line
	// number and the drift detector will warn later.
	var lineHash string
	for _, p := range brain.CachedPRs() {
		if p.Repo == repo && p.Number == num {
			if content, err := fetchFileAtRef(repo, path, p.HeadSHA); err == nil && content != "" {
				lines := strings.Split(content, "\n")
				if lineNo >= 1 && lineNo <= len(lines) {
					lineHash = hashLine(lines[lineNo-1])
				}
			}
			break
		}
	}
	return brain.SaveNote(repo, num, path, lineNo, lineHash, body)
}

// --- rhodium log: per-commit review overlay -------------------------------

// commitFileStatus is the per-file breakdown within a single commit:
// how many hunks that commit introduced for this file are currently
// marked in the PR-level brain. Used for --verbose output and JSON.
type commitFileStatus struct {
	Path   string `json:"path"`
	Marked int    `json:"marked"`
	Total  int    `json:"total"`
}

// commitStatus is one commit's aggregate review state for rhodium log.
// Marked / Total are sums across Files. The caveat lives here: a commit
// whose hunks were rewritten by later commits in the same PR will show
// Marked=0 even if the net effect has been reviewed — marks are keyed
// on +/- content hash, so the rewritten version hashes differently from
// the original. See 2026-04-21-brain-events-log.md for the longer
// discussion of why we accept this.
type commitStatus struct {
	SHA     string             `json:"sha"`
	Title   string             `json:"title"`
	Author  string             `json:"author"`
	Date    string             `json:"date"`
	Message string             `json:"message,omitempty"`
	Marked  int                `json:"marked"`
	Total   int                `json:"total"`
	Files   []commitFileStatus `json:"files"`
}

// overlayCommitStatus is the pure core of rhodium log: given the files a
// commit introduced and the PR's current marks map (path → set of marked
// hunk hashes), return the commit's review aggregate. Kept free of
// network / DB so it can be exercised in unit tests.
//
// Marks are matched by the same +/- content hash the brain uses. Hunks
// whose content no longer matches a final-PR hunk (e.g. the commit was
// later rewritten) simply don't intersect with any mark — such a commit
// will read as 0/N even if its net effect was reviewed through a
// different hunk further down the history. That's the documented
// approximation; it's the right tradeoff because any more precise
// answer requires commit-SHA-keyed state that breaks under rebase.
func overlayCommitStatus(c Commit, files []FileChange, marksByPath map[string]map[string]bool) commitStatus {
	out := commitStatus{
		SHA:     c.SHA,
		Title:   c.Title,
		Author:  c.Author,
		Date:    c.Date,
		Message: c.Message,
	}
	for _, f := range files {
		hunks := parseHunks(f.Patch)
		if len(hunks) == 0 {
			continue
		}
		fileMarks := marksByPath[f.Path]
		marked := 0
		for _, h := range hunks {
			if fileMarks[h.Hash] {
				marked++
			}
		}
		out.Files = append(out.Files, commitFileStatus{
			Path: f.Path, Marked: marked, Total: len(hunks),
		})
		out.Marked += marked
		out.Total += len(hunks)
	}
	return out
}

// commitStatusGlyph picks the familiar ✓/◐/blank glyph for a commit's
// aggregate status, matching FileStatus.Glyph() so log lines visually
// line up with todo / state output.
func commitStatusGlyph(s commitStatus) string {
	if s.Total == 0 {
		// Merge commits and empty patches — nothing reviewable; leave blank.
		return " "
	}
	switch {
	case s.Marked == 0:
		return " "
	case s.Marked == s.Total:
		return "✓"
	default:
		return "◐"
	}
}

// cmdLog prints the PR's commits with per-commit review overlay.
// Shells out to gh for commit + file data, joins against hunk_marks
// via overlayCommitStatus, and renders either a tab-aligned table
// (default), a verbose form with per-file lines, or JSON.
func cmdLog(args []string) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	verbose := fs.Bool("verbose", false, "show per-file breakdown under each commit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) != 1 {
		return fmt.Errorf("usage: rhodium log <owner/repo#N>")
	}
	repo, num, err := parsePRRef(pos[0])
	if err != nil {
		return err
	}

	brain, err := LoadBrain()
	if err != nil {
		return err
	}
	defer brain.Close()

	commits, err := listPRCommits(repo, num)
	if err != nil {
		return err
	}
	if len(commits) == 0 {
		fmt.Println("log: no commits")
		return nil
	}

	// Build the PR-level marks map once, keyed by path. overlayCommitStatus
	// reads this without a brain round-trip per commit.
	prFiles, err := listPRFiles(repo, num)
	if err != nil {
		return err
	}
	marksByPath := make(map[string]map[string]bool, len(prFiles))
	for _, f := range prFiles {
		marksByPath[f.Path] = brain.HunkMarks(repo, num, f.Path)
	}

	// Fetch per-commit files in input order, overlay each. A parallel fan-out
	// is the obvious optimization but this is a CLI surface — keep it simple
	// until someone complains.
	statuses := make([]commitStatus, 0, len(commits))
	for _, c := range commits {
		files, err := fetchCommitFiles(repo, c.SHA)
		if err != nil {
			return err
		}
		statuses = append(statuses, overlayCommitStatus(c, files, marksByPath))
	}

	// Newest first — GitHub returns oldest first, reverse for git-log parity.
	for i, j := 0, len(statuses)-1; i < j; i, j = i+1, j-1 {
		statuses[i], statuses[j] = statuses[j], statuses[i]
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Key     string         `json:"key"`
			Commits []commitStatus `json:"commits"`
		}{Key: prKey(repo, num), Commits: statuses})
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, s := range statuses {
		ratio := fmt.Sprintf("%d/%d", s.Marked, s.Total)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			shortSHA(s.SHA),
			commitStatusGlyph(s),
			ratio,
			truncate(s.Title, 50),
			s.Author,
			humanizeTime(s.Date),
		)
		if *verbose {
			for _, f := range s.Files {
				gl := " "
				switch {
				case f.Total == 0 || f.Marked == 0:
					gl = " "
				case f.Marked == f.Total:
					gl = "✓"
				default:
					gl = "◐"
				}
				fmt.Fprintf(tw, "\t\t\t    %s  %d/%d\t%s\t\n", gl, f.Marked, f.Total, f.Path)
			}
		}
	}
	return tw.Flush()
}

// humanizeTime formats an ISO8601 timestamp as a coarse relative string
// for list views. Falls back to the input when parsing fails — CLI
// output should never lose information just because a date field is
// unfamiliar. Thresholds are loose on purpose: these are glance-values,
// not accurate ones. Anything older than a month reverts to YYYY-MM-DD.
func humanizeTime(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

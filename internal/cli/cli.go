// Package cli holds the CLI surface for rhodium — the subcommands invoked
// when the binary is given any arguments. The TUI lives in
// internal/rhodium and is launched when no arguments are passed.
//
// One file per subcommand; this file is the dispatcher plus the few
// helpers shared across more than one command.
package cli

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Run dispatches the given argv (without argv[0]) to the matching
// subcommand. Unknown commands print the usage and return an error.
func Run(args []string) error {
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

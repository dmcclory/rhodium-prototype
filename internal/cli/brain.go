package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"rhodium/internal/brain"
)

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
	status, err := brain.InspectBrain()
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

	filter := brain.EventFilter{KindPrefix: *kind, Limit: *limit}
	if *prRef != "" {
		repo, num, err := parsePRRef(*prRef)
		if err != nil {
			return err
		}
		filter.PRKey = brain.PRKey(repo, num)
	}

	b, err := brain.LoadBrain()
	if err != nil {
		return err
	}
	defer b.Close()

	events := b.RecentEvents(filter)

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

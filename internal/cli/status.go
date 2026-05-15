package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"rhodium/internal/brain"
)

// cmdStatus dispatches between sub-commands: set, clear, list.
func cmdStatus(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: rhodium status <owner/repo#N> <status>  |  status clear <owner/repo#N>  |  status list [--json]")
	}

	switch args[0] {
	case "list":
		return cmdStatusList(args[1:])
	case "clear":
		return cmdStatusClear(args[1:])
	default:
		return cmdStatusSet(args)
	}
}

// cmdStatusSet sets a custom status on a PR.
func cmdStatusSet(args []string) error {
	flags, pos := splitFlags(args)
	fs := flag.NewFlagSet("status set", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(flags); err != nil {
		return err
	}

	if len(pos) < 2 {
		return fmt.Errorf("usage: rhodium status <owner/repo#N> <status>")
	}
	repo, num, err := parsePRRef(pos[0])
	if err != nil {
		return err
	}
	status := pos[1]

	b, err := brain.OpenForCLI()
	if err != nil {
		return err
	}
	defer b.Close()

	if err := b.SetPRStatus(repo, num, status); err != nil {
		return err
	}

	if *asJSON {
		out := struct {
			PRKey  string `json:"pr_key"`
			Status string `json:"status"`
		}{PRKey: brain.PRKey(repo, num), Status: status}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("%s#%d → %s\n", repo, num, status)
	return nil
}

// cmdStatusClear clears the status on a PR.
func cmdStatusClear(args []string) error {
	flags, pos := splitFlags(args)
	fs := flag.NewFlagSet("status clear", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(flags); err != nil {
		return err
	}

	if len(pos) != 1 {
		return fmt.Errorf("usage: rhodium status clear <owner/repo#N>")
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

	prev := b.PRStatus(repo, num)
	if err := b.SetPRStatus(repo, num, ""); err != nil {
		return err
	}

	if *asJSON {
		out := struct {
			PRKey       string `json:"pr_key"`
			PrevStatus  string `json:"prev_status"`
		}{PRKey: brain.PRKey(repo, num), PrevStatus: prev}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("%s#%d: status cleared (was %q)\n", repo, num, prev)
	return nil
}

// cmdStatusList lists all PRs with custom statuses.
func cmdStatusList(args []string) error {
	flags, _ := splitFlags(args)
	fs := flag.NewFlagSet("status list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(flags); err != nil {
		return err
	}

	b, err := brain.OpenForCLI()
	if err != nil {
		return err
	}
	defer b.Close()

	entries := b.AllPRStatuses()

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}

	if len(entries) == 0 {
		fmt.Println("status: no custom statuses set")
		return nil
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].PRKey < entries[j].PRKey })
	for _, e := range entries {
		fmt.Printf("  %-36s  %s  (%s)\n", e.PRKey, e.Status, e.SetAt)
	}
	return nil
}

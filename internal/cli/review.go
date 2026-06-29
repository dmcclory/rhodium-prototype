package cli

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"rhodium/internal/brain"
	"rhodium/internal/gh"
	"rhodium/internal/rhodium"
)

// cmdReview runs an on-demand review agent against a PR.
func cmdReview(args []string) error {
	flags, pos := splitFlags(args, "agent")
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	firstPass := fs.Bool("first-pass", false, "run the first-pass review agent")
	if err := fs.Parse(flags); err != nil {
		return err
	}

	if len(pos) != 1 {
		return fmt.Errorf("usage: rhodium review --first-pass <owner/repo#N>")
	}
	if !*firstPass {
		return fmt.Errorf("usage: rhodium review --first-pass <owner/repo#N>")
	}

	repo, num, err := parsePRRef(pos[0])
	if err != nil {
		return err
	}

	cfg, err := rhodium.LoadConfig()
	if err != nil {
		return err
	}

	b, err := brain.OpenForCLI()
	if err != nil {
		return err
	}
	defer b.Close()

	// Find the first-pass action.
	var action rhodium.Action
	for _, a := range cfg.ActionsResolved() {
		if a.Key == "f" && a.Mode == "oneshot" {
			action = a
			break
		}
	}
	if action.Name == "" {
		// Fall back to the default first-pass action if user has custom actions
		// but no "f" key.
		for _, a := range cfg.ActionsResolved() {
			if a.Name == "first-pass" && a.Mode == "oneshot" {
				action = a
				break
			}
		}
	}
	if action.Name == "" {
		return fmt.Errorf("no first-pass action configured — ensure your config has a oneshot action with key 'f' or name 'first-pass'")
	}
	if action.Delivery != "inline-notes" {
		return fmt.Errorf("first-pass action must use delivery 'inline-notes', got %q", action.Delivery)
	}

	// Resolve the default agent.
	agent := cfg.DefaultAgentResolved()
	if agent.Command == "" {
		return fmt.Errorf("no default agent configured")
	}

	// Fetch PR files with patches from GitHub.
	files, err := gh.ListPRFiles(repo, num)
	if err != nil {
		return fmt.Errorf("fetching PR files: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("PR %s#%d has no changed files", repo, num)
	}

	// Build the PR info for the prompt context.
	// Try to get it from the cache first; fall back to minimal data.
	pr := gh.PR{Repo: repo, Number: num}
	for _, p := range b.CachedPRs() {
		if p.Repo == repo && p.Number == num {
			pr = p
			break
		}
	}
	// Fill in head/base SHAs from the first file's blob if we couldn't get them from cache.
	if pr.HeadSHA == "" && len(files) > 0 {
		pr.HeadSHA = files[0].Blob
	}

	// Build prompt context and render the template.
	ctx := rhodium.BuildPromptCtx(pr, files, "", cfg.BaseBranch(repo))
	prompt, err := rhodium.RenderPrompt(action, ctx)
	if err != nil {
		return err
	}

	// Run the agent.
	fmt.Fprintf(os.Stderr, "running %s first-pass review on %s#%d…\n", agent.Name, repo, num)

	cmd := exec.Command(agent.Command, agent.OneshotArgs...)
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		rhodium.StashAgentOutput(pr, action.Name, stdout.Bytes(), stderr.Bytes())
		return fmt.Errorf("%s %v: %w (stderr: %s)", agent.Command, agent.OneshotArgs, err, strings.TrimSpace(stderr.String()))
	}

	// Parse agent output.
	if stdout.Len() == 0 {
		rhodium.StashAgentOutput(pr, action.Name, nil, stderr.Bytes())
		return fmt.Errorf("agent produced no output — check that %s is configured for non-interactive mode (oneshot_args like -p or --prompt)", agent.Name)
	}

	notes, err := rhodium.ParseAgentNotes(stdout.Bytes())
	if err != nil {
		rhodium.StashAgentOutput(pr, action.Name, stdout.Bytes(), stderr.Bytes())
		return fmt.Errorf("parse agent output: %w", err)
	}

	// Save notes to the brain.
	saved := 0
	for _, n := range notes {
		if err := b.SaveAgentNote(repo, num, n.Path, n.Line, n.Body, pr.BaseSHA); err != nil {
			fmt.Fprintf(os.Stderr, "warn: saving note on %s:%d: %v\n", n.Path, n.Line, err)
			continue
		}
		saved++
	}

	if saved == 0 {
		fmt.Printf("%s#%d: no issues found\n", repo, num)
	} else {
		fmt.Printf("%s#%d: %d agent %s saved\n", repo, num, saved, pluralize("note", saved))
	}

	// Raw output is always stashed for reference.
	rhodium.StashAgentOutput(pr, action.Name, stdout.Bytes(), stderr.Bytes())
	return nil
}

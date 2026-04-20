package main

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// PromptCtx is the data model passed into an action's prompt template. Fields
// are named for direct use as {{.Title}}, {{.FileList}}, etc.
type PromptCtx struct {
	Repo     string // "owner/name"
	Number   int
	Title    string
	Author   string
	Body     string
	HeadSHA  string
	BaseSHA  string
	Worktree string // absolute path; empty when the action doesn't use a worktree
	FileList string // one line per file: "path  +A -D"
	Patches  string // concatenated unified diffs, one file after another
}

// buildPromptCtx assembles a PromptCtx from a PR + its file list. Worktree is
// passed in because non-worktree actions resolve it as "" and some do.
func buildPromptCtx(pr PR, files []FileChange, worktree string) PromptCtx {
	return PromptCtx{
		Repo:     pr.Repo,
		Number:   pr.Number,
		Title:    pr.Title,
		Author:   pr.Author,
		Body:     pr.Body,
		HeadSHA:  pr.HeadSHA,
		BaseSHA:  pr.BaseSHA,
		Worktree: worktree,
		FileList: renderFileList(files),
		Patches:  renderPatches(files),
	}
}

func renderFileList(files []FileChange) string {
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "%s  +%d -%d\n", f.Path, f.Additions, f.Deletions)
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderPatches concatenates per-file unified diffs with a header line so the
// agent can tell where one file ends and the next begins. Files with empty
// patches (binary, too-large) are listed but marked as such.
func renderPatches(files []FileChange) string {
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "diff --- %s  (+%d -%d)\n", f.Path, f.Additions, f.Deletions)
		if f.Patch == "" {
			b.WriteString("(no patch available — binary or too large)\n")
		} else {
			b.WriteString(f.Patch)
			if !strings.HasSuffix(f.Patch, "\n") {
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderPrompt executes the action's prompt template against ctx. Errors
// surface to the status bar — we don't fall back to a default template
// because silent substitution would hide user config mistakes.
func renderPrompt(action Action, ctx PromptCtx) (string, error) {
	tmpl, err := template.New(action.Name).Parse(action.PromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parse prompt template for action %q: %w", action.Name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("render prompt template for action %q: %w", action.Name, err)
	}
	return buf.String(), nil
}

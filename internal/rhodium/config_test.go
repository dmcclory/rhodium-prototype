package rhodium

import (
	"strings"
	"testing"
)

// chatTemplate returns the built-in "chat" action's prompt template.
func chatTemplate(t *testing.T) string {
	t.Helper()
	for _, a := range defaultActions() {
		if a.Name == "chat" {
			return a.PromptTemplate
		}
	}
	t.Fatal("no built-in chat action")
	return ""
}

func TestPromptTemplateBaseBranchClause(t *testing.T) {
	tests := []struct {
		base         string
		wantContains string
		wantNoWarn   bool // expect the "do NOT compare against main/master" clause omitted
	}{
		{"develop", `git diff develop...HEAD`, false},
		{"main", `git diff main...HEAD`, true},
		{"master", `git diff master...HEAD`, true},
	}
	for _, tt := range tests {
		out, err := RenderPrompt(
			Action{Name: "chat", PromptTemplate: chatTemplate(t)},
			PromptCtx{Repo: "o/r", Number: 1, BaseBranch: tt.base},
		)
		if err != nil {
			t.Fatalf("base %q: render: %v", tt.base, err)
		}
		if !strings.Contains(out, tt.wantContains) {
			t.Errorf("base %q: missing %q in:\n%s", tt.base, tt.wantContains, out)
		}
		hasWarn := strings.Contains(out, "do NOT compare against main/master")
		if tt.wantNoWarn && hasWarn {
			t.Errorf("base %q: warn clause should be omitted but was present", tt.base)
		}
		if !tt.wantNoWarn && !hasWarn {
			t.Errorf("base %q: warn clause should be present but was omitted", tt.base)
		}
	}

	// No base branch → no clause at all.
	out, err := RenderPrompt(
		Action{Name: "chat", PromptTemplate: chatTemplate(t)},
		PromptCtx{Repo: "o/r", Number: 1},
	)
	if err != nil {
		t.Fatalf("empty base: render: %v", err)
	}
	if strings.Contains(out, "This PR targets") {
		t.Errorf("empty base: clause should be absent:\n%s", out)
	}
}

func TestDefaultPRViewResolved(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "files"},
		{"files", "files"},
		{"commits", "commits"},
		{"bogus", "files"},
	}
	for _, tt := range tests {
		c := &Config{DefaultPRView: tt.in}
		if got := c.DefaultPRViewResolved(); got != tt.want {
			t.Errorf("DefaultPRViewResolved(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBaseBranch(t *testing.T) {
	tests := []struct {
		name       string
		defaultBr  string
		repoBranch map[string]string
		repo       string
		want       string
	}{
		{"unset falls back to empty", "", nil, "owner/repo", ""},
		{"global default applies", "develop", nil, "owner/repo", "develop"},
		{"per-repo overrides global", "main", map[string]string{"owner/repo": "develop"}, "owner/repo", "develop"},
		{"per-repo miss uses global", "main", map[string]string{"other/repo": "develop"}, "owner/repo", "main"},
		{"empty per-repo entry uses global", "main", map[string]string{"owner/repo": ""}, "owner/repo", "main"},
	}
	for _, tt := range tests {
		c := &Config{DefaultBranch: tt.defaultBr, RepoBranches: tt.repoBranch}
		if got := c.BaseBranch(tt.repo); got != tt.want {
			t.Errorf("%s: BaseBranch(%q) = %q, want %q", tt.name, tt.repo, got, tt.want)
		}
	}
}

package rhodium

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rhodium/internal/brain"
	"rhodium/internal/gh"
)

// TestWritePromptFile_NoSymlinkFollow verifies writePromptFile does NOT
// overwrite a victim file via a pre-existing symlink at the formerly
// deterministic path. The old implementation built
// "$TMPDIR/rhodium-<safekey>-<action>.md" and called os.WriteFile, which
// follows symlinks; a co-located attacker on a shared /tmp could pre-create
// that path as a symlink to a victim-owned file and have rhodium overwrite
// it with the (partially attacker-controlled) prompt. The fix moves to
// os.CreateTemp, which uses O_CREATE|O_EXCL on an unpredictable name.
func TestWritePromptFile_NoSymlinkFollow(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	pr := gh.PR{Repo: "acme/web", Number: 42}
	actionName := "review"

	// Pre-create the file the attacker wants overwritten.
	victim := filepath.Join(tmp, "victim.txt")
	const victimContents = "DO NOT OVERWRITE"
	if err := os.WriteFile(victim, []byte(victimContents), 0o600); err != nil {
		t.Fatal(err)
	}

	// Pre-create a symlink at the deterministic path that the old
	// implementation would have used.
	safeKey := strings.ReplaceAll(brain.PRKey(pr.Repo, pr.Number), "/", "-")
	oldDeterministicPath := filepath.Join(tmp, "rhodium-"+safeKey+"-"+actionName+".md")
	if err := os.Symlink(victim, oldDeterministicPath); err != nil {
		t.Fatal(err)
	}

	// Call the production function with a known prompt body.
	const prompt = "attacker-controlled prompt body"
	got, err := writePromptFile(pr, actionName, prompt)
	if err != nil {
		t.Fatalf("writePromptFile: %v", err)
	}
	t.Cleanup(func() { os.Remove(got) })

	// The victim file MUST be untouched.
	gotVictim, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(gotVictim) != victimContents {
		t.Fatalf("symlink follow attack succeeded: victim was overwritten\n got: %q\nwant: %q", string(gotVictim), victimContents)
	}

	// The returned path must be a regular file (not a symlink) and must
	// contain the prompt body.
	info, err := os.Lstat(got)
	if err != nil {
		t.Fatalf("lstat returned path: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("writePromptFile returned a symlink: %s", got)
	}
	gotBody, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read returned path: %v", err)
	}
	if string(gotBody) != prompt {
		t.Fatalf("returned file contents = %q; want %q", string(gotBody), prompt)
	}

	// The returned path MUST NOT be the deterministic path (else an attacker
	// could still pre-create that name).
	if got == oldDeterministicPath {
		t.Fatalf("writePromptFile returned the deterministic path %q — predictable", got)
	}
}

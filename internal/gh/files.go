package gh

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"rhodium/internal/shellout"
)

// ErrFileNotFound is returned by FetchFileAtRef when the underlying gh API
// call distinguishes a legitimate "file does not exist at this ref" response
// (HTTP 404) from a transient transport / auth / parse failure. Callers that
// want to treat missing files as "gone" (e.g., stale-note resolution) should
// `errors.Is(err, ErrFileNotFound)`; any other non-nil error means the result
// is unreliable and should NOT be interpreted as "file is gone".
var ErrFileNotFound = errors.New("gh: file not found at ref")

// FetchFileAtRefFn is the injectable seam for FetchFileAtRef so tests can
// fake gh shell-outs without spawning subprocesses. Production code MUST
// always call gh.FetchFileAtRef (which dispatches through this var); tests
// can swap this for a deterministic stub and restore via t.Cleanup.
var FetchFileAtRefFn = fetchFileAtRefReal

type FileChange struct {
	Path      string
	Additions int
	Deletions int
	Blob      string // blob SHA at the PR's current head
	Patch     string // unified diff vs base (may be empty for binary or huge files)
}

type ghAPIFile struct {
	Sha       string `json:"sha"`
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch"`
}

// ListPRFiles fetches the PR's changed files via `gh api`, returning per-file
// blob SHAs and patches in a single call.
func ListPRFiles(repo string, number int) ([]FileChange, error) {
	out, err := shellout.Output("gh", "api",
		"--paginate",
		fmt.Sprintf("repos/%s/pulls/%d/files", repo, number),
	)
	if err != nil {
		return nil, fmt.Errorf("gh api pulls files %s#%d: %w", repo, number, err)
	}
	var items []ghAPIFile
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("parse files json: %w", err)
	}
	files := make([]FileChange, 0, len(items))
	for _, it := range items {
		files = append(files, FileChange{
			Path:      it.Filename,
			Additions: it.Additions,
			Deletions: it.Deletions,
			Blob:      it.Sha,
			Patch:     it.Patch,
		})
	}
	return files, nil
}

// FetchCompare returns the files that changed between two commits using the
// GitHub compare API. Files not in the result haven't changed — they're
// automatically caught up. The returned FileChanges include patches for only
// the delta between base and head.
func FetchCompare(repo, base, head string) ([]FileChange, error) {
	out, err := shellout.Output("gh", "api",
		fmt.Sprintf("repos/%s/compare/%s...%s", repo, base, head),
	)
	if err != nil {
		return nil, fmt.Errorf("gh api compare %s %s...%s: %w", repo, base, head, err)
	}
	var result struct {
		Files []ghAPIFile `json:"files"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("parse compare json: %w", err)
	}
	files := make([]FileChange, 0, len(result.Files))
	for _, it := range result.Files {
		files = append(files, FileChange{
			Path:      it.Filename,
			Additions: it.Additions,
			Deletions: it.Deletions,
			Blob:      it.Sha,
			Patch:     it.Patch,
		})
	}
	return files, nil
}

// FetchFileAtRef fetches file content at a specific git ref (commit SHA,
// branch). It distinguishes three outcomes:
//
//   - (content, nil): file present, decoded successfully.
//   - ("", ErrFileNotFound): file does not exist at that ref (HTTP 404).
//     Callers may treat this as "file is gone".
//   - ("", err) for any other err: transient failure (network, auth,
//     parse, decode). Callers MUST NOT interpret this as "file is gone";
//     the result is unreliable and the operation should be retried or
//     skipped.
//
// Dispatches through FetchFileAtRefFn so tests can inject fakes.
func FetchFileAtRef(repo, path, ref string) (string, error) {
	return FetchFileAtRefFn(repo, path, ref)
}

func fetchFileAtRefReal(repo, path, ref string) (string, error) {
	out, err := shellout.Output("gh", "api",
		fmt.Sprintf("repos/%s/contents/%s?ref=%s", repo, path, ref),
	)
	if err != nil {
		// gh prints "HTTP 404" on stderr when the resource is genuinely
		// missing. Anything else (network, auth, rate-limit, 5xx, exec
		// failure) is transient — surface a real error.
		var se *shellout.Error
		if errors.As(err, &se) && strings.Contains(se.Stderr, "HTTP 404") {
			return "", ErrFileNotFound
		}
		return "", fmt.Errorf("gh api contents %s %s@%s: %w", repo, path, ref, err)
	}
	var content struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(out, &content); err != nil {
		return "", fmt.Errorf("parse contents json %s %s@%s: %w", repo, path, ref, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(content.Content, "\n", ""))
	if err != nil {
		return "", fmt.Errorf("decode contents %s %s@%s: %w", repo, path, ref, err)
	}
	return string(decoded), nil
}

func FetchBlob(repo, sha string) (string, error) {
	out, err := shellout.Output("gh", "api",
		fmt.Sprintf("repos/%s/git/blobs/%s", repo, sha),
	)
	if err != nil {
		return "", fmt.Errorf("gh api blob %s %s: %w", repo, sha, err)
	}
	var blob struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(out, &blob); err != nil {
		return "", err
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(blob.Content, "\n", ""))
	if err != nil {
		return "", fmt.Errorf("decode blob: %w", err)
	}
	return string(decoded), nil
}

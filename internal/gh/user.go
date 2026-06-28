package gh

import (
	"encoding/json"
	"fmt"

	"rhodium/internal/shellout"
)

// Contributor is a flattened row from GET repos/:o/:r/contributors. Login
// is the @-mention handle; Contributions drives sort order in the picker.
type Contributor struct {
	Login         string
	Contributions int
}

type ghAPIContributor struct {
	Login         string `json:"login"`
	Contributions int    `json:"contributions"`
}

// FetchUser returns the login of whoever `gh` is authenticated as.
// Used to auto-populate Config.GitHubUser when the user hasn't set it.
func FetchUser() (string, error) {
	out, err := shellout.Output("gh", "api", "user")
	if err != nil {
		return "", fmt.Errorf("gh api user: %w", err)
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(out, &u); err != nil {
		return "", fmt.Errorf("parse gh api user: %w", err)
	}
	return u.Login, nil
}

// ListContributors pulls up to a few hundred contributors via the GitHub API
// (sorted by contribution count, descending — GitHub's default). One call
// per repo is cached on *app for the rest of the session; this function has
// no caching of its own.
func ListContributors(repo string) ([]Contributor, error) {
	out, err := shellout.Output("gh", "api",
		"--paginate",
		fmt.Sprintf("repos/%s/contributors?per_page=100", repo),
	)
	if err != nil {
		return nil, fmt.Errorf("gh api contributors %s: %w", repo, err)
	}
	// --paginate concatenates JSON arrays by stripping the outer brackets
	// between pages, but `gh` already emits one valid array for us when the
	// response fits in a single page. For multi-page it emits a single merged
	// array, so a plain unmarshal handles both cases.
	var items []ghAPIContributor
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("parse contributors json: %w", err)
	}
	contribs := make([]Contributor, 0, len(items))
	for _, it := range items {
		if it.Login == "" {
			continue // anonymous contributors have no login
		}
		contribs = append(contribs, Contributor{Login: it.Login, Contributions: it.Contributions})
	}
	return contribs, nil
}

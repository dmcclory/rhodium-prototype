package brain

import (
	"fmt"
)

// SetPRStatus sets a custom local status for a PR. User-set statuses
// override any auto-detected status. Setting to an empty string clears
// the status.
func (b *Brain) SetPRStatus(repo string, pr int, status string) error {
	key := PRKey(repo, pr)
	if status == "" {
		_, err := b.db.Exec(`DELETE FROM pr_statuses WHERE pr_key = ?`, key)
		if err != nil {
			return fmt.Errorf("clear status for %s: %w", key, err)
		}
		return nil
	}
	_, err := b.db.Exec(
		`INSERT INTO pr_statuses (pr_key, status, set_by) VALUES (?, ?, 'user')
		 ON CONFLICT(pr_key) DO UPDATE SET status = excluded.status, set_at = datetime('now'), set_by = 'user'`,
		key, status,
	)
	if err != nil {
		return fmt.Errorf("set status for %s: %w", key, err)
	}
	return nil
}

// PRStatus returns the current custom status for a PR, or "" if none is set.
func (b *Brain) PRStatus(repo string, pr int) string {
	var status string
	key := PRKey(repo, pr)
	err := b.db.QueryRow(`SELECT status FROM pr_statuses WHERE pr_key = ?`, key).Scan(&status)
	if err != nil {
		return ""
	}
	return status
}

// AllPRStatuses returns all PRs that have a custom status set, along with
// their status and when it was set.
func (b *Brain) AllPRStatuses() []PRStatusEntry {
	rows, err := b.db.Query(`SELECT pr_key, status, set_at FROM pr_statuses ORDER BY set_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []PRStatusEntry
	for rows.Next() {
		var e PRStatusEntry
		if rows.Scan(&e.PRKey, &e.Status, &e.SetAt) == nil {
			out = append(out, e)
		}
	}
	return out
}

// PRStatusEntry is one row from AllPRStatuses.
type PRStatusEntry struct {
	PRKey  string `json:"pr_key"`
	Status string `json:"status"`
	SetAt  string `json:"set_at"`
}

// PRStatusByKeys returns the statuses for the given pr_keys (returns a map
// keyed by pr_key; missing keys are absent from the map).
func (b *Brain) PRStatusByKeys(keys []string) map[string]string {
	if len(keys) == 0 {
		return nil
	}
	// Build placeholders.
	args := make([]interface{}, len(keys))
	placeholders := make([]byte, 0, len(keys)*2)
	for i, k := range keys {
		args[i] = k
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
	}
	query := fmt.Sprintf(`SELECT pr_key, status FROM pr_statuses WHERE pr_key IN (%s)`, string(placeholders))
	rows, err := b.db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var key, status string
		if rows.Scan(&key, &status) == nil {
			m[key] = status
		}
	}
	return m
}
